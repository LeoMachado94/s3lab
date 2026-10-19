package s3api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCORSMiddleware(t *testing.T) {
	restricted := CORSConfig{
		AllowedOrigins: []string{"http://localhost:5173"},
		AllowedMethods: []string{"GET", "PUT"},
		AllowedHeaders: []string{"Content-Type"},
		ExposeHeaders:  []string{"ETag"},
	}
	tests := []struct {
		name       string
		cfg        CORSConfig
		origin     string
		method     string
		wantOrigin string
		wantExpose string
	}{
		{"wildcard", DefaultCORS(), "http://any.example", "GET", "*", "ETag, x-amz-request-id, x-amz-version-id, Content-Length, Content-Range"},
		{"exact match echoes origin", restricted, "http://localhost:5173", "GET", "http://localhost:5173", "ETag"},
		{"origin not allowed", restricted, "http://evil.example", "GET", "", ""},
		{"no origin", DefaultCORS(), "", "GET", "", ""},
		{"disabled", CORSConfig{}, "http://localhost:5173", "GET", "", ""},
		{"options passes through untouched", DefaultCORS(), "http://any.example", "OPTIONS", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := newTestHandler(t, WithCORS(tc.cfg))
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
			r := httptest.NewRequest(tc.method, "/bkt/key", nil)
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			rec := httptest.NewRecorder()
			h.CORS(next).ServeHTTP(rec, r)
			require.Equal(t, http.StatusTeapot, rec.Code)
			require.Equal(t, tc.wantOrigin, rec.Header().Get("Access-Control-Allow-Origin"))
			require.Equal(t, tc.wantExpose, rec.Header().Get("Access-Control-Expose-Headers"))
			if tc.wantOrigin != "" {
				require.Contains(t, rec.Header().Values("Vary"), "Origin")
			}
		})
	}
}

func TestCORSPreflight(t *testing.T) {
	restricted := CORSConfig{
		AllowedOrigins: []string{"http://localhost:5173"},
		AllowedMethods: []string{"GET", "PUT"},
		AllowedHeaders: []string{"Content-Type", "X-Amz-Meta-Foo"},
		ExposeHeaders:  []string{"ETag"},
		MaxAgeSeconds:  600,
	}
	tests := []struct {
		name        string
		cfg         CORSConfig
		headers     map[string]string
		status      int
		wantCode    string
		wantOrigin  string
		wantMethods string
		wantHeaders string
		wantMaxAge  string
	}{
		{
			name: "default allows and echoes requested headers", cfg: DefaultCORS(),
			headers: map[string]string{"Origin": "http://a.example", "Access-Control-Request-Method": "PUT", "Access-Control-Request-Headers": "content-type,x-amz-meta-x"},
			status:  200, wantOrigin: "*", wantMethods: "GET, PUT, POST, DELETE, HEAD", wantHeaders: "content-type,x-amz-meta-x", wantMaxAge: "3600",
		},
		{
			name: "restricted allowed", cfg: restricted,
			headers: map[string]string{"Origin": "http://localhost:5173", "Access-Control-Request-Method": "PUT", "Access-Control-Request-Headers": "content-type"},
			status:  200, wantOrigin: "http://localhost:5173", wantMethods: "GET, PUT", wantHeaders: "Content-Type, X-Amz-Meta-Foo", wantMaxAge: "600",
		},
		{
			name: "restricted origin denied", cfg: restricted,
			headers: map[string]string{"Origin": "http://evil.example", "Access-Control-Request-Method": "PUT"},
			status:  403, wantCode: "AccessForbidden",
		},
		{
			name: "method denied", cfg: restricted,
			headers: map[string]string{"Origin": "http://localhost:5173", "Access-Control-Request-Method": "DELETE"},
			status:  403, wantCode: "AccessForbidden",
		},
		{
			name: "header denied", cfg: restricted,
			headers: map[string]string{"Origin": "http://localhost:5173", "Access-Control-Request-Method": "PUT", "Access-Control-Request-Headers": "x-other"},
			status:  403, wantCode: "AccessForbidden",
		},
		{
			name: "disabled", cfg: CORSConfig{},
			headers: map[string]string{"Origin": "http://a.example", "Access-Control-Request-Method": "GET"},
			status:  403, wantCode: "AccessForbidden",
		},
		{
			name: "missing origin", cfg: DefaultCORS(),
			headers: map[string]string{"Access-Control-Request-Method": "GET"},
			status:  400, wantCode: "BadRequest",
		},
		{
			name: "missing request method", cfg: DefaultCORS(),
			headers: map[string]string{"Origin": "http://a.example"},
			status:  400, wantCode: "BadRequest",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := newTestHandler(t, WithCORS(tc.cfg))
			rec := do(t, h, testReq{method: "OPTIONS", target: "/bkt/key", headers: tc.headers})
			requireS3Error(t, rec, tc.status, tc.wantCode)
			require.Equal(t, tc.wantOrigin, rec.Header().Get("Access-Control-Allow-Origin"))
			require.Equal(t, tc.wantMethods, rec.Header().Get("Access-Control-Allow-Methods"))
			require.Equal(t, tc.wantHeaders, rec.Header().Get("Access-Control-Allow-Headers"))
			require.Equal(t, tc.wantMaxAge, rec.Header().Get("Access-Control-Max-Age"))
			if tc.status == 200 {
				require.Contains(t, strings.Join(rec.Header().Values("Vary"), ","), "Origin")
				require.Equal(t, "ETag", strings.Split(rec.Header().Get("Access-Control-Expose-Headers"), ",")[0])
			}
		})
	}
}
