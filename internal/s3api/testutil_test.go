package s3api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeoMachado94/s3lab/internal/storage"
	"github.com/LeoMachado94/s3lab/internal/storage/memory"
)

// Shared test helpers. Group-specific helpers go in the group's own _test.go
// file, prefixed with the group name.

func newTestHandler(t *testing.T, opts ...Option) (*Handler, storage.Backend) {
	t.Helper()
	b := memory.New()
	return New(b, opts...), b
}

type testReq struct {
	method  string
	target  string
	body    io.Reader
	headers map[string]string
}

func do(t *testing.T, h http.Handler, req testReq) *httptest.ResponseRecorder {
	t.Helper()
	body := req.body
	if body == nil {
		body = http.NoBody
	}
	r := httptest.NewRequest(req.method, req.target, body)
	for k, v := range req.headers {
		r.Header.Set(k, v)
	}
	if s, ok := req.body.(*strings.Reader); ok {
		r.ContentLength = s.Size()
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// requireS3Error asserts an S3 XML error response with the given code.
func requireS3Error(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	require.Equal(t, status, rec.Code, rec.Body.String())
	if rec.Body.Len() > 0 {
		require.Contains(t, rec.Body.String(), "<Code>"+code+"</Code>")
	}
}

func mustCreateBucket(t *testing.T, b storage.Backend, name string) {
	t.Helper()
	require.NoError(t, b.CreateBucket(t.Context(), name))
}

func mustPut(t *testing.T, b storage.Backend, bucket, key, data string) storage.ObjectInfo {
	t.Helper()
	info, err := b.PutObject(t.Context(), bucket, key, strings.NewReader(data), storage.PutOptions{Size: int64(len(data))})
	require.NoError(t, err)
	return info
}
