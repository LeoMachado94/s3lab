// Package console serves the admin API and the embedded web UI.
package console

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/LeoMachado94/s3lab/internal/faults"
	"github.com/LeoMachado94/s3lab/internal/reqlog"
	"github.com/LeoMachado94/s3lab/internal/seed"
	"github.com/LeoMachado94/s3lab/internal/storage"
	"github.com/LeoMachado94/s3lab/web"
)

const (
	timeLayout      = "2006-01-02T15:04:05.000Z07:00"
	maxBodyBytes    = 1 << 20
	defaultPingEach = 15 * time.Second

	defaultMaxObjectSize = 5 << 30
)

type Info struct {
	Version            string    `json:"version"`
	Region             string    `json:"region"`
	Storage            string    `json:"storage"`
	S3Port             int       `json:"s3Port"`
	ConsolePort        int       `json:"consolePort"`
	AccessKey          string    `json:"accessKey"`
	VirtualHostDomain  string    `json:"virtualHostDomain"`
	RequestLogCapacity int       `json:"requestLogCapacity"`
	StartedAt          time.Time `json:"-"`
}

// Deps wires the console. Backend must be the raw backend (not fault-wrapped)
// so storage faults never affect the console.
type Deps struct {
	Backend  storage.Backend
	Requests *reqlog.Store
	Faults   *faults.Engine
	Seeder   *seed.Seeder
	Info     Info
	Logger   *slog.Logger

	// AllowedHosts are extra Host values (port stripped, case-insensitive) accepted by the console.
	AllowedHosts []string
	// MaxObjectSize caps console uploads in bytes; 0 means 5 GiB.
	MaxObjectSize int64
	// Done, when closed, ends long-lived streams so graceful shutdown is not blocked.
	Done <-chan struct{}

	pingInterval time.Duration
}

type server struct {
	Deps
	// mu serializes reset and force-delete against each other.
	mu sync.Mutex
}

func New(d Deps) http.Handler {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.pingInterval <= 0 {
		d.pingInterval = defaultPingEach
	}
	if d.MaxObjectSize <= 0 {
		d.MaxObjectSize = defaultMaxObjectSize
	}
	s := &server{Deps: d}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /_s3lab/health", s.health)
	mux.HandleFunc("GET /_s3lab/info", s.info)

	mux.HandleFunc("GET /_s3lab/buckets", s.listBuckets)
	mux.HandleFunc("POST /_s3lab/buckets", s.createBucket)
	mux.HandleFunc("DELETE /_s3lab/buckets/{bucket}", s.deleteBucket)

	mux.HandleFunc("GET /_s3lab/buckets/{bucket}/objects", s.listObjects)
	mux.HandleFunc("DELETE /_s3lab/buckets/{bucket}/objects", s.deletePrefix)
	mux.HandleFunc("GET /_s3lab/buckets/{bucket}/object", s.getObject)
	mux.HandleFunc("PUT /_s3lab/buckets/{bucket}/object", s.putObject)
	mux.HandleFunc("DELETE /_s3lab/buckets/{bucket}/object", s.deleteObject)
	mux.HandleFunc("GET /_s3lab/buckets/{bucket}/download", s.download)

	mux.HandleFunc("GET /_s3lab/requests", s.listRequests)
	mux.HandleFunc("DELETE /_s3lab/requests", s.clearRequests)
	mux.HandleFunc("GET /_s3lab/requests/stream", s.streamRequests)

	mux.HandleFunc("GET /_s3lab/faults", s.listFaults)
	mux.HandleFunc("POST /_s3lab/faults", s.addFault)
	mux.HandleFunc("DELETE /_s3lab/faults/{id}", s.deleteFault)
	mux.HandleFunc("DELETE /_s3lab/faults", s.clearFaults)

	mux.HandleFunc("POST /_s3lab/reset", s.reset)

	mux.HandleFunc("/", s.static())

	return s.recoverer(s.hostGuard(sameOrigin(mux)))
}

func (s *server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			if rec == http.ErrAbortHandler { //nolint:errorlint // sentinel panic value
				panic(rec)
			}
			s.Logger.Error("console panic", "panic", rec, "path", r.URL.Path)
			writeError(w, http.StatusInternalServerError, "InternalError", "internal error")
		}()
		next.ServeHTTP(w, r)
	})
}

// hostGuard rejects admin API requests whose Host is not local/private, blocking DNS rebinding.
func (s *server) hostGuard(next http.Handler) http.Handler {
	allowed := make(map[string]struct{}, len(s.AllowedHosts))
	for _, h := range s.AllowedHosts {
		allowed[strings.ToLower(strings.TrimSpace(h))] = struct{}{}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/_s3lab/") && !hostAllowed(r.Host, allowed) {
			writeError(w, http.StatusForbidden, "Forbidden", "host not allowed; set S3LAB_CONSOLE_ALLOWED_HOSTS")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func hostAllowed(hostport string, allowed map[string]struct{}) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
	if host == "" {
		return false
	}
	if net.ParseIP(host) != nil || !strings.Contains(host, ".") {
		return true
	}
	if _, ok := allowed[host]; ok {
		return true
	}
	for _, suffix := range []string{".localhost", ".local", ".test", ".internal"} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

// sameOrigin blocks cross-site state-changing requests (no auth, so a web page
// must not be able to drive the admin API on localhost).
func sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || u.Host != r.Host {
					writeError(w, http.StatusForbidden, "Forbidden", "cross-origin requests are not allowed")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) static() http.HandlerFunc {
	sub, err := fs.Sub(web.Static, "static")
	if err != nil {
		panic(err)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/_s3lab/") {
			writeError(w, http.StatusNotFound, "NotFound", "unknown admin API endpoint")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeError(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "method not allowed")
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if st, err := fs.Stat(sub, name); name == "" || err != nil || st.IsDir() {
			name = "index.html"
		}
		if name == "index.html" {
			w.Header().Set("Cache-Control", "no-cache")
		}
		http.ServeFileFS(w, r, sub, name)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func (s *server) storageError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrNoSuchBucket):
		writeError(w, http.StatusNotFound, "NoSuchBucket", err.Error())
	case errors.Is(err, storage.ErrNoSuchKey):
		writeError(w, http.StatusNotFound, "NoSuchKey", err.Error())
	case errors.Is(err, storage.ErrBucketNotEmpty):
		writeError(w, http.StatusConflict, "BucketNotEmpty", err.Error())
	case errors.Is(err, storage.ErrInvalidBucketName):
		writeError(w, http.StatusBadRequest, "InvalidBucketName", err.Error())
	case errors.Is(err, storage.ErrBucketAlreadyOwnedByYou):
		writeError(w, http.StatusConflict, "BucketAlreadyOwnedByYou", err.Error())
	case errors.Is(err, storage.ErrInvalidKey), errors.Is(err, storage.ErrKeyTooLong):
		writeError(w, http.StatusBadRequest, "BadRequest", err.Error())
	default:
		s.Logger.Error("console storage error", "err", err)
		writeError(w, http.StatusInternalServerError, "InternalError", "internal error")
	}
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "BadRequest", "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func ts(t time.Time) string { return t.UTC().Format(timeLayout) }

func (s *server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": s.Info.Version})
}

func (s *server) info(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, struct {
		Info
		StartedAt string `json:"startedAt"`
	}{s.Info, ts(s.Info.StartedAt)})
}
