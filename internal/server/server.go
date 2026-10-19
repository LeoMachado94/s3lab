// Package server wires storage, auth, middlewares, the S3 API and the console
// into two HTTP servers. Owner: task T3.A.
package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/LeoMachado94/s3lab/internal/config"
	"github.com/LeoMachado94/s3lab/internal/faults"
	"github.com/LeoMachado94/s3lab/internal/reqlog"
	"github.com/LeoMachado94/s3lab/internal/storage"
)

type Server struct{}

type Option func(*Server)

// WithLogger sets the logger (default: built from cfg.LogLevel/LogFormat).
func WithLogger(l *slog.Logger) Option { return func(*Server) {} }

// WithBackend overrides the backend chosen by cfg.Storage (tests).
func WithBackend(b storage.Backend) Option { return func(*Server) {} }

// WithVersion sets the version reported by the console.
func WithVersion(v string) Option { return func(*Server) {} }

// New builds the backend, seeds it and assembles both handlers. It does not
// listen.
func New(ctx context.Context, cfg config.Config, opts ...Option) (*Server, error) {
	return nil, errors.New("server: not implemented")
}

// S3Handler is the full S3 chain: recover → health aliases → s3op resolver →
// reqlog → faults → CORS → auth → s3api.
func (s *Server) S3Handler() http.Handler { return http.NotFoundHandler() }

// ConsoleHandler is the console UI + admin API (nil when disabled).
func (s *Server) ConsoleHandler() http.Handler { return nil }

func (s *Server) Requests() *reqlog.Store { return nil }
func (s *Server) Faults() *faults.Engine  { return nil }

// Run listens on cfg.Addr and cfg.ConsoleAddr until ctx is canceled, then
// shuts down gracefully.
func (s *Server) Run(ctx context.Context) error { return errors.New("server: not implemented") }
