// Package server wires storage, auth, middlewares, the S3 API and the console
// into two HTTP servers.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime/debug"
	"strconv"
	"sync"
	"time"

	"github.com/LeoMachado94/s3lab/internal/auth"
	"github.com/LeoMachado94/s3lab/internal/config"
	"github.com/LeoMachado94/s3lab/internal/console"
	"github.com/LeoMachado94/s3lab/internal/faults"
	"github.com/LeoMachado94/s3lab/internal/reqlog"
	"github.com/LeoMachado94/s3lab/internal/s3api"
	"github.com/LeoMachado94/s3lab/internal/s3api/s3err"
	"github.com/LeoMachado94/s3lab/internal/s3op"
	"github.com/LeoMachado94/s3lab/internal/seed"
	"github.com/LeoMachado94/s3lab/internal/storage"
	"github.com/LeoMachado94/s3lab/internal/storage/fs"
	"github.com/LeoMachado94/s3lab/internal/storage/memory"
)

const shutdownGrace = 10 * time.Second

type Server struct {
	cfg     config.Config
	log     *slog.Logger
	backend storage.Backend
	version string

	requests *reqlog.Store
	faults   *faults.Engine
	s3       http.Handler
	console  http.Handler

	done     chan struct{}
	doneOnce sync.Once

	mu          sync.Mutex
	s3Addr      string
	consoleAddr string
}

type Option func(*Server)

// WithLogger sets the logger (default: built from cfg.LogLevel/LogFormat).
func WithLogger(l *slog.Logger) Option { return func(s *Server) { s.log = l } }

// WithBackend overrides the backend chosen by cfg.Storage (tests).
func WithBackend(b storage.Backend) Option { return func(s *Server) { s.backend = b } }

// WithVersion sets the version reported by the console.
func WithVersion(v string) Option { return func(s *Server) { s.version = v } }

func newLogger(cfg config.Config) *slog.Logger {
	var lvl slog.Level
	_ = lvl.UnmarshalText([]byte(cfg.LogLevel))
	opts := &slog.HandlerOptions{Level: lvl}
	if cfg.LogFormat == "json" {
		return slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}

// New builds the backend, seeds it and assembles both handlers. It does not
// listen.
func New(ctx context.Context, cfg config.Config, opts ...Option) (*Server, error) {
	s := &Server{cfg: cfg, version: "dev", done: make(chan struct{})}
	for _, o := range opts {
		o(s)
	}
	if s.log == nil {
		s.log = newLogger(cfg)
	}
	for _, w := range cfg.Warnings {
		s.log.Warn(w)
	}

	raw := s.backend
	if raw == nil {
		switch cfg.Storage {
		case config.StorageMemory:
			raw = memory.New()
		default:
			b, err := fs.New(cfg.DataDir)
			if err != nil {
				return nil, fmt.Errorf("open data dir %q: %w", cfg.DataDir, err)
			}
			raw = b
		}
	}
	s.backend = raw

	s.requests = reqlog.NewStore(cfg.RequestLogSize)
	s.faults = faults.NewEngine()
	if cfg.FaultsJSON != "" {
		if err := s.faults.LoadJSON([]byte(cfg.FaultsJSON)); err != nil {
			return nil, fmt.Errorf("S3LAB_FAULTS: %w", err)
		}
	}

	seeder := seed.New(raw, cfg.Buckets, cfg.SeedDir)
	if len(cfg.Buckets) > 0 || cfg.SeedDir != "" {
		if err := seeder.Apply(ctx); err != nil {
			return nil, fmt.Errorf("seed: %w", err)
		}
	}

	s.s3 = s.buildS3(faults.WrapBackend(raw, s.faults))

	if cfg.ConsoleEnabled {
		s.console = console.New(console.Deps{
			Backend:       raw,
			Requests:      s.requests,
			Faults:        s.faults,
			Seeder:        seeder,
			Logger:        s.log,
			AllowedHosts:  cfg.ConsoleAllowedHosts,
			MaxObjectSize: cfg.MaxObjectSize,
			Done:          s.shutdownDone(),
			Info: console.Info{
				Version:            s.version,
				Region:             cfg.Region,
				Storage:            string(cfg.Storage),
				S3Port:             portOf(cfg.Addr),
				ConsolePort:        portOf(cfg.ConsoleAddr),
				AccessKey:          cfg.AccessKey,
				VirtualHostDomain:  cfg.Domain,
				RequestLogCapacity: cfg.RequestLogSize,
				StartedAt:          time.Now(),
			},
		})
	}
	return s, nil
}

func portOf(addr string) int {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(p)
	return n
}

func (s *Server) buildS3(wrapped storage.Backend) http.Handler {
	cfg := s.cfg
	cors := s3api.CORSConfig{}
	if len(cfg.CORSOrigins) > 0 {
		cors = s3api.DefaultCORS()
		cors.AllowedOrigins = cfg.CORSOrigins
	}
	api := s3api.New(wrapped,
		s3api.WithRegion(cfg.Region),
		s3api.WithVirtualHostDomain(cfg.Domain),
		s3api.WithCORS(cors),
		s3api.WithLogger(s.log),
		s3api.WithMaxObjectSize(cfg.MaxObjectSize),
	)
	verifier := auth.NewVerifier(auth.StaticCredentials{cfg.AccessKey: cfg.SecretKey}, cfg.Region,
		auth.WithAnonymous(cfg.Anonymous))

	var h http.Handler = api
	h = auth.Middleware(verifier, s3err.Write)(h)
	// Inner recoverer so the request log records the 500 of a panicking handler.
	h = recoverer(s.log)(h)
	h = api.CORS(h)
	h = faults.Middleware(s.faults, s3err.Write)(h)
	if s.log.Enabled(context.Background(), slog.LevelDebug) {
		h = debugLog(s.log)(h)
	}
	h = reqlog.Middleware(s.requests)(h)
	h = s3op.Resolver{Domain: cfg.Domain}.Middleware(h)
	h = healthAliases(h)
	return recoverer(s.log)(h)
}

func recoverer(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(rec)
				}
				log.Error("panic serving request", "method", r.Method, "path", r.URL.Path,
					"panic", fmt.Sprint(rec), "stack", string(debug.Stack()))
				s3err.Write(w, r, s3err.InternalError)
			}()
			next.ServeHTTP(w, r)
		})
	}
}

func healthAliases(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			switch r.URL.Path {
			case "/minio/health/live", "/minio/health/ready":
				w.WriteHeader(http.StatusOK)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(p)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func debugLog(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w}
			defer func() {
				status := sw.status
				if status == 0 {
					status = http.StatusOK
				}
				attrs := []any{"method", r.Method, "status", status, "duration", time.Since(start).Round(time.Microsecond)}
				if info := s3op.FromContext(r.Context()); info != nil {
					attrs = append(attrs, "op", string(info.Op), "bucket", info.Bucket, "key", info.Key,
						"error", info.ErrorCode)
				}
				log.Debug("request", attrs...)
			}()
			next.ServeHTTP(sw, r)
		})
	}
}

// S3Handler is the full S3 chain: recover → health aliases → s3op resolver →
// reqlog → faults → CORS → recover → auth → s3api.
func (s *Server) S3Handler() http.Handler { return s.s3 }

// ConsoleHandler is the console UI + admin API (nil when disabled).
func (s *Server) ConsoleHandler() http.Handler { return s.console }

func (s *Server) Requests() *reqlog.Store { return s.requests }
func (s *Server) Faults() *faults.Engine  { return s.faults }

// Addrs returns the bound S3 and console addresses once Run has started
// listening; the console address is empty when disabled.
func (s *Server) Addrs() (s3, console string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.s3Addr, s.consoleAddr
}

// shutdownDone is closed when Run begins shutting down, so long-lived
// handlers (SSE) can return.
func (s *Server) shutdownDone() <-chan struct{} { return s.done }

func newHTTPServer(h http.Handler, base context.Context) *http.Server {
	return &http.Server{
		Handler:           h,
		BaseContext:       func(net.Listener) context.Context { return base },
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}

// Run listens on cfg.Addr and cfg.ConsoleAddr until ctx is canceled, then
// shuts down gracefully.
func (s *Server) Run(ctx context.Context) error {
	s3Ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen s3 %q: %w", s.cfg.Addr, err)
	}
	var consoleLn net.Listener
	if s.console != nil {
		consoleLn, err = net.Listen("tcp", s.cfg.ConsoleAddr)
		if err != nil {
			_ = s3Ln.Close()
			return fmt.Errorf("listen console %q: %w", s.cfg.ConsoleAddr, err)
		}
	}

	consoleAddr := "disabled"
	s.mu.Lock()
	s.s3Addr = s3Ln.Addr().String()
	if consoleLn != nil {
		s.consoleAddr = consoleLn.Addr().String()
		consoleAddr = s.consoleAddr
	}
	s3Addr := s.s3Addr
	s.mu.Unlock()

	baseCtx, baseCancel := context.WithCancel(context.WithoutCancel(ctx))
	defer baseCancel()
	servers := []*http.Server{newHTTPServer(s.s3, baseCtx)}
	lns := []net.Listener{s3Ln}
	if consoleLn != nil {
		servers = append(servers, newHTTPServer(s.console, baseCtx))
		lns = append(lns, consoleLn)
	}

	errCh := make(chan error, len(servers))
	for i, srv := range servers {
		go func() { errCh <- srv.Serve(lns[i]) }()
	}
	s.log.Info("listening", "s3", s3Addr, "console", consoleAddr, "storage", string(s.cfg.Storage), "version", s.version)

	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-errCh:
	}

	s.doneOnce.Do(func() { close(s.done) })
	baseCancel()
	shutCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
	defer cancel()
	var wg sync.WaitGroup
	for _, srv := range servers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := srv.Shutdown(shutCtx); err != nil {
				_ = srv.Close()
			}
		}()
	}
	wg.Wait()

	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	return nil
}
