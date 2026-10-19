package server

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeoMachado94/s3lab/internal/config"
	"github.com/LeoMachado94/s3lab/internal/faults"
	"github.com/LeoMachado94/s3lab/internal/reqlog"
	"github.com/LeoMachado94/s3lab/internal/storage"
)

func testConfig() config.Config {
	cfg := config.Default()
	cfg.Storage = config.StorageMemory
	cfg.Addr = "127.0.0.1:0"
	cfg.ConsoleAddr = "127.0.0.1:0"
	return cfg
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newServer(t *testing.T, cfg config.Config, opts ...Option) (*Server, *httptest.Server) {
	t.Helper()
	opts = append([]Option{WithLogger(quietLogger())}, opts...)
	s, err := New(t.Context(), cfg, opts...)
	require.NoError(t, err)
	ts := httptest.NewServer(s.S3Handler())
	t.Cleanup(ts.Close)
	return s, ts
}

func client(ts *httptest.Server, access, secret string) *s3.Client {
	return s3.New(s3.Options{
		BaseEndpoint: aws.String(ts.URL),
		Region:       "us-east-1",
		UsePathStyle: true,
		Credentials:  credentials.NewStaticCredentialsProvider(access, secret, ""),
		Retryer:      aws.NopRetryer{},
	})
}

func lastEntry(t *testing.T, store *reqlog.Store) reqlog.Entry {
	t.Helper()
	list := store.List(reqlog.Filter{Limit: 1})
	require.NotEmpty(t, list)
	return list[len(list)-1]
}

func TestSignedRoundTrip(t *testing.T) {
	cfg := testConfig()
	cfg.Buckets = []string{"dev"}
	s, ts := newServer(t, cfg)
	c := client(ts, cfg.AccessKey, cfg.SecretKey)
	ctx := t.Context()

	_, err := c.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String("dev"), Key: aws.String("a/b.txt"), Body: strings.NewReader("hello")})
	require.NoError(t, err)

	got, err := c.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("dev"), Key: aws.String("a/b.txt")})
	require.NoError(t, err)
	body, _ := io.ReadAll(got.Body)
	assert.Equal(t, "hello", string(body))

	list, err := c.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String("dev")})
	require.NoError(t, err)
	require.Len(t, list.Contents, 1)
	assert.Equal(t, "a/b.txt", aws.ToString(list.Contents[0].Key))

	e := lastEntry(t, s.Requests())
	assert.Equal(t, "ListObjectsV2", e.Op)
	assert.Equal(t, "header", e.Auth)
	assert.Equal(t, cfg.AccessKey, e.AccessKey)
}

func TestWrongSecret(t *testing.T) {
	cfg := testConfig()
	cfg.Buckets = []string{"dev"}
	s, ts := newServer(t, cfg)
	c := client(ts, cfg.AccessKey, "wrong")

	_, err := c.ListObjectsV2(t.Context(), &s3.ListObjectsV2Input{Bucket: aws.String("dev")})
	require.ErrorContains(t, err, "SignatureDoesNotMatch")

	e := lastEntry(t, s.Requests())
	assert.Equal(t, "failed", e.Auth)
	assert.Equal(t, "SignatureDoesNotMatch", e.ErrorCode)
	assert.Equal(t, http.StatusForbidden, e.Status)
}

func TestHealthAliasesBypassAuth(t *testing.T) {
	s, ts := newServer(t, testConfig())
	for _, p := range []string{"/minio/health/live", "/minio/health/ready"} {
		for _, m := range []string{http.MethodGet, http.MethodHead} {
			req, _ := http.NewRequestWithContext(t.Context(), m, ts.URL+p, http.NoBody)
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			b, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			assert.Equal(t, http.StatusOK, resp.StatusCode, p)
			assert.Empty(t, b)
		}
	}
	assert.Empty(t, s.Requests().List(reqlog.Filter{}))
}

func TestFaultRuleFailsPut(t *testing.T) {
	cfg := testConfig()
	cfg.Buckets = []string{"dev"}
	s, ts := newServer(t, cfg)
	rule, err := s.Faults().Add(faults.Rule{Op: "PutObject", Status: 503})
	require.NoError(t, err)

	c := client(ts, cfg.AccessKey, cfg.SecretKey)
	_, err = c.PutObject(t.Context(), &s3.PutObjectInput{Bucket: aws.String("dev"), Key: aws.String("k"), Body: bytes.NewReader([]byte("x"))})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "503")

	e := lastEntry(t, s.Requests())
	assert.Equal(t, rule.ID, e.FaultID)
	assert.Equal(t, http.StatusServiceUnavailable, e.Status)
}

func TestFaultsFromConfig(t *testing.T) {
	cfg := testConfig()
	cfg.FaultsJSON = `[{"op":"PutObject","status":500}]`
	s, _ := newServer(t, cfg)
	assert.Len(t, s.Faults().List(), 1)

	cfg.FaultsJSON = `[{"op":"PutObject"}]`
	_, err := New(t.Context(), cfg, WithLogger(quietLogger()))
	require.Error(t, err)
}

func TestCORSPreflightUnauthenticated(t *testing.T) {
	_, ts := newServer(t, testConfig())
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodOptions, ts.URL+"/dev/key", http.NoBody)
	req.Header.Set("Origin", "http://app.example")
	req.Header.Set("Access-Control-Request-Method", "PUT")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "*", resp.Header.Get("Access-Control-Allow-Origin"))
}

func TestCORSDisabled(t *testing.T) {
	cfg := testConfig()
	cfg.CORSOrigins = []string{}
	_, ts := newServer(t, cfg)
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodOptions, ts.URL+"/dev/key", http.NoBody)
	req.Header.Set("Origin", "http://app.example")
	req.Header.Set("Access-Control-Request-Method", "PUT")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Empty(t, resp.Header.Get("Access-Control-Allow-Origin"))
}

func TestConsole(t *testing.T) {
	cfg := testConfig()
	s, _ := newServer(t, cfg)
	require.NotNil(t, s.ConsoleHandler())
	rec := httptest.NewRecorder()
	s.ConsoleHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://localhost:9001/_s3lab/health", http.NoBody))
	assert.Equal(t, http.StatusOK, rec.Code)

	cfg.ConsoleEnabled = false
	s, _ = newServer(t, cfg)
	assert.Nil(t, s.ConsoleHandler())
}

func TestSeedAtStartup(t *testing.T) {
	cfg := testConfig()
	cfg.Buckets = []string{"one", "two"}
	s, _ := newServer(t, cfg)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://localhost:9001/_s3lab/buckets", http.NoBody)
	req.SetBasicAuth(cfg.AccessKey, cfg.SecretKey)
	s.ConsoleHandler().ServeHTTP(rec, req)
	assert.Contains(t, rec.Body.String(), `"one"`)
	assert.Contains(t, rec.Body.String(), `"two"`)

	cfg.Buckets = []string{"BAD"}
	_, err := New(t.Context(), cfg, WithLogger(quietLogger()))
	require.Error(t, err)
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type panicBackend struct{ storage.Backend }

func (panicBackend) ListBuckets(context.Context) ([]storage.Bucket, error) { panic("boom") }

func TestPanicRecovery(t *testing.T) {
	cfg := testConfig()
	cfg.Anonymous = true
	s, ts := newServer(t, cfg, WithBackend(panicBackend{}))
	resp, err := http.Get(ts.URL + "/") //nolint:noctx // test
	require.NoError(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	assert.Contains(t, string(b), "<Code>InternalError</Code>")
	entries := s.Requests().List(reqlog.Filter{})
	require.Len(t, entries, 1)
	assert.Equal(t, http.StatusInternalServerError, entries[0].Status)
	assert.Equal(t, "InternalError", entries[0].ErrorCode)
}

func TestDebugLogging(t *testing.T) {
	var buf bytes.Buffer
	cfg := testConfig()
	cfg.Buckets = []string{"dev"}
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	s, err := New(t.Context(), cfg, WithLogger(log))
	require.NoError(t, err)
	ts := httptest.NewServer(s.S3Handler())
	defer ts.Close()
	c := client(ts, cfg.AccessKey, cfg.SecretKey)
	_, err = c.HeadBucket(t.Context(), &s3.HeadBucketInput{Bucket: aws.String("dev")})
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "op=HeadBucket")
	assert.Contains(t, buf.String(), "status=200")
	assert.Contains(t, buf.String(), "requestId=")
}

func TestRunStartsAndStops(t *testing.T) {
	var buf syncBuffer
	cfg := testConfig()
	cfg.Buckets = []string{"dev"}
	s, err := New(t.Context(), cfg, WithLogger(slog.New(slog.NewTextHandler(&buf, nil))), WithVersion("1.2.3"))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	require.Eventually(t, func() bool { a, _ := s.Addrs(); return a != "" }, 3*time.Second, 10*time.Millisecond)
	s3Addr, consoleAddr := s.Addrs()
	assert.NotEqual(t, s3Addr, consoleAddr)

	resp, err := http.Get("http://" + consoleAddr + "/_s3lab/health") //nolint:noctx // test
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp, err = http.Get("http://" + s3Addr + "/minio/health/live") //nolint:noctx // test
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, buf.String(), "listening")
	assert.Contains(t, buf.String(), "version=1.2.3")

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
}

func TestRunPortInUse(t *testing.T) {
	s, _ := newServer(t, testConfig())
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _ = s.Run(ctx) }()
	require.Eventually(t, func() bool { a, _ := s.Addrs(); return a != "" }, 3*time.Second, 10*time.Millisecond)
	s3Addr, _ := s.Addrs()

	cfg := testConfig()
	cfg.Addr = s3Addr
	s2, _ := newServer(t, cfg)
	err := s2.Run(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "listen s3")
}

func TestRunConsoleDisabled(t *testing.T) {
	cfg := testConfig()
	cfg.ConsoleEnabled = false
	s, _ := newServer(t, cfg)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	require.Eventually(t, func() bool { a, _ := s.Addrs(); return a != "" }, 3*time.Second, 10*time.Millisecond)
	_, c := s.Addrs()
	assert.Empty(t, c)
	cancel()
	require.NoError(t, <-done)
}

func TestRunStopsLongRequests(t *testing.T) {
	cfg := testConfig()
	s, err := New(t.Context(), cfg, WithLogger(quietLogger()))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	require.Eventually(t, func() bool { a, _ := s.Addrs(); return a != "" }, 3*time.Second, 10*time.Millisecond)
	_, consoleAddr := s.Addrs()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+consoleAddr+"/_s3lab/requests/stream", http.NoBody)
	require.NoError(t, err)
	req.SetBasicAuth(cfg.AccessKey, cfg.SecretKey)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	start := time.Now()
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return with an open SSE request")
	}
	assert.Less(t, time.Since(start), 5*time.Second)
	select {
	case <-s.shutdownDone():
	default:
		t.Fatal("shutdownDone not closed")
	}
}
