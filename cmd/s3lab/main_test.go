package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func env(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func portOf(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	_, p, err := net.SplitHostPort(ts.Listener.Addr().String())
	require.NoError(t, err)
	return p
}

func TestVersion(t *testing.T) {
	for _, arg := range []string{"version", "--version"} {
		var out, errOut bytes.Buffer
		code := run(t.Context(), []string{arg}, env(nil), &out, &errOut)
		assert.Equal(t, 0, code)
		assert.Equal(t, version+"\n", out.String())
	}
}

func TestHealthcheck(t *testing.T) {
	var gotPath string
	status := http.StatusOK
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(status)
	}))
	defer ts.Close()
	port := portOf(t, ts)

	tests := []struct {
		name   string
		env    map[string]string
		status int
		want   int
		path   string
	}{
		{"console ok", map[string]string{"S3LAB_CONSOLE_ADDR": ":" + port}, 200, 0, "/_s3lab/health"},
		{"console unhealthy", map[string]string{"S3LAB_CONSOLE_ADDR": "127.0.0.1:" + port}, 503, 1, "/_s3lab/health"},
		{"console disabled uses s3", map[string]string{"S3LAB_CONSOLE_ENABLED": "false", "S3LAB_ADDR": ":" + port}, 200, 0, "/minio/health/live"},
		{"invalid config", map[string]string{"S3LAB_STORAGE": "bogus"}, 200, 1, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotPath, status = "", tt.status
			var out, errOut bytes.Buffer
			code := run(t.Context(), []string{"healthcheck"}, env(tt.env), &out, &errOut)
			assert.Equal(t, tt.want, code)
			assert.Equal(t, tt.path, gotPath)
			if tt.want != 0 {
				assert.NotEmpty(t, errOut.String())
			}
		})
	}
}

func TestHealthcheckConnectionRefused(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	_, p, _ := net.SplitHostPort(l.Addr().String())
	_ = l.Close()
	var out, errOut bytes.Buffer
	code := run(t.Context(), []string{"healthcheck"}, env(map[string]string{"S3LAB_CONSOLE_ADDR": ":" + p}), &out, &errOut)
	assert.Equal(t, 1, code)
	assert.NotEmpty(t, errOut.String())
}

func TestServeConfigError(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run(t.Context(), []string{"--storage=nope"}, env(nil), &out, &errOut)
	assert.Equal(t, 2, code)
	assert.Contains(t, errOut.String(), "storage")
}

func TestServeStartsAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan int, 1)
	var out, errOut bytes.Buffer
	go func() {
		done <- run(ctx, []string{"serve", "--addr=127.0.0.1:0", "--console-addr=127.0.0.1:0", "--storage=memory"}, env(nil), &out, &errOut)
	}()
	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case code := <-done:
		assert.Equal(t, 0, code, errOut.String())
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not stop")
	}
}

func TestServeMinioStyleArgs(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan int, 1)
	var out, errOut bytes.Buffer
	go func() {
		done <- run(ctx, []string{"server", t.TempDir(), "--address", "127.0.0.1:0", "--console-address", "127.0.0.1:0"}, env(nil), &out, &errOut)
	}()
	time.Sleep(300 * time.Millisecond)
	cancel()
	assert.Equal(t, 0, <-done, errOut.String())
}

func TestHealthcheckIPv6Fallback(t *testing.T) {
	l, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skip("ipv6 loopback unavailable")
	}
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	_ = ts.Listener.Close()
	ts.Listener = l
	ts.Start()
	defer ts.Close()
	_, p, _ := net.SplitHostPort(l.Addr().String())
	var out, errOut bytes.Buffer
	code := run(t.Context(), []string{"healthcheck"}, env(map[string]string{"S3LAB_CONSOLE_ADDR": ":" + p}), &out, &errOut)
	assert.Equal(t, 0, code, errOut.String())
}
