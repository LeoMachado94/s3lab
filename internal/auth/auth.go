// Package auth verifies AWS Signature Version 4 requests. Owner: task T1.C.
package auth

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/LeoMachado94/s3lab/internal/s3op"
)

// CredentialStore resolves an access key to its secret.
type CredentialStore interface {
	Lookup(accessKey string) (secret string, ok bool)
}

// StaticCredentials maps access key -> secret.
type StaticCredentials map[string]string

func (s StaticCredentials) Lookup(accessKey string) (string, bool) {
	secret, ok := s[accessKey]
	return secret, ok
}

// Error is an auth failure carrying its S3 error code (implements s3err.Coder).
type Error struct {
	Code    string
	Message string
	Status  int
}

func (e *Error) Error() string   { return e.Code + ": " + e.Message }
func (e *Error) S3Code() string  { return e.Code }
func (e *Error) HTTPStatus() int { return e.Status }

var (
	ErrAccessDenied          = &Error{"AccessDenied", "Access Denied", http.StatusForbidden}
	ErrExpiredPresign        = &Error{"AccessDenied", "Request has expired", http.StatusForbidden}
	ErrHeaderMalformed       = &Error{"AuthorizationHeaderMalformed", "The authorization header is malformed.", http.StatusBadRequest}
	ErrQueryMalformed        = &Error{"AuthorizationQueryParametersError", "Query-string authentication is malformed.", http.StatusBadRequest}
	ErrInvalidAccessKeyID    = &Error{"InvalidAccessKeyId", "The AWS Access Key Id you provided does not exist in our records.", http.StatusForbidden}
	ErrSignatureDoesNotMatch = &Error{"SignatureDoesNotMatch", "The request signature we calculated does not match the signature you provided.", http.StatusForbidden}
	ErrRequestTimeTooSkewed  = &Error{"RequestTimeTooSkewed", "The difference between the request time and the server's time is too large.", http.StatusForbidden}
	ErrContentSHA256Mismatch = &Error{"XAmzContentSHA256Mismatch", "The provided 'x-amz-content-sha256' header does not match what was computed.", http.StatusBadRequest}
	ErrUnsupportedSignature  = &Error{"InvalidRequest", "Please use AWS4-HMAC-SHA256.", http.StatusBadRequest}
	ErrHeadersNotSigned      = &Error{"AccessDenied", "There were headers present in the request which were not signed", http.StatusForbidden}
	ErrMissingDate           = &Error{"AccessDenied", "AWS authentication requires a valid Date or x-amz-date header", http.StatusForbidden}
)

// Result describes an authenticated request.
type Result struct {
	AccessKey string
	Method    s3op.AuthResult
}

type Verifier struct {
	creds          CredentialStore
	region         string
	now            func() time.Time
	maxSkew        time.Duration
	allowAnonymous bool
}

type Option func(*Verifier)

// WithClock injects the time source (tests).
func WithClock(now func() time.Time) Option { return func(v *Verifier) { v.now = now } }

// WithMaxSkew overrides the allowed clock skew (default 15 minutes).
func WithMaxSkew(d time.Duration) Option { return func(v *Verifier) { v.maxSkew = d } }

// WithAnonymous lets unsigned requests through as anonymous.
func WithAnonymous(allow bool) Option { return func(v *Verifier) { v.allowAnonymous = allow } }

// NewVerifier builds a SigV4 verifier. Region is informational: the region in
// the credential scope is accepted as sent so any SDK region config works.
func NewVerifier(creds CredentialStore, region string, opts ...Option) *Verifier {
	v := &Verifier{creds: creds, region: region, now: time.Now, maxSkew: 15 * time.Minute}
	for _, o := range opts {
		o(v)
	}
	return v
}

// Verify authenticates r. On success it may replace r.Body with a reader that
// decodes aws-chunked payloads or verifies x-amz-content-sha256 at EOF (the
// reader then returns ErrContentSHA256Mismatch).
func (v *Verifier) Verify(r *http.Request) (Result, error) {
	authz := r.Header.Get("Authorization")
	q := r.URL.Query()
	presigned := q.Has("X-Amz-Algorithm") || q.Has("X-Amz-Signature") || q.Has("X-Amz-Credential")
	switch {
	case authz != "" && presigned:
		return Result{}, ErrQueryMalformed
	case authz == "" && !presigned:
		if v.allowAnonymous {
			return Result{Method: s3op.AuthNone}, nil
		}
		return Result{}, ErrAccessDenied
	}

	var sr *sigRequest
	var err error
	if presigned {
		sr, err = parsePresigned(q)
	} else {
		sr, err = parseAuthorization(authz)
	}
	if err != nil {
		return Result{}, err
	}

	secret, ok := v.creds.Lookup(sr.cred.accessKey)
	if !ok {
		return Result{}, ErrInvalidAccessKeyID
	}
	if !presigned && hasUnsignedAmzHeader(r, sr.signedHeaders) {
		return Result{}, ErrHeadersNotSigned
	}
	t, err := v.requestTime(r, sr)
	if err != nil {
		return Result{}, err
	}

	payloadHash := r.Header.Get("X-Amz-Content-Sha256")
	if payloadHash == "" {
		if !presigned {
			return Result{}, ErrHeaderMalformed
		}
		payloadHash = unsignedPayload
	}

	amzDate := t.Format(isoFormat)
	sts := algorithm + "\n" + amzDate + "\n" + sr.cred.scope() + "\n" +
		hexSHA256([]byte(canonicalRequest(r, sr, payloadHash)))
	key := signingKey(secret, sr.cred)
	want := hex.EncodeToString(hmacSHA256(key, sts))
	if !hmac.Equal([]byte(want), []byte(sr.signature)) {
		return Result{}, ErrSignatureDoesNotMatch
	}

	if err := wrapBody(r, payloadHash, chunkOpts{
		key: key, amzDate: amzDate, scope: sr.cred.scope(), seedSig: want, declared: -1,
	}); err != nil {
		return Result{}, err
	}

	method := s3op.AuthHeader
	if presigned {
		method = s3op.AuthPresigned
	}
	return Result{AccessKey: sr.cred.accessKey, Method: method}, nil
}

// hasUnsignedAmzHeader reports whether r carries an x-amz-* header missing from signed.
func hasUnsignedAmzHeader(r *http.Request, signed []string) bool {
	for name := range r.Header {
		l := strings.ToLower(name)
		if strings.HasPrefix(l, "x-amz-") && !slices.Contains(signed, l) {
			return true
		}
	}
	return false
}

// wrapBody installs the payload verification or aws-chunked decoding reader.
func wrapBody(r *http.Request, payloadHash string, opts chunkOpts) error {
	switch payloadHash {
	case unsignedPayload:
		return nil
	case streamUnsignedTrailer, streamSigned, streamSignedTrailer:
		opts.signed = payloadHash != streamUnsignedTrailer
		opts.declared = -1
		if s := r.Header.Get("X-Amz-Decoded-Content-Length"); s != "" {
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil || n < 0 {
				return ErrHeaderMalformed
			}
			opts.declared = n
		}
		r.Body = newChunkReader(r.Body, opts)
		r.ContentLength = opts.declared
		return nil
	}
	want, err := hex.DecodeString(payloadHash)
	if err != nil || len(want) != sha256.Size {
		return ErrHeaderMalformed
	}
	if r.Body == nil || r.Body == http.NoBody {
		if !bytes.Equal(want, emptyDigest[:]) {
			return ErrContentSHA256Mismatch
		}
		return nil
	}
	r.Body = &hashReader{rc: r.Body, h: sha256.New(), want: want, remain: r.ContentLength}
	return nil
}

var emptyDigest = sha256.Sum256(nil)

// hashReader reports ErrContentSHA256Mismatch once the whole body was read
// (at EOF, or when the declared Content-Length has been consumed).
type hashReader struct {
	rc     io.ReadCloser
	h      hash.Hash
	want   []byte
	remain int64
	err    error
}

func (h *hashReader) Read(p []byte) (int, error) {
	if h.err != nil {
		return 0, h.err
	}
	n, err := h.rc.Read(p)
	h.h.Write(p[:n])
	if h.remain >= 0 {
		h.remain -= int64(n)
	}
	if errors.Is(err, io.EOF) || (err == nil && h.remain == 0) {
		if !hmac.Equal(h.h.Sum(nil), h.want) {
			h.err = ErrContentSHA256Mismatch
			return n, h.err
		}
	}
	return n, err
}

func (h *hashReader) Close() error { return h.rc.Close() }

// Middleware verifies every request except CORS preflights, records the
// outcome in s3op.Info and calls onErr on failure.
func Middleware(v *Verifier, onErr func(http.ResponseWriter, *http.Request, error)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			info := s3op.FromContext(r.Context())
			if info != nil && info.Op == s3op.CORSPreflight {
				next.ServeHTTP(w, r)
				return
			}
			res, err := v.Verify(r)
			if info != nil {
				info.AccessKey = res.AccessKey
				info.Auth = res.Method
				if err != nil {
					info.Auth = s3op.AuthFailed
				}
			}
			if err != nil {
				onErr(w, r, err)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
