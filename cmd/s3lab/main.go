// Command s3lab is an S3-compatible server for development and testing.
package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/LeoMachado94/s3lab/internal/config"
	"github.com/LeoMachado94/s3lab/internal/server"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.LookupEnv, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, lookup func(string) (string, bool), stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "version", "--version", "-version":
			_, _ = fmt.Fprintln(stdout, version)
			return 0
		case "healthcheck":
			return healthcheck(ctx, lookup, stderr)
		case "serve":
			args = args[1:]
		}
	}
	return serve(ctx, args, lookup, stderr)
}

func serve(ctx context.Context, args []string, lookup func(string) (string, bool), stderr io.Writer) int {
	cfg, err := config.LoadWithLookup(args, lookup)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "s3lab: %v\n", err)
		return 2
	}
	srv, err := server.New(ctx, cfg, server.WithVersion(version))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "s3lab: %v\n", err)
		return 1
	}
	if err := srv.Run(ctx); err != nil {
		_, _ = fmt.Fprintf(stderr, "s3lab: %v\n", err)
		return 1
	}
	return 0
}

func healthcheck(ctx context.Context, lookup func(string) (string, bool), stderr io.Writer) int {
	cfg, err := config.LoadWithLookup(nil, lookup)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "healthcheck: %v\n", err)
		return 1
	}
	addr, path := cfg.ConsoleAddr, "/_s3lab/health"
	if !cfg.ConsoleEnabled {
		addr, path = cfg.Addr, "/minio/health/live"
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "healthcheck: invalid address %q: %v\n", addr, err)
		return 1
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	url := "http://" + net.JoinHostPort("127.0.0.1", port) + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "healthcheck: %v\n", err)
		return 1
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "healthcheck: %v\n", err)
		return 1
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = fmt.Fprintf(stderr, "healthcheck: %s returned %d\n", url, resp.StatusCode)
		return 1
	}
	return 0
}
