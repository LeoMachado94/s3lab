package s3api

import (
	"net/http"

	"github.com/LeoMachado94/s3lab/internal/s3api/s3err"
	"github.com/LeoMachado94/s3lab/internal/s3op"
)

// CORS handling. Owner: task T2.B.

// CORSConfig applies to every bucket (s3lab has no per-bucket CORS rules).
type CORSConfig struct {
	AllowedOrigins []string
	AllowedMethods []string
	AllowedHeaders []string
	ExposeHeaders  []string
	MaxAgeSeconds  int
}

// DefaultCORS is permissive so browser direct-upload tests work out of the box.
func DefaultCORS() CORSConfig {
	return CORSConfig{
		AllowedOrigins: []string{"*"},
		AllowedMethods: []string{"GET", "PUT", "POST", "DELETE", "HEAD"},
		AllowedHeaders: []string{"*"},
		ExposeHeaders:  []string{"ETag", "x-amz-request-id", "x-amz-version-id", "Content-Length", "Content-Range"},
		MaxAgeSeconds:  3600,
	}
}

// CORS adds CORS response headers to actual (non-preflight) requests.
func (h *Handler) CORS(next http.Handler) http.Handler {
	return next
}

func (h *Handler) corsPreflight(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	s3err.Write(w, r, s3err.NotImplemented)
}
