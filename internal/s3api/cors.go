package s3api

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

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

var corsAccessForbidden = s3err.Error{Code: "AccessForbidden", Status: http.StatusForbidden}

func (h *Handler) corsEnabled() bool { return len(h.cors.AllowedOrigins) > 0 }

func (h *Handler) corsWildcard() bool { return slices.Contains(h.cors.AllowedOrigins, "*") }

func (h *Handler) corsOriginAllowed(origin string) bool {
	return origin != "" && (h.corsWildcard() || slices.Contains(h.cors.AllowedOrigins, origin))
}

func (h *Handler) corsAllowOrigin(origin string) string {
	if h.corsWildcard() {
		return "*"
	}
	return origin
}

// CORS adds CORS response headers to actual (non-preflight) requests.
func (h *Handler) CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if r.Method != http.MethodOptions && h.corsOriginAllowed(origin) {
			hd := w.Header()
			hd.Set("Access-Control-Allow-Origin", h.corsAllowOrigin(origin))
			if len(h.cors.ExposeHeaders) > 0 {
				hd.Set("Access-Control-Expose-Headers", strings.Join(h.cors.ExposeHeaders, ", "))
			}
			hd.Add("Vary", "Origin")
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) corsHeadersAllowed(requested string) bool {
	if slices.Contains(h.cors.AllowedHeaders, "*") {
		return true
	}
	for _, name := range strings.Split(requested, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if !slices.ContainsFunc(h.cors.AllowedHeaders, func(a string) bool { return strings.EqualFold(a, name) }) {
			return false
		}
	}
	return true
}

func (h *Handler) corsPreflight(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	if !h.corsEnabled() {
		s3err.Write(w, r, corsAccessForbidden.WithMessage("CORSResponse: CORS is not enabled for this bucket."))
		return
	}
	origin := r.Header.Get("Origin")
	method := r.Header.Get("Access-Control-Request-Method")
	if origin == "" || method == "" {
		s3err.Write(w, r, s3err.Error{Code: "BadRequest", Message: "Insufficient information. Origin request header needed.", Status: http.StatusBadRequest})
		return
	}
	reqHeaders := r.Header.Get("Access-Control-Request-Headers")
	if !h.corsOriginAllowed(origin) || !slices.Contains(h.cors.AllowedMethods, method) || !h.corsHeadersAllowed(reqHeaders) {
		s3err.Write(w, r, corsAccessForbidden.WithMessage("CORSResponse: This CORS request is not allowed. This is usually because the evalution of Origin, request method / Access-Control-Request-Method or Access-Control-Request-Headers are not whitelisted by the resource's CORS spec."))
		return
	}
	hd := w.Header()
	hd.Set("Access-Control-Allow-Origin", h.corsAllowOrigin(origin))
	hd.Set("Access-Control-Allow-Methods", strings.Join(h.cors.AllowedMethods, ", "))
	switch {
	case slices.Contains(h.cors.AllowedHeaders, "*"):
		if reqHeaders != "" {
			hd.Set("Access-Control-Allow-Headers", reqHeaders)
		}
	case len(h.cors.AllowedHeaders) > 0:
		hd.Set("Access-Control-Allow-Headers", strings.Join(h.cors.AllowedHeaders, ", "))
	}
	if len(h.cors.ExposeHeaders) > 0 {
		hd.Set("Access-Control-Expose-Headers", strings.Join(h.cors.ExposeHeaders, ", "))
	}
	if h.cors.MaxAgeSeconds > 0 {
		hd.Set("Access-Control-Max-Age", strconv.Itoa(h.cors.MaxAgeSeconds))
	}
	hd.Add("Vary", "Origin")
	hd.Add("Vary", "Access-Control-Request-Headers")
	hd.Add("Vary", "Access-Control-Request-Method")
	writeEmpty(w, http.StatusOK)
}
