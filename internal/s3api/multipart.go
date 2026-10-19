package s3api

import (
	"crypto/md5"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"hash"
	"io"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/LeoMachado94/s3lab/internal/s3api/s3err"
	"github.com/LeoMachado94/s3lab/internal/s3op"
	"github.com/LeoMachado94/s3lab/internal/storage"
)

const mpMaxListItems = 1000

func (h *Handler) createMultipartUpload(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	id, err := h.backend.CreateMultipartUpload(r.Context(), info.Bucket, info.Key, metaFromHeaders(r.Header))
	if err != nil {
		s3err.Write(w, r, err)
		return
	}
	writeXML(w, r, http.StatusOK, InitiateMultipartUploadResult{
		Xmlns: s3Namespace, Bucket: info.Bucket, Key: info.Key, UploadID: id,
	})
}

func (h *Handler) uploadPart(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	q := r.URL.Query()
	n, err := mpPartNumber(q.Get("partNumber"))
	if err != nil {
		s3err.Write(w, r, err)
		return
	}
	size, err := mpDeclaredSize(r)
	if err != nil {
		s3err.Write(w, r, err)
		return
	}
	if size > h.maxObjectSize {
		s3err.Write(w, r, s3err.EntityTooLarge)
		return
	}
	body, err := newMPBodyReader(r.Body, size, h.maxObjectSize, r.Header.Get("Content-MD5"))
	if err != nil {
		s3err.Write(w, r, err)
		return
	}
	part, err := h.backend.UploadPart(r.Context(), info.Bucket, info.Key, q.Get("uploadId"), n, body, size)
	if err != nil {
		s3err.Write(w, r, err)
		return
	}
	w.Header().Set("ETag", quoteETag(part.ETag))
	writeEmpty(w, http.StatusOK)
}

func (h *Handler) uploadPartCopy(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	q := r.URL.Query()
	n, err := mpPartNumber(q.Get("partNumber"))
	if err != nil {
		s3err.Write(w, r, err)
		return
	}
	src, err := parseCopySource(r.Header.Get("x-amz-copy-source"))
	if err != nil {
		s3err.Write(w, r, err)
		return
	}
	rng, err := mpCopyRange(r.Header.Get("x-amz-copy-source-range"))
	if err != nil {
		s3err.Write(w, r, err)
		return
	}
	rc, srcInfo, err := h.backend.GetObject(r.Context(), src.Bucket, src.Key, rng)
	if err != nil {
		s3err.Write(w, r, err)
		return
	}
	defer func() { _ = rc.Close() }()

	length := srcInfo.Size
	if rng != nil {
		if rng.End >= srcInfo.Size {
			s3err.Write(w, r, s3err.InvalidRequest.WithMessage("The specified copy range is invalid for the source object size"))
			return
		}
		length = rng.End - rng.Start + 1
	}
	if !mpCopyConditionsMet(r.Header, srcInfo) {
		s3err.Write(w, r, s3err.PreconditionFailed)
		return
	}
	if length > h.maxObjectSize {
		s3err.Write(w, r, s3err.EntityTooLarge)
		return
	}
	part, err := h.backend.UploadPart(r.Context(), info.Bucket, info.Key, q.Get("uploadId"), n, rc, length)
	if err != nil {
		s3err.Write(w, r, err)
		return
	}
	writeXML(w, r, http.StatusOK, CopyPartResult{
		Xmlns: s3Namespace, LastModified: timeISO(part.LastModified), ETag: quoteETag(part.ETag),
	})
}

func (h *Handler) completeMultipartUpload(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	var req CompleteMultipartUpload
	if err := decodeXMLBody(r, &req); err != nil {
		s3err.Write(w, r, err)
		return
	}
	if len(req.Parts) == 0 {
		s3err.Write(w, r, s3err.MalformedXML)
		return
	}
	parts := make([]storage.CompletePart, len(req.Parts))
	for i, p := range req.Parts {
		parts[i] = storage.CompletePart{PartNumber: p.PartNumber, ETag: unquoteETag(p.ETag)}
	}
	obj, err := h.backend.CompleteMultipartUpload(r.Context(), info.Bucket, info.Key, r.URL.Query().Get("uploadId"), parts)
	if err != nil {
		s3err.Write(w, r, err)
		return
	}
	loc := url.URL{Path: "/" + info.Bucket + "/" + info.Key}
	writeXML(w, r, http.StatusOK, CompleteMultipartUploadResult{
		Xmlns:    s3Namespace,
		Location: "http://" + r.Host + loc.EscapedPath(),
		Bucket:   info.Bucket,
		Key:      info.Key,
		ETag:     quoteETag(obj.ETag),
	})
}

func (h *Handler) abortMultipartUpload(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	if err := h.backend.AbortMultipartUpload(r.Context(), info.Bucket, info.Key, r.URL.Query().Get("uploadId")); err != nil {
		s3err.Write(w, r, err)
		return
	}
	writeEmpty(w, http.StatusNoContent)
}

func (h *Handler) listParts(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	marker, err := queryInt(r, "part-number-marker", 0, 0)
	if err != nil {
		s3err.Write(w, r, err)
		return
	}
	maxParts, err := queryInt(r, "max-parts", mpMaxListItems, mpMaxListItems)
	if err != nil {
		s3err.Write(w, r, err)
		return
	}
	if maxParts == 0 {
		maxParts = mpMaxListItems
	}
	uploadID := r.URL.Query().Get("uploadId")
	res, err := h.backend.ListParts(r.Context(), info.Bucket, info.Key, uploadID,
		storage.ListPartsOptions{PartNumberMarker: marker, MaxParts: maxParts})
	if err != nil {
		s3err.Write(w, r, err)
		return
	}
	out := ListPartsResult{
		Xmlns:            s3Namespace,
		Bucket:           info.Bucket,
		Key:              info.Key,
		UploadID:         uploadID,
		Initiator:        defaultOwner,
		Owner:            defaultOwner,
		StorageClass:     "STANDARD",
		PartNumberMarker: marker,
		MaxParts:         maxParts,
		IsTruncated:      res.IsTruncated,
	}
	if res.IsTruncated {
		out.NextPartNumberMarker = res.NextPartNumberMarker
	}
	for _, p := range res.Parts {
		out.Parts = append(out.Parts, PartEntry{
			PartNumber: p.PartNumber, LastModified: timeISO(p.LastModified), ETag: quoteETag(p.ETag), Size: p.Size,
		})
	}
	writeXML(w, r, http.StatusOK, out)
}

// listMultipartUploads ignores delimiter (no CommonPrefixes grouping).
func (h *Handler) listMultipartUploads(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	q := r.URL.Query()
	maxUploads, err := queryInt(r, "max-uploads", mpMaxListItems, mpMaxListItems)
	if err != nil {
		s3err.Write(w, r, err)
		return
	}
	if maxUploads == 0 {
		maxUploads = mpMaxListItems
	}
	res, err := h.backend.ListMultipartUploads(r.Context(), info.Bucket,
		storage.ListUploadsOptions{Prefix: q.Get("prefix"), MaxUploads: math.MaxInt32})
	if err != nil {
		s3err.Write(w, r, err)
		return
	}
	keyMarker, idMarker := q.Get("key-marker"), q.Get("upload-id-marker")
	uploads := slices.DeleteFunc(res.Uploads, func(u storage.Upload) bool {
		if keyMarker == "" {
			return false
		}
		return u.Key < keyMarker || (u.Key == keyMarker && (idMarker == "" || u.UploadID <= idMarker))
	})
	slices.SortFunc(uploads, func(a, b storage.Upload) int {
		if d := strings.Compare(a.Key, b.Key); d != 0 {
			return d
		}
		return strings.Compare(a.UploadID, b.UploadID)
	})
	out := ListMultipartUploadsResult{
		Xmlns:          s3Namespace,
		Bucket:         info.Bucket,
		KeyMarker:      keyMarker,
		UploadIDMarker: idMarker,
		Prefix:         q.Get("prefix"),
		MaxUploads:     maxUploads,
	}
	if len(uploads) > maxUploads {
		uploads = uploads[:maxUploads]
		out.IsTruncated = true
		last := uploads[len(uploads)-1]
		out.NextKeyMarker, out.NextUploadIDMarker = last.Key, last.UploadID
	}
	for _, u := range uploads {
		out.Uploads = append(out.Uploads, UploadEntry{
			Key: u.Key, UploadID: u.UploadID, Initiator: defaultOwner, Owner: defaultOwner,
			StorageClass: "STANDARD", Initiated: timeISO(u.Initiated),
		})
	}
	writeXML(w, r, http.StatusOK, out)
}

func mpPartNumber(v string) (int, error) {
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > storage.MaxPartNumber {
		return 0, s3err.InvalidArgument.WithMessage("Part number must be an integer between 1 and 10000, inclusive")
	}
	return n, nil
}

// mpDeclaredSize returns the payload size, or -1 for streaming bodies of unknown length.
func mpDeclaredSize(r *http.Request) (int64, error) {
	decoded := r.Header.Get("x-amz-decoded-content-length")
	streaming := decoded != "" || strings.HasPrefix(r.Header.Get("x-amz-content-sha256"), "STREAMING-")
	if decoded != "" {
		n, err := strconv.ParseInt(decoded, 10, 64)
		if err != nil || n < 0 {
			return 0, s3err.InvalidArgument.WithMessage("Invalid x-amz-decoded-content-length")
		}
		return n, nil
	}
	if streaming {
		return -1, nil
	}
	if r.ContentLength < 0 {
		return 0, s3err.MissingContentLength
	}
	return r.ContentLength, nil
}

// mpCopyRange parses x-amz-copy-source-range; unlike GET, both bounds are required.
func mpCopyRange(v string) (*storage.ByteRange, error) {
	if v == "" {
		return nil, nil
	}
	rng, err := parseRange(v)
	if err != nil || rng.Start < 0 || rng.End < 0 {
		return nil, s3err.InvalidArgument.WithMessage("The x-amz-copy-source-range value must be of the form bytes=first-last where first and last are the zero-based offsets of the first and last bytes to copy")
	}
	return rng, nil
}

func mpCopyConditionsMet(h http.Header, src storage.ObjectInfo) bool {
	etagMatches := func(v string) bool {
		for _, e := range strings.Split(v, ",") {
			if e = unquoteETag(e); e == "*" || e == src.ETag {
				return true
			}
		}
		return false
	}
	parse := func(name string) (time.Time, bool) {
		t, err := http.ParseTime(h.Get(name))
		return t, err == nil
	}
	ifMatch := h.Get("x-amz-copy-source-if-match")
	ifNoneMatch := h.Get("x-amz-copy-source-if-none-match")
	matchOK := ifMatch != "" && etagMatches(ifMatch)
	if ifMatch != "" && !matchOK {
		return false
	}
	if ifNoneMatch != "" && etagMatches(ifNoneMatch) {
		return false
	}
	lm := src.LastModified.Truncate(time.Second)
	if t, ok := parse("x-amz-copy-source-if-unmodified-since"); ok && !matchOK && lm.After(t) {
		return false
	}
	if t, ok := parse("x-amz-copy-source-if-modified-since"); ok && ifNoneMatch == "" && !lm.After(t) {
		return false
	}
	return true
}

// mpBodyReader enforces the declared size, the size cap and Content-MD5 at
// EOF, returning an error instead of io.EOF so backends never commit a bad part.
type mpBodyReader struct {
	r      io.Reader
	size   int64
	maxLen int64
	n      int64
	sum    []byte
	hash   hash.Hash
}

func newMPBodyReader(r io.Reader, size, maxLen int64, contentMD5 string) (*mpBodyReader, error) {
	b := &mpBodyReader{r: r, size: size, maxLen: maxLen}
	if contentMD5 != "" {
		sum, err := base64.StdEncoding.DecodeString(contentMD5)
		if err != nil || len(sum) != md5.Size {
			return nil, s3err.InvalidDigest
		}
		b.sum, b.hash = sum, md5.New()
	}
	return b, nil
}

func (b *mpBodyReader) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	b.n += int64(n)
	if b.hash != nil {
		b.hash.Write(p[:n])
	}
	if b.n > b.maxLen {
		return n, s3err.EntityTooLarge
	}
	switch {
	case errors.Is(err, io.EOF):
		if b.size >= 0 && b.n != b.size {
			return n, s3err.IncompleteBody
		}
		if b.hash != nil && subtle.ConstantTimeCompare(b.hash.Sum(nil), b.sum) != 1 {
			return n, s3err.BadDigest
		}
	case errors.Is(err, io.ErrUnexpectedEOF):
		return n, s3err.IncompleteBody
	}
	return n, err
}
