// Package auth verifies AWS Signature Version 4 requests. Owner: task T1.C.
package auth

import (
	"errors"
	"net/http"
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
	return Result{}, errors.New("auth: not implemented")
}

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
