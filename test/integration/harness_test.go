//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/stretchr/testify/require"

	"github.com/LeoMachado94/s3lab/internal/config"
	"github.com/LeoMachado94/s3lab/internal/server"
)

type harness struct {
	t        *testing.T
	cfg      config.Config
	s3URL    string
	adminURL string
	s3       *s3.Client
}

type entry struct {
	ID        int64  `json:"id"`
	Method    string `json:"method"`
	Path      string `json:"path"`
	Query     string `json:"query"`
	Op        string `json:"op"`
	Bucket    string `json:"bucket"`
	Key       string `json:"key"`
	Status    int    `json:"status"`
	ErrorCode string `json:"errorCode"`
	Auth      string `json:"auth"`
	AccessKey string `json:"accessKey"`
	FaultID   string `json:"faultId"`
	BytesIn   int64  `json:"bytesIn"`
}

type faultRule struct {
	ID   string `json:"id"`
	Hits int    `json:"hits"`
}

var backends = []config.Storage{config.StorageFS, config.StorageMemory}

// forEachBackend runs fn as parallel subtests named "fs" and "memory".
func forEachBackend(t *testing.T, fn func(t *testing.T, storage config.Storage)) {
	t.Helper()
	for _, b := range backends {
		t.Run(string(b), func(t *testing.T) {
			t.Parallel()
			fn(t, b)
		})
	}
}

func startServer(t *testing.T, storage config.Storage, mutate func(*config.Config)) *harness {
	t.Helper()
	cfg := config.Default()
	cfg.AccessKey = testAccessKey
	cfg.SecretKey = testSecretKey
	cfg.Storage = storage
	cfg.DataDir = t.TempDir()
	if mutate != nil {
		mutate(&cfg)
	}
	srv, err := server.New(t.Context(), cfg, server.WithVersion("test"))
	require.NoError(t, err)

	s3srv := httptest.NewServer(srv.S3Handler())
	t.Cleanup(s3srv.Close)
	console := httptest.NewServer(srv.ConsoleHandler())
	t.Cleanup(console.Close)

	return &harness{
		t:        t,
		cfg:      cfg,
		s3URL:    s3srv.URL,
		adminURL: console.URL,
		s3:       newClient(t, s3srv.URL),
	}
}

func (h *harness) adminJSON(method, path string, body any, out any) int {
	h.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		require.NoError(h.t, err)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(h.t.Context(), method, h.adminURL+path, rd)
	require.NoError(h.t, err)
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(h.t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(h.t, err)
	if out != nil && len(raw) > 0 {
		require.NoError(h.t, json.Unmarshal(raw, out), string(raw))
	}
	return resp.StatusCode
}

// requests returns the request log; filter is a raw query string.
func (h *harness) requests(filter string) []entry {
	h.t.Helper()
	path := "/_s3lab/requests"
	if filter != "" {
		path += "?" + filter
	}
	var out struct {
		Entries []entry `json:"entries"`
	}
	require.Equal(h.t, http.StatusOK, h.adminJSON(http.MethodGet, path, nil, &out))
	return out.Entries
}

// waitRequests polls until at least n entries match, since the log entry may
// be written just after the client sees the response.
func (h *harness) waitRequests(filter string, n int) []entry {
	h.t.Helper()
	var got []entry
	require.Eventually(h.t, func() bool {
		got = h.requests(filter)
		return len(got) >= n
	}, 3*time.Second, 20*time.Millisecond, "request log %q never reached %d entries", filter, n)
	return got
}

func (h *harness) addFault(rule string) string {
	h.t.Helper()
	var out faultRule
	require.Equal(h.t, http.StatusCreated, h.adminJSON(http.MethodPost, "/_s3lab/faults", rule, &out))
	require.NotEmpty(h.t, out.ID)
	return out.ID
}

func (h *harness) faultRules() []faultRule {
	h.t.Helper()
	var out struct {
		Rules []faultRule `json:"rules"`
	}
	require.Equal(h.t, http.StatusOK, h.adminJSON(http.MethodGet, "/_s3lab/faults", nil, &out))
	return out.Rules
}

func (h *harness) reset(query string) {
	h.t.Helper()
	path := "/_s3lab/reset"
	if query != "" {
		path += "?" + query
	}
	require.Equal(h.t, http.StatusNoContent, h.adminJSON(http.MethodPost, path, nil, nil))
}

func (h *harness) createBucket(name string) {
	h.t.Helper()
	_, err := h.s3.CreateBucket(h.t.Context(), &s3.CreateBucketInput{Bucket: &name})
	require.NoError(h.t, err)
}

func (h *harness) putBytes(bucket, key string, data []byte) {
	h.t.Helper()
	_, err := h.s3.PutObject(h.t.Context(), &s3.PutObjectInput{Bucket: &bucket, Key: &key, Body: bytes.NewReader(data)})
	require.NoError(h.t, err)
}

func (h *harness) getBytes(bucket, key string) []byte {
	h.t.Helper()
	out, err := h.s3.GetObject(h.t.Context(), &s3.GetObjectInput{Bucket: &bucket, Key: &key})
	require.NoError(h.t, err)
	defer out.Body.Close()
	data, err := io.ReadAll(out.Body)
	require.NoError(h.t, err)
	return data
}

func (h *harness) listKeys(bucket, prefix string) []string {
	h.t.Helper()
	var keys []string
	p := s3.NewListObjectsV2Paginator(h.s3, &s3.ListObjectsV2Input{Bucket: &bucket, Prefix: &prefix})
	for p.HasMorePages() {
		page, err := p.NextPage(h.t.Context())
		require.NoError(h.t, err)
		for _, o := range page.Contents {
			keys = append(keys, *o.Key)
		}
	}
	return keys
}

func ops(entries []entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Op)
	}
	return out
}

func countOp(entries []entry, op string) int {
	n := 0
	for _, e := range entries {
		if e.Op == op {
			n++
		}
	}
	return n
}

func apiCode(err error) string {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		return ae.ErrorCode()
	}
	return ""
}

func httpStatus(err error) int {
	var re *smithyhttp.ResponseError
	if errors.As(err, &re) {
		return re.HTTPStatusCode()
	}
	return 0
}

func requireAPIError(t *testing.T, err error, status int, code string) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, status, httpStatus(err), err.Error())
	if code != "" {
		require.Equal(t, code, apiCode(err), err.Error())
	}
}

// genReader is a deterministic, seekable pseudo-random byte stream.
type genReader struct {
	seed uint64
	size int64
	pos  int64
}

func newGenReader(seed uint64, size int64) *genReader { return &genReader{seed: seed, size: size} }

func (g *genReader) Read(p []byte) (int, error) {
	if g.pos >= g.size {
		return 0, io.EOF
	}
	n := 0
	var block [8]byte
	for n < len(p) && g.pos < g.size {
		binary.LittleEndian.PutUint64(block[:], splitmix(g.seed+uint64(g.pos/8)))
		c := copy(p[n:], block[int(g.pos%8):])
		if remain := g.size - g.pos; int64(c) > remain {
			c = int(remain)
		}
		n += c
		g.pos += int64(c)
	}
	return n, nil
}

func (g *genReader) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		offset += g.pos
	case io.SeekEnd:
		offset += g.size
	default:
		return 0, fmt.Errorf("bad whence %d", whence)
	}
	if offset < 0 {
		return 0, errors.New("negative position")
	}
	g.pos = offset
	return offset, nil
}

func splitmix(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

func genBytes(seed uint64, size int) []byte {
	b := make([]byte, size)
	_, _ = io.ReadFull(newGenReader(seed, int64(size)), b)
	return b
}

func sha256Of(r io.Reader) ([32]byte, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return [32]byte{}, err
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum, nil
}

func plainDo(t *testing.T, method, rawURL string, body io.Reader, hdr http.Header) (*http.Response, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	require.NoError(t, err)
	for k, v := range hdr {
		req.Header[k] = v
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, data
}

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }
