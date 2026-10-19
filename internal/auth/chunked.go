package auth

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const (
	maxChunkSize    = 64 << 20
	maxChunkHexLen  = 16
	maxChunkLine    = 512
	maxTrailerBytes = 16 << 10
)

func errMalformedChunk(msg string) error {
	return &Error{Code: "InvalidRequest", Message: "Malformed aws-chunked payload: " + msg, Status: http.StatusBadRequest}
}

func errMalformedDecodedLength() error {
	return errMalformedChunk("decoded length differs from x-amz-decoded-content-length")
}

// chunkOpts configures an aws-chunked decoder. When signed is false chunk
// signatures are neither required nor checked.
type chunkOpts struct {
	signed   bool
	key      []byte
	amzDate  string
	scope    string
	seedSig  string
	declared int64
}

// chunkReader decodes aws-chunked streaming bodies. Chunk data is passed
// through as it is read; a bad chunk signature surfaces at the end of that
// chunk, before EOF, so callers never see a clean end of an unverified body.
// Trailers (including x-amz-trailer-signature) are skipped, not verified.
type chunkReader struct {
	br        *bufio.Reader
	closer    io.Closer
	opts      chunkOpts
	prevSig   string
	wantSig   string
	h         hash.Hash
	remaining int64
	inChunk   bool
	total     int64
	err       error
}

func newChunkReader(rc io.ReadCloser, opts chunkOpts) *chunkReader {
	c := &chunkReader{br: bufio.NewReaderSize(rc, 8<<10), closer: rc, opts: opts, prevSig: opts.seedSig}
	if opts.signed {
		c.h = sha256.New()
	}
	return c
}

func (c *chunkReader) Close() error { return c.closer.Close() }

func (c *chunkReader) Read(p []byte) (int, error) {
	if c.err != nil {
		return 0, c.err
	}
	if len(p) == 0 {
		return 0, nil
	}
	if !c.inChunk {
		if err := c.nextChunk(); err != nil {
			c.err = err
			return 0, err
		}
		if !c.inChunk {
			c.err = io.EOF
			return 0, io.EOF
		}
	}
	if int64(len(p)) > c.remaining {
		p = p[:c.remaining]
	}
	n, err := c.br.Read(p)
	if c.h != nil {
		c.h.Write(p[:n])
	}
	c.remaining -= int64(n)
	c.total += int64(n)
	if c.opts.declared >= 0 && c.total > c.opts.declared {
		c.err = errMalformedDecodedLength()
		return n, c.err
	}
	if err != nil && c.remaining > 0 {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		c.err = err
		return n, err
	}
	if c.remaining == 0 {
		if err := c.endChunk(); err != nil {
			c.err = err
			return 0, err
		}
	}
	return n, nil
}

func (c *chunkReader) readLine() (string, error) {
	line, err := c.br.ReadSlice('\n')
	if err != nil {
		switch {
		case errors.Is(err, io.EOF):
			return "", io.ErrUnexpectedEOF
		case errors.Is(err, bufio.ErrBufferFull):
			return "", errMalformedChunk("line too long")
		}
		return "", fmt.Errorf("read chunk line: %w", err)
	}
	s, ok := strings.CutSuffix(string(line), "\r\n")
	if !ok {
		return "", errMalformedChunk("missing CRLF")
	}
	return s, nil
}

func (c *chunkReader) nextChunk() error {
	line, err := c.readLine()
	if err != nil {
		return err
	}
	if len(line) > maxChunkLine {
		return errMalformedChunk("chunk header too long")
	}
	sizeStr, ext, _ := strings.Cut(line, ";")
	if sizeStr == "" || len(sizeStr) > maxChunkHexLen {
		return errMalformedChunk("invalid chunk size")
	}
	size, err := strconv.ParseUint(sizeStr, 16, 64)
	if err != nil {
		return errMalformedChunk("invalid chunk size")
	}
	if size > maxChunkSize {
		return errMalformedChunk("chunk too large")
	}
	if c.opts.signed {
		sig, ok := strings.CutPrefix(ext, "chunk-signature=")
		if !ok || sig == "" {
			return errMalformedChunk("missing chunk signature")
		}
		c.wantSig = sig
		c.h.Reset()
	}
	if size == 0 {
		return c.finish()
	}
	c.remaining = int64(size)
	c.inChunk = true
	return nil
}

func (c *chunkReader) endChunk() error {
	c.inChunk = false
	var crlf [2]byte
	if _, err := io.ReadFull(c.br, crlf[:]); err != nil {
		if errors.Is(err, io.EOF) {
			return io.ErrUnexpectedEOF
		}
		return fmt.Errorf("read chunk end: %w", err)
	}
	if string(crlf[:]) != "\r\n" {
		return errMalformedChunk("missing CRLF after chunk data")
	}
	return c.verifyChunk()
}

func (c *chunkReader) verifyChunk() error {
	if !c.opts.signed {
		return nil
	}
	sts := "AWS4-HMAC-SHA256-PAYLOAD\n" + c.opts.amzDate + "\n" + c.opts.scope + "\n" +
		c.prevSig + "\n" + emptySHA256 + "\n" + hex.EncodeToString(c.h.Sum(nil))
	sig := hex.EncodeToString(hmacSHA256(c.opts.key, sts))
	if !hmac.Equal([]byte(sig), []byte(strings.ToLower(c.wantSig))) {
		return ErrSignatureDoesNotMatch
	}
	c.prevSig = sig
	return nil
}

// finish handles the zero-length chunk and skips the trailers.
func (c *chunkReader) finish() error {
	if err := c.verifyChunk(); err != nil {
		return err
	}
	used := 0
	for {
		line, err := c.readLine()
		if err != nil {
			return err
		}
		if line == "" {
			break
		}
		if used += len(line) + 2; used > maxTrailerBytes {
			return errMalformedChunk("trailer too large")
		}
	}
	if c.opts.declared >= 0 && c.total != c.opts.declared {
		return errMalformedDecodedLength()
	}
	return nil
}
