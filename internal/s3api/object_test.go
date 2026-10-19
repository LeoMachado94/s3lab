package s3api

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/require"

	"github.com/LeoMachado94/s3lab/internal/auth"
	"github.com/LeoMachado94/s3lab/internal/s3api/s3err"
	"github.com/LeoMachado94/s3lab/internal/storage"
)

func objectMD5(data string) string {
	sum := md5.Sum([]byte(data))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// objectFailingReader yields data then fails with err.
type objectFailingReader struct {
	data string
	err  error
	done bool
}

func (f *objectFailingReader) Read(p []byte) (int, error) {
	if f.done {
		return 0, f.err
	}
	f.done = true
	return copy(p, f.data), nil
}

// objectDoRaw sends a request with an arbitrary body and explicit length.
func objectDoRaw(h http.Handler, method, target string, body io.Reader, length int64, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, body)
	r.ContentLength = length
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func objectExists(t *testing.T, b storage.Backend, bucket, key string) bool {
	t.Helper()
	_, err := b.HeadObject(t.Context(), bucket, key)
	if err == nil {
		return true
	}
	require.ErrorIs(t, err, storage.ErrNoSuchKey)
	return false
}

func objectReadAll(t *testing.T, b storage.Backend, bucket, key string) string {
	t.Helper()
	rc, _, err := b.GetObject(t.Context(), bucket, key, nil)
	require.NoError(t, err)
	defer rc.Close()
	data, err := io.ReadAll(rc)
	require.NoError(t, err)
	return string(data)
}

func TestPutObject(t *testing.T) {
	const bucket = "bkt"
	tests := []struct {
		name       string
		opts       []Option
		body       string
		length     int64 // -2 means len(body)
		headers    map[string]string
		wantStatus int
		wantCode   string
		wantStored bool
	}{
		{name: "ok", body: "hello", length: -2, wantStatus: 200, wantStored: true},
		{name: "empty body", body: "", length: 0, wantStatus: 200, wantStored: true},
		{name: "good md5", body: "hello", length: -2, headers: map[string]string{"Content-MD5": objectMD5("hello")}, wantStatus: 200, wantStored: true},
		{name: "bad md5", body: "hello", length: -2, headers: map[string]string{"Content-MD5": objectMD5("other")}, wantStatus: 400, wantCode: "BadDigest"},
		{name: "invalid md5 base64", body: "hello", length: -2, headers: map[string]string{"Content-MD5": "!!!"}, wantStatus: 400, wantCode: "InvalidDigest"},
		{name: "invalid md5 length", body: "hello", length: -2, headers: map[string]string{"Content-MD5": base64.StdEncoding.EncodeToString([]byte("short"))}, wantStatus: 400, wantCode: "InvalidDigest"},
		{name: "missing content length", body: "hello", length: -1, wantStatus: 411, wantCode: "MissingContentLength"},
		{name: "streaming without length ok", body: "hello", length: -1, headers: map[string]string{"x-amz-content-sha256": "STREAMING-AWS4-HMAC-SHA256-PAYLOAD"}, wantStatus: 200, wantStored: true},
		{name: "too large by content-length", opts: []Option{WithMaxObjectSize(10)}, body: strings.Repeat("a", 11), length: -2, wantStatus: 400, wantCode: "EntityTooLarge"},
		{name: "at limit", opts: []Option{WithMaxObjectSize(10)}, body: strings.Repeat("a", 10), length: -2, wantStatus: 200, wantStored: true},
		{name: "too large by decoded length", opts: []Option{WithMaxObjectSize(10)}, body: "x", length: -1, headers: map[string]string{"x-amz-decoded-content-length": "11"}, wantStatus: 400, wantCode: "EntityTooLarge"},
		{name: "too large while streaming", opts: []Option{WithMaxObjectSize(10)}, body: strings.Repeat("a", 20), length: -1, headers: map[string]string{"x-amz-content-sha256": "STREAMING-UNSIGNED-PAYLOAD-TRAILER"}, wantStatus: 400, wantCode: "EntityTooLarge"},
		{name: "incomplete body", body: "abc", length: 10, wantStatus: 400, wantCode: "IncompleteBody"},
		{name: "incomplete decoded body", body: "abc", length: -1, headers: map[string]string{"x-amz-decoded-content-length": "10"}, wantStatus: 400, wantCode: "IncompleteBody"},
		{name: "decoded length matches", body: "abc", length: -1, headers: map[string]string{"x-amz-decoded-content-length": "3"}, wantStatus: 200, wantStored: true},
		{name: "invalid decoded length", body: "abc", length: -1, headers: map[string]string{"x-amz-decoded-content-length": "zz"}, wantStatus: 400, wantCode: "InvalidArgument"},
		{name: "ignored headers", body: "hello", length: -2, headers: map[string]string{
			"x-amz-acl": "public-read", "x-amz-storage-class": "GLACIER", "x-amz-server-side-encryption": "AES256",
			"x-amz-checksum-crc32": "AAAAAA==", "x-amz-sdk-checksum-algorithm": "CRC32",
		}, wantStatus: 200, wantStored: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, b := newTestHandler(t, tc.opts...)
			mustCreateBucket(t, b, bucket)
			length := tc.length
			if length == -2 {
				length = int64(len(tc.body))
			}
			rec := objectDoRaw(h, http.MethodPut, "/bkt/key", io.NopCloser(strings.NewReader(tc.body)), length, tc.headers)
			requireS3Error(t, rec, tc.wantStatus, tc.wantCode)
			require.Equal(t, tc.wantStored, objectExists(t, b, bucket, "key"))
			if tc.wantStored {
				sum := md5.Sum([]byte(tc.body))
				require.Equal(t, `"`+hexString(sum[:])+`"`, rec.Header().Get("ETag"))
			}
		})
	}
}

// objectRequire is requireS3Error that also accepts success bodies.
func objectRequire(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if code == "" {
		require.Equal(t, status, rec.Code, rec.Body.String())
		return
	}
	requireS3Error(t, rec, status, code)
}

func hexString(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&0xf])
	}
	return string(out)
}

func TestPutObjectBadDigestKeepsPreviousObject(t *testing.T) {
	h, b := newTestHandler(t)
	mustCreateBucket(t, b, "bkt")
	mustPut(t, b, "bkt", "key", "old")
	rec := do(t, h, testReq{method: "PUT", target: "/bkt/key", body: strings.NewReader("new"), headers: map[string]string{"Content-MD5": objectMD5("x")}})
	requireS3Error(t, rec, 400, "BadDigest")
	require.Equal(t, "old", objectReadAll(t, b, "bkt", "key"))
}

func TestPutObjectNoSuchBucket(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := do(t, h, testReq{method: "PUT", target: "/nope/key", body: strings.NewReader("x")})
	requireS3Error(t, rec, 404, "NoSuchBucket")
}

func TestPutObjectBodyReaderErrors(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		status   int
		wantCode string
	}{
		{"s3err.Error", s3err.XAmzContentSHA256Mismatch, 400, "XAmzContentSHA256Mismatch"},
		{"auth coder", auth.ErrContentSHA256Mismatch, 400, "XAmzContentSHA256Mismatch"},
		{"signature", s3err.SignatureDoesNotMatch, 403, "SignatureDoesNotMatch"},
		{"unknown", io.ErrClosedPipe, 500, "InternalError"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, b := newTestHandler(t)
			mustCreateBucket(t, b, "bkt")
			body := &objectFailingReader{data: "abc", err: tc.err}
			rec := objectDoRaw(h, http.MethodPut, "/bkt/key", body, 10, nil)
			objectRequire(t, rec, tc.status, tc.wantCode)
			require.False(t, objectExists(t, b, "bkt", "key"))
		})
	}
}

func TestPutObjectMetadata(t *testing.T) {
	h, b := newTestHandler(t)
	mustCreateBucket(t, b, "bkt")
	rec := do(t, h, testReq{method: "PUT", target: "/bkt/key", body: strings.NewReader("hello"), headers: map[string]string{
		"Content-Type":     "text/plain",
		"Content-Encoding": "aws-chunked,gzip",
		"Cache-Control":    "no-cache",
		"X-Amz-Meta-Foo":   "Bar",
		"x-amz-meta-baz":   "qux",
	}})
	require.Equal(t, 200, rec.Code)
	info, err := b.HeadObject(t.Context(), "bkt", "key")
	require.NoError(t, err)
	require.Equal(t, "text/plain", info.Meta.ContentType)
	require.Equal(t, "gzip", info.Meta.ContentEncoding)
	require.Equal(t, "no-cache", info.Meta.CacheControl)
	require.Equal(t, map[string]string{"foo": "Bar", "baz": "qux"}, info.Meta.User)

	rec = do(t, h, testReq{method: "PUT", target: "/bkt/key2", body: strings.NewReader("x"), headers: map[string]string{"Content-Encoding": "aws-chunked"}})
	require.Equal(t, 200, rec.Code)
	info, err = b.HeadObject(t.Context(), "bkt", "key2")
	require.NoError(t, err)
	require.Empty(t, info.Meta.ContentEncoding)
}

func TestGetObject(t *testing.T) {
	h, b := newTestHandler(t)
	mustCreateBucket(t, b, "bkt")
	_, err := b.PutObject(t.Context(), "bkt", "key", strings.NewReader("0123456789"), storage.PutOptions{
		Size: 10,
		Meta: storage.ObjectMeta{ContentType: "text/plain", User: map[string]string{"foo": "Bar"}},
	})
	require.NoError(t, err)

	rec := do(t, h, testReq{method: "GET", target: "/bkt/key"})
	require.Equal(t, 200, rec.Code)
	require.Equal(t, "0123456789", rec.Body.String())
	require.Equal(t, "10", rec.Header().Get("Content-Length"))
	require.Equal(t, "text/plain", rec.Header().Get("Content-Type"))
	require.Equal(t, "bytes", rec.Header().Get("Accept-Ranges"))
	require.Equal(t, []string{"Bar"}, objectHeaderValues(rec, "x-amz-meta-foo"))
	require.NotEmpty(t, rec.Header().Get("Last-Modified"))

	rec = do(t, h, testReq{method: "GET", target: "/bkt/missing"})
	requireS3Error(t, rec, 404, "NoSuchKey")
	rec = do(t, h, testReq{method: "GET", target: "/nobkt/key"})
	requireS3Error(t, rec, 404, "NoSuchBucket")
}

func TestGetHeadRange(t *testing.T) {
	h, b := newTestHandler(t)
	mustCreateBucket(t, b, "bkt")
	mustPut(t, b, "bkt", "key", "0123456789")
	tests := []struct {
		name      string
		rng       string
		status    int
		body      string
		wantRange string
		wantCode  string
	}{
		{"closed", "bytes=2-4", 206, "234", "bytes 2-4/10", ""},
		{"open ended", "bytes=7-", 206, "789", "bytes 7-9/10", ""},
		{"suffix", "bytes=-3", 206, "789", "bytes 7-9/10", ""},
		{"suffix larger than object", "bytes=-100", 206, "0123456789", "bytes 0-9/10", ""},
		{"end past size", "bytes=5-100", 206, "56789", "bytes 5-9/10", ""},
		{"start past size", "bytes=10-", 416, "", "bytes */10", "InvalidRange"},
		{"malformed", "bytes=abc", 416, "", "bytes */10", "InvalidRange"},
		{"multi range", "bytes=0-1,3-4", 416, "", "bytes */10", "InvalidRange"},
		{"reversed", "bytes=5-2", 416, "", "bytes */10", "InvalidRange"},
	}
	for _, tc := range tests {
		for _, method := range []string{"GET", "HEAD"} {
			t.Run(method+" "+tc.name, func(t *testing.T) {
				rec := do(t, h, testReq{method: method, target: "/bkt/key", headers: map[string]string{"Range": tc.rng}})
				objectRequire(t, rec, tc.status, tc.wantCode)
				require.Equal(t, tc.wantRange, rec.Header().Get("Content-Range"))
				if tc.status == 206 {
					require.Equal(t, strconv.Itoa(len(tc.body)), rec.Header().Get("Content-Length"))
					if method == "GET" {
						require.Equal(t, tc.body, rec.Body.String())
					} else {
						require.Empty(t, rec.Body.String())
					}
				}
			})
		}
	}
}

func TestHeadObject(t *testing.T) {
	h, b := newTestHandler(t)
	mustCreateBucket(t, b, "bkt")
	obj := mustPut(t, b, "bkt", "key", "hello")
	rec := do(t, h, testReq{method: "HEAD", target: "/bkt/key"})
	require.Equal(t, 200, rec.Code)
	require.Equal(t, "5", rec.Header().Get("Content-Length"))
	require.Equal(t, `"`+obj.ETag+`"`, rec.Header().Get("ETag"))
	require.Empty(t, rec.Body.String())
	rec = do(t, h, testReq{method: "HEAD", target: "/bkt/missing"})
	require.Equal(t, 404, rec.Code)
	require.Empty(t, rec.Body.String())
}

func TestConditionalRequests(t *testing.T) {
	h, b := newTestHandler(t)
	mustCreateBucket(t, b, "bkt")
	obj := mustPut(t, b, "bkt", "key", "hello")
	etag := `"` + obj.ETag + `"`
	other := `"deadbeef"`
	past := obj.LastModified.Add(-time.Hour).UTC().Format(http.TimeFormat)
	future := obj.LastModified.Add(time.Hour).UTC().Format(http.TimeFormat)
	same := obj.LastModified.UTC().Format(http.TimeFormat)

	tests := []struct {
		name    string
		headers map[string]string
		status  int
	}{
		{"if-match hit", map[string]string{"If-Match": etag}, 200},
		{"if-match unquoted hit", map[string]string{"If-Match": obj.ETag}, 200},
		{"if-match star", map[string]string{"If-Match": "*"}, 200},
		{"if-match list hit", map[string]string{"If-Match": other + ", " + etag}, 200},
		{"if-match miss", map[string]string{"If-Match": other}, 412},
		{"if-unmodified-since pass", map[string]string{"If-Unmodified-Since": future}, 200},
		{"if-unmodified-since same", map[string]string{"If-Unmodified-Since": same}, 200},
		{"if-unmodified-since fail", map[string]string{"If-Unmodified-Since": past}, 412},
		{"if-match true overrides if-unmodified-since false", map[string]string{"If-Match": etag, "If-Unmodified-Since": past}, 200},
		{"if-match false with if-unmodified-since true", map[string]string{"If-Match": other, "If-Unmodified-Since": future}, 412},
		{"if-none-match hit", map[string]string{"If-None-Match": etag}, 304},
		{"if-none-match star", map[string]string{"If-None-Match": "*"}, 304},
		{"if-none-match list", map[string]string{"If-None-Match": other + "," + etag}, 304},
		{"if-none-match miss", map[string]string{"If-None-Match": other}, 200},
		{"if-modified-since not modified", map[string]string{"If-Modified-Since": same}, 304},
		{"if-modified-since future", map[string]string{"If-Modified-Since": future}, 200},
		{"if-modified-since modified", map[string]string{"If-Modified-Since": past}, 200},
		{"if-modified-since invalid date ignored", map[string]string{"If-Modified-Since": "garbage"}, 200},
		{"if-none-match false overrides if-modified-since true", map[string]string{"If-None-Match": other, "If-Modified-Since": past}, 200},
		{"if-none-match true with if-modified-since true", map[string]string{"If-None-Match": etag, "If-Modified-Since": past}, 304},
		{"if-none-match false with if-modified-since false", map[string]string{"If-None-Match": other, "If-Modified-Since": same}, 200},
	}
	for _, tc := range tests {
		for _, method := range []string{"GET", "HEAD"} {
			t.Run(method+" "+tc.name, func(t *testing.T) {
				rec := do(t, h, testReq{method: method, target: "/bkt/key", headers: tc.headers})
				require.Equal(t, tc.status, rec.Code, rec.Body.String())
				switch tc.status {
				case 304:
					require.Equal(t, etag, rec.Header().Get("ETag"))
					require.NotEmpty(t, rec.Header().Get("Last-Modified"))
					require.Empty(t, rec.Body.String())
				case 412:
					if method == "GET" {
						require.Contains(t, rec.Body.String(), "<Code>PreconditionFailed</Code>")
					}
				}
			})
		}
	}
}

func TestGetObjectResponseOverrides(t *testing.T) {
	h, b := newTestHandler(t)
	mustCreateBucket(t, b, "bkt")
	mustPut(t, b, "bkt", "key", "hello")
	target := "/bkt/key?response-content-type=text%2Fcsv&response-content-language=pt-BR" +
		"&response-expires=Thu%2C%2001%20Dec%202033%2016%3A00%3A00%20GMT&response-cache-control=no-store" +
		"&response-content-disposition=attachment%3B%20filename%3Dx.csv&response-content-encoding=identity"
	rec := do(t, h, testReq{method: "GET", target: target})
	require.Equal(t, 200, rec.Code)
	require.Equal(t, "text/csv", rec.Header().Get("Content-Type"))
	require.Equal(t, "pt-BR", rec.Header().Get("Content-Language"))
	require.Equal(t, "Thu, 01 Dec 2033 16:00:00 GMT", rec.Header().Get("Expires"))
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	require.Equal(t, "attachment; filename=x.csv", rec.Header().Get("Content-Disposition"))
	require.Equal(t, "identity", rec.Header().Get("Content-Encoding"))

	rec = do(t, h, testReq{method: "HEAD", target: target})
	require.NotEqual(t, "text/csv", rec.Header().Get("Content-Type"))
}

func TestUserMetadataRoundTrip(t *testing.T) {
	h, b := newTestHandler(t)
	mustCreateBucket(t, b, "bkt")
	rec := do(t, h, testReq{method: "PUT", target: "/bkt/key", body: strings.NewReader("x"), headers: map[string]string{
		"X-AMZ-META-Mixed-Case": "v1", "x-amz-meta-lower": "v2",
	}})
	require.Equal(t, 200, rec.Code)
	for _, method := range []string{"GET", "HEAD"} {
		rec = do(t, h, testReq{method: method, target: "/bkt/key"})
		require.Equal(t, []string{"v1"}, objectHeaderValues(rec, "x-amz-meta-mixed-case"))
		require.Equal(t, []string{"v2"}, objectHeaderValues(rec, "x-amz-meta-lower"))
	}
}

func TestDeleteObject(t *testing.T) {
	h, b := newTestHandler(t)
	mustCreateBucket(t, b, "bkt")
	mustPut(t, b, "bkt", "key", "x")
	rec := do(t, h, testReq{method: "DELETE", target: "/bkt/key"})
	require.Equal(t, 204, rec.Code)
	require.False(t, objectExists(t, b, "bkt", "key"))
	rec = do(t, h, testReq{method: "DELETE", target: "/bkt/key"})
	require.Equal(t, 204, rec.Code)
	rec = do(t, h, testReq{method: "DELETE", target: "/nobkt/key"})
	requireS3Error(t, rec, 404, "NoSuchBucket")
}

func TestCopyObject(t *testing.T) {
	past := time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)
	future := time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)
	nowSec := time.Now().UTC().Format(http.TimeFormat)
	tests := []struct {
		name     string
		target   string
		headers  func(etag string) map[string]string
		status   int
		wantCode string
		check    func(t *testing.T, dst storage.ObjectInfo)
	}{
		{
			name: "default COPY keeps metadata", target: "/bkt/dst",
			headers: func(string) map[string]string { return map[string]string{"x-amz-copy-source": "/bkt/src"} },
			status:  200,
			check: func(t *testing.T, dst storage.ObjectInfo) {
				require.Equal(t, "text/plain", dst.Meta.ContentType)
				require.Equal(t, map[string]string{"k": "v"}, dst.Meta.User)
			},
		},
		{
			name: "source without leading slash and url-encoded", target: "/bkt/dst",
			headers: func(string) map[string]string { return map[string]string{"x-amz-copy-source": "bkt/s%72c"} },
			status:  200,
		},
		{
			name: "explicit COPY ignores new headers", target: "/bkt/dst",
			headers: func(string) map[string]string {
				return map[string]string{"x-amz-copy-source": "/bkt/src", "x-amz-metadata-directive": "COPY", "Content-Type": "image/png"}
			},
			status: 200,
			check: func(t *testing.T, dst storage.ObjectInfo) {
				require.Equal(t, "text/plain", dst.Meta.ContentType)
			},
		},
		{
			name: "REPLACE uses request metadata", target: "/bkt/dst",
			headers: func(string) map[string]string {
				return map[string]string{"x-amz-copy-source": "/bkt/src", "x-amz-metadata-directive": "REPLACE", "Content-Type": "image/png", "x-amz-meta-new": "1"}
			},
			status: 200,
			check: func(t *testing.T, dst storage.ObjectInfo) {
				require.Equal(t, "image/png", dst.Meta.ContentType)
				require.Equal(t, map[string]string{"new": "1"}, dst.Meta.User)
			},
		},
		{
			name: "invalid directive", target: "/bkt/dst",
			headers: func(string) map[string]string {
				return map[string]string{"x-amz-copy-source": "/bkt/src", "x-amz-metadata-directive": "MERGE"}
			},
			status: 400, wantCode: "InvalidArgument",
		},
		{
			name: "self copy with COPY", target: "/bkt/src",
			headers: func(string) map[string]string { return map[string]string{"x-amz-copy-source": "/bkt/src"} },
			status:  400, wantCode: "InvalidRequest",
		},
		{
			name: "self copy with REPLACE", target: "/bkt/src",
			headers: func(string) map[string]string {
				return map[string]string{"x-amz-copy-source": "/bkt/src", "x-amz-metadata-directive": "REPLACE", "Content-Type": "a/b"}
			},
			status: 200,
			check: func(t *testing.T, dst storage.ObjectInfo) {
				require.Equal(t, "a/b", dst.Meta.ContentType)
			},
		},
		{
			name: "missing source key", target: "/bkt/dst",
			headers: func(string) map[string]string { return map[string]string{"x-amz-copy-source": "/bkt/nope"} },
			status:  404, wantCode: "NoSuchKey",
		},
		{
			name: "missing source bucket", target: "/bkt/dst",
			headers: func(string) map[string]string { return map[string]string{"x-amz-copy-source": "/nobkt/src"} },
			status:  404, wantCode: "NoSuchBucket",
		},
		{
			name: "missing destination bucket", target: "/nobkt/dst",
			headers: func(string) map[string]string { return map[string]string{"x-amz-copy-source": "/bkt/src"} },
			status:  404, wantCode: "NoSuchBucket",
		},
		{
			name: "malformed source", target: "/bkt/dst",
			headers: func(string) map[string]string { return map[string]string{"x-amz-copy-source": "onlybucket"} },
			status:  400, wantCode: "InvalidArgument",
		},
		{
			name: "if-match ok", target: "/bkt/dst",
			headers: func(e string) map[string]string {
				return map[string]string{"x-amz-copy-source": "/bkt/src", "x-amz-copy-source-if-match": `"` + e + `"`}
			},
			status: 200,
		},
		{
			name: "if-match fails", target: "/bkt/dst",
			headers: func(string) map[string]string {
				return map[string]string{"x-amz-copy-source": "/bkt/src", "x-amz-copy-source-if-match": `"nope"`}
			},
			status: 412, wantCode: "PreconditionFailed",
		},
		{
			name: "if-none-match matches", target: "/bkt/dst",
			headers: func(e string) map[string]string {
				return map[string]string{"x-amz-copy-source": "/bkt/src", "x-amz-copy-source-if-none-match": `"` + e + `"`}
			},
			status: 412, wantCode: "PreconditionFailed",
		},
		{
			name: "if-unmodified-since fails", target: "/bkt/dst",
			headers: func(string) map[string]string {
				return map[string]string{"x-amz-copy-source": "/bkt/src", "x-amz-copy-source-if-unmodified-since": past}
			},
			status: 412, wantCode: "PreconditionFailed",
		},
		{
			name: "if-unmodified-since ok", target: "/bkt/dst",
			headers: func(string) map[string]string {
				return map[string]string{"x-amz-copy-source": "/bkt/src", "x-amz-copy-source-if-unmodified-since": future}
			},
			status: 200,
		},
		{
			name: "if-modified-since fails", target: "/bkt/dst",
			headers: func(string) map[string]string {
				return map[string]string{"x-amz-copy-source": "/bkt/src", "x-amz-copy-source-if-modified-since": nowSec}
			},
			status: 412, wantCode: "PreconditionFailed",
		},
		{
			name: "if-modified-since ok", target: "/bkt/dst",
			headers: func(string) map[string]string {
				return map[string]string{"x-amz-copy-source": "/bkt/src", "x-amz-copy-source-if-modified-since": past}
			},
			status: 200,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, b := newTestHandler(t)
			mustCreateBucket(t, b, "bkt")
			src, err := b.PutObject(t.Context(), "bkt", "src", strings.NewReader("payload"), storage.PutOptions{
				Size: 7, Meta: storage.ObjectMeta{ContentType: "text/plain", User: map[string]string{"k": "v"}},
			})
			require.NoError(t, err)
			rec := do(t, h, testReq{method: "PUT", target: tc.target, headers: tc.headers(src.ETag)})
			objectRequire(t, rec, tc.status, tc.wantCode)
			if tc.status != 200 {
				if strings.HasPrefix(tc.target, "/bkt/dst") {
					require.False(t, objectExists(t, b, "bkt", "dst"))
				}
				return
			}
			require.Contains(t, rec.Body.String(), "<CopyObjectResult")
			require.Contains(t, rec.Body.String(), "<ETag>&#34;"+src.ETag+"&#34;</ETag>")
			require.Regexp(t, `<LastModified>\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z</LastModified>`, rec.Body.String())
			dstKey := strings.TrimPrefix(tc.target, "/bkt/")
			dst, err := b.HeadObject(t.Context(), "bkt", dstKey)
			require.NoError(t, err)
			require.Equal(t, int64(7), dst.Size)
			if tc.check != nil {
				tc.check(t, dst)
			}
		})
	}
}

func TestObjectACL(t *testing.T) {
	h, b := newTestHandler(t)
	mustCreateBucket(t, b, "bkt")
	mustPut(t, b, "bkt", "key", "x")

	rec := do(t, h, testReq{method: "GET", target: "/bkt/key?acl"})
	require.Equal(t, 200, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, "<AccessControlPolicy")
	require.Contains(t, body, `xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"`)
	require.Contains(t, body, `xsi:type="CanonicalUser"`)
	require.Contains(t, body, "<Permission>FULL_CONTROL</Permission>")
	require.Contains(t, body, "<ID>s3lab</ID>")

	rec = do(t, h, testReq{method: "GET", target: "/bkt/missing?acl"})
	requireS3Error(t, rec, 404, "NoSuchKey")

	require.NotContains(t, body, "AllUsers", "private by default")

	rec = do(t, h, testReq{method: "PUT", target: "/bkt/key?acl", body: strings.NewReader("<AccessControlPolicy/>"), headers: map[string]string{"x-amz-acl": "public-read"}})
	require.Equal(t, 200, rec.Code)
	got, err := b.HeadObject(t.Context(), "bkt", "key")
	require.NoError(t, err)
	require.Equal(t, "public-read", got.Meta.ACL)
	rec = do(t, h, testReq{method: "GET", target: "/bkt/key?acl"})
	require.Contains(t, rec.Body.String(), "<URI>http://acs.amazonaws.com/groups/global/AllUsers</URI>")
	require.Contains(t, rec.Body.String(), "<Permission>READ</Permission>")

	private := `<AccessControlPolicy><AccessControlList><Grant><Grantee><ID>s3lab</ID></Grantee><Permission>FULL_CONTROL</Permission></Grant></AccessControlList></AccessControlPolicy>`
	rec = do(t, h, testReq{method: "PUT", target: "/bkt/key?acl", body: strings.NewReader(private)})
	require.Equal(t, 200, rec.Code)
	got, err = b.HeadObject(t.Context(), "bkt", "key")
	require.NoError(t, err)
	require.Empty(t, got.Meta.ACL, "body without public grants means private")

	public := `<AccessControlPolicy><AccessControlList><Grant><Grantee><URI>http://acs.amazonaws.com/groups/global/AllUsers</URI></Grantee><Permission>READ</Permission></Grant></AccessControlList></AccessControlPolicy>`
	rec = do(t, h, testReq{method: "PUT", target: "/bkt/key?acl", body: strings.NewReader(public)})
	require.Equal(t, 200, rec.Code)
	got, err = b.HeadObject(t.Context(), "bkt", "key")
	require.NoError(t, err)
	require.Equal(t, "public-read", got.Meta.ACL)
	data, _, err := b.GetObject(t.Context(), "bkt", "key", nil)
	require.NoError(t, err)
	content, _ := io.ReadAll(data)
	_ = data.Close()
	require.Equal(t, "x", string(content), "acl change keeps the body")

	rec = do(t, h, testReq{method: "PUT", target: "/bkt/key?acl", body: strings.NewReader("<not-xml")})
	requireS3Error(t, rec, 400, "MalformedXML")

	rec = do(t, h, testReq{method: "PUT", target: "/bkt/pub", body: strings.NewReader("p"), headers: map[string]string{"x-amz-acl": "public-read"}})
	require.Equal(t, 200, rec.Code)
	got, err = b.HeadObject(t.Context(), "bkt", "pub")
	require.NoError(t, err)
	require.Equal(t, "public-read", got.Meta.ACL, "x-amz-acl on PutObject is stored")

	rec = do(t, h, testReq{method: "PUT", target: "/bkt/missing?acl", body: strings.NewReader("x")})
	requireS3Error(t, rec, 404, "NoSuchKey")
}

func TestObjectSDK(t *testing.T) {
	h, b := newTestHandler(t)
	mustCreateBucket(t, b, "bkt")
	srv := httptest.NewServer(h)
	defer srv.Close()

	client := s3.New(s3.Options{
		Region:                     "us-east-1",
		BaseEndpoint:               aws.String(srv.URL),
		UsePathStyle:               true,
		Credentials:                aws.AnonymousCredentials{},
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	put, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String("bkt"), Key: aws.String("dir/a.txt"),
		Body:        strings.NewReader("0123456789"),
		ContentType: aws.String("text/plain"),
		Metadata:    map[string]string{"Owner": "leo"},
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(put.ETag))

	get, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("bkt"), Key: aws.String("dir/a.txt"), Range: aws.String("bytes=2-5")})
	require.NoError(t, err)
	data, err := io.ReadAll(get.Body)
	require.NoError(t, err)
	require.NoError(t, get.Body.Close())
	require.Equal(t, "2345", string(data))
	require.Equal(t, "bytes 2-5/10", aws.ToString(get.ContentRange))
	require.Equal(t, map[string]string{"owner": "leo"}, get.Metadata)

	head, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String("bkt"), Key: aws.String("dir/a.txt")})
	require.NoError(t, err)
	require.Equal(t, int64(10), aws.ToInt64(head.ContentLength))
	require.Equal(t, "text/plain", aws.ToString(head.ContentType))
	require.Equal(t, aws.ToString(put.ETag), aws.ToString(head.ETag))

	cp, err := client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket: aws.String("bkt"), Key: aws.String("copy b.txt"), CopySource: aws.String("bkt/dir/a.txt"),
		MetadataDirective: s3types.MetadataDirectiveReplace, ContentType: aws.String("text/csv"),
	})
	require.NoError(t, err)
	require.Equal(t, aws.ToString(put.ETag), aws.ToString(cp.CopyObjectResult.ETag))
	require.Equal(t, "0123456789", objectReadAll(t, b, "bkt", "copy b.txt"))
	copied, err := b.HeadObject(ctx, "bkt", "copy b.txt")
	require.NoError(t, err)
	require.Equal(t, "text/csv", copied.Meta.ContentType)

	acl, err := client.GetObjectAcl(ctx, &s3.GetObjectAclInput{Bucket: aws.String("bkt"), Key: aws.String("dir/a.txt")})
	require.NoError(t, err)
	require.Len(t, acl.Grants, 1)
	require.Equal(t, s3types.PermissionFullControl, acl.Grants[0].Permission)
	require.Equal(t, s3types.TypeCanonicalUser, acl.Grants[0].Grantee.Type)

	_, err = client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String("bkt"), Key: aws.String("dir/a.txt")})
	require.NoError(t, err)
	_, err = client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String("bkt"), Key: aws.String("dir/a.txt")})
	require.Error(t, err)
	_, err = client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("bkt"), Key: aws.String("dir/a.txt")})
	var nsk *s3types.NoSuchKey
	require.ErrorAs(t, err, &nsk)
}

// objectHeaderValues reads headers set with non-canonical (lowercase) keys.
func objectHeaderValues(rec *httptest.ResponseRecorder, name string) []string {
	for k, v := range rec.Header() {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return nil
}
