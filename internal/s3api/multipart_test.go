package s3api

import (
	"bytes"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/LeoMachado94/s3lab/internal/storage"
)

const mpMiB = 1 << 20

func mpBigPart() []byte { return bytes.Repeat([]byte("a"), 5*mpMiB) }

func mpMD5Hex(b []byte) string {
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}

func mpMD5B64(b []byte) string {
	sum := md5.Sum(b)
	return base64.StdEncoding.EncodeToString(sum[:])
}

func mpCreate(t *testing.T, h http.Handler, bucket, key string) string {
	t.Helper()
	rec := do(t, h, testReq{method: http.MethodPost, target: "/" + bucket + "/" + key + "?uploads"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var res InitiateMultipartUploadResult
	require.NoError(t, xml.Unmarshal(rec.Body.Bytes(), &res))
	require.Equal(t, bucket, res.Bucket)
	require.Equal(t, key, res.Key)
	require.NotEmpty(t, res.UploadID)
	return res.UploadID
}

func mpPut(t *testing.T, h http.Handler, bucket, key, uploadID string, n int, data []byte) string {
	t.Helper()
	rec := do(t, h, mpPutReq(bucket, key, uploadID, n, data, nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	etag := rec.Header().Get("ETag")
	require.Equal(t, `"`+mpMD5Hex(data)+`"`, etag)
	return etag
}

func mpPutReq(bucket, key, uploadID string, n int, data []byte, headers map[string]string) testReq {
	return testReq{
		method:  http.MethodPut,
		target:  fmt.Sprintf("/%s/%s?partNumber=%d&uploadId=%s", bucket, key, n, uploadID),
		body:    strings.NewReader(string(data)),
		headers: headers,
	}
}

func mpCompleteBody(parts ...CompletePart) *strings.Reader {
	out, _ := xml.Marshal(CompleteMultipartUpload{Parts: parts})
	return strings.NewReader(string(out))
}

func mpComplete(h http.Handler, bucket, key, uploadID string, body *strings.Reader) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/"+bucket+"/"+key+"?uploadId="+uploadID, body)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func mpListParts(t *testing.T, h http.Handler, target string) ListPartsResult {
	t.Helper()
	rec := do(t, h, testReq{method: http.MethodGet, target: target})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var res ListPartsResult
	require.NoError(t, xml.Unmarshal(rec.Body.Bytes(), &res))
	return res
}

func TestMultipartFlow(t *testing.T) {
	h, b := newTestHandler(t)
	mustCreateBucket(t, b, "bkt")

	rec := do(t, h, testReq{
		method:  http.MethodPost,
		target:  "/bkt/dir/my%20file.bin?uploads",
		headers: map[string]string{"Content-Type": "text/plain", "x-amz-meta-owner": "me"},
	})
	require.Equal(t, http.StatusOK, rec.Code)
	var init InitiateMultipartUploadResult
	require.NoError(t, xml.Unmarshal(rec.Body.Bytes(), &init))
	require.Equal(t, "dir/my file.bin", init.Key)
	require.Equal(t, s3Namespace, init.Xmlns)

	first, last := mpBigPart(), []byte("tail")
	key := "dir/my%20file.bin"
	e2 := mpPut(t, h, "bkt", key, init.UploadID, 2, last)
	e1 := mpPut(t, h, "bkt", key, init.UploadID, 1, first)

	parts := mpListParts(t, h, "/bkt/"+key+"?uploadId="+init.UploadID)
	require.Len(t, parts.Parts, 2)
	require.Equal(t, 1, parts.Parts[0].PartNumber)
	require.Equal(t, int64(len(first)), parts.Parts[0].Size)
	require.Equal(t, e1, parts.Parts[0].ETag)
	require.Equal(t, e2, parts.Parts[1].ETag)
	require.Equal(t, "STANDARD", parts.StorageClass)
	require.Equal(t, defaultOwner, parts.Owner)
	require.False(t, parts.IsTruncated)

	rec = mpComplete(h, "bkt", key, init.UploadID, mpCompleteBody(
		CompletePart{PartNumber: 1, ETag: e1}, CompletePart{PartNumber: 2, ETag: e2}))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var done CompleteMultipartUploadResult
	require.NoError(t, xml.Unmarshal(rec.Body.Bytes(), &done))
	wantETag, err := storage.MultipartETag([]string{mpMD5Hex(first), mpMD5Hex(last)})
	require.NoError(t, err)
	require.Equal(t, `"`+wantETag+`"`, done.ETag)
	require.Equal(t, "http://example.com/bkt/dir/my%20file.bin", done.Location)
	require.Equal(t, "bkt", done.Bucket)

	rc, obj, err := b.GetObject(t.Context(), "bkt", "dir/my file.bin", nil)
	require.NoError(t, err)
	data, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.NoError(t, rc.Close())
	require.Equal(t, append(append([]byte{}, first...), last...), data)
	require.Equal(t, 2, obj.PartsCount)
	require.Equal(t, "text/plain", obj.Meta.ContentType)
	require.Equal(t, "me", obj.Meta.User["owner"])

	requireS3Error(t, do(t, h, testReq{method: http.MethodGet, target: "/bkt/" + key + "?uploadId=" + init.UploadID}), 404, "NoSuchUpload")
}

func TestMultipartCreateMissingBucket(t *testing.T) {
	h, _ := newTestHandler(t)
	requireS3Error(t, do(t, h, testReq{method: http.MethodPost, target: "/nobucket/k?uploads"}), 404, "NoSuchBucket")
}

func TestListPartsPagination(t *testing.T) {
	h, b := newTestHandler(t)
	mustCreateBucket(t, b, "bkt")
	id := mpCreate(t, h, "bkt", "k")
	for _, n := range []int{3, 1, 2, 5} {
		mpPut(t, h, "bkt", "k", id, n, []byte("p"+strconv.Itoa(n)))
	}

	tests := []struct {
		name      string
		query     string
		wantNums  []int
		truncated bool
		next      int
		maxParts  int
	}{
		{"default", "", []int{1, 2, 3, 5}, false, 0, 1000},
		{"first page", "&max-parts=2", []int{1, 2}, true, 2, 2},
		{"second page", "&max-parts=2&part-number-marker=2", []int{3, 5}, false, 0, 2},
		{"marker past end", "&part-number-marker=5", nil, false, 0, 1000},
		{"cap at 1000", "&max-parts=5000", []int{1, 2, 3, 5}, false, 0, 1000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := mpListParts(t, h, "/bkt/k?uploadId="+id+tt.query)
			var nums []int
			for _, p := range res.Parts {
				nums = append(nums, p.PartNumber)
			}
			require.Equal(t, tt.wantNums, nums)
			require.Equal(t, tt.truncated, res.IsTruncated)
			require.Equal(t, tt.next, res.NextPartNumberMarker)
			require.Equal(t, tt.maxParts, res.MaxParts)
		})
	}

	requireS3Error(t, do(t, h, testReq{method: http.MethodGet, target: "/bkt/k?uploadId=" + id + "&max-parts=abc"}), 400, "InvalidArgument")
}

func TestListMultipartUploads(t *testing.T) {
	h, b := newTestHandler(t)
	mustCreateBucket(t, b, "bkt")
	for _, k := range []string{"a/1", "a/2", "b/1"} {
		mpCreate(t, h, "bkt", k)
	}
	mpCreate(t, h, "bkt", "a/1")

	list := func(query string) ListMultipartUploadsResult {
		rec := do(t, h, testReq{method: http.MethodGet, target: "/bkt?uploads" + query})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var res ListMultipartUploadsResult
		require.NoError(t, xml.Unmarshal(rec.Body.Bytes(), &res))
		return res
	}
	keys := func(res ListMultipartUploadsResult) []string {
		var out []string
		for _, u := range res.Uploads {
			out = append(out, u.Key)
		}
		return out
	}

	all := list("")
	require.Equal(t, []string{"a/1", "a/1", "a/2", "b/1"}, keys(all))
	require.False(t, all.IsTruncated)
	require.Equal(t, 1000, all.MaxUploads)
	require.Equal(t, "STANDARD", all.Uploads[0].StorageClass)

	require.Equal(t, []string{"a/1", "a/1", "a/2"}, keys(list("&prefix=a/")))
	require.Equal(t, "a/", list("&prefix=a/").Prefix)

	page1 := list("&max-uploads=2")
	require.True(t, page1.IsTruncated)
	require.Equal(t, []string{"a/1", "a/1"}, keys(page1))
	require.Equal(t, "a/1", page1.NextKeyMarker)
	require.Equal(t, page1.Uploads[1].UploadID, page1.NextUploadIDMarker)

	page2 := list("&max-uploads=2&key-marker=" + page1.NextKeyMarker + "&upload-id-marker=" + page1.NextUploadIDMarker)
	require.Equal(t, []string{"a/2", "b/1"}, keys(page2))
	require.False(t, page2.IsTruncated)

	require.Equal(t, []string{"a/2", "b/1"}, keys(list("&key-marker=a/1")))
	require.Equal(t, []string{"b/1"}, keys(list("&key-marker=a/2")))

	requireS3Error(t, do(t, h, testReq{method: http.MethodGet, target: "/nobucket?uploads"}), 404, "NoSuchBucket")
}

func TestCompleteMultipartErrors(t *testing.T) {
	h, b := newTestHandler(t)
	mustCreateBucket(t, b, "bkt")
	id := mpCreate(t, h, "bkt", "k")
	small1 := mpPut(t, h, "bkt", "k", id, 1, []byte("small"))
	small2 := mpPut(t, h, "bkt", "k", id, 2, []byte("small2"))

	tests := []struct {
		name   string
		id     string
		body   *strings.Reader
		status int
		code   string
	}{
		{"entity too small", id, mpCompleteBody(CompletePart{PartNumber: 1, ETag: small1}, CompletePart{PartNumber: 2, ETag: small2}), 400, "EntityTooSmall"},
		{"unknown part", id, mpCompleteBody(CompletePart{PartNumber: 9, ETag: small1}), 400, "InvalidPart"},
		{"wrong etag", id, mpCompleteBody(CompletePart{PartNumber: 2, ETag: `"deadbeef"`}), 400, "InvalidPart"},
		{"part order", id, mpCompleteBody(CompletePart{PartNumber: 2, ETag: small2}, CompletePart{PartNumber: 1, ETag: small1}), 400, "InvalidPartOrder"},
		{"malformed xml", id, strings.NewReader("<CompleteMultipartUpload><Part>"), 400, "MalformedXML"},
		{"empty body", id, strings.NewReader(""), 400, "MalformedXML"},
		{"empty part list", id, mpCompleteBody(), 400, "MalformedXML"},
		{"no such upload", "nope", mpCompleteBody(CompletePart{PartNumber: 1, ETag: small1}), 404, "NoSuchUpload"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireS3Error(t, mpComplete(h, "bkt", "k", tt.id, tt.body), tt.status, tt.code)
		})
	}

	_, err := b.HeadObject(t.Context(), "bkt", "k")
	require.ErrorIs(t, err, storage.ErrNoSuchKey)
}

func TestAbortMultipartUpload(t *testing.T) {
	h, b := newTestHandler(t)
	mustCreateBucket(t, b, "bkt")
	id := mpCreate(t, h, "bkt", "k")
	mpPut(t, h, "bkt", "k", id, 1, []byte("x"))

	rec := do(t, h, testReq{method: http.MethodDelete, target: "/bkt/k?uploadId=" + id})
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Empty(t, rec.Body.String())

	requireS3Error(t, do(t, h, testReq{method: http.MethodDelete, target: "/bkt/k?uploadId=" + id}), 404, "NoSuchUpload")
	requireS3Error(t, do(t, h, testReq{method: http.MethodGet, target: "/bkt/k?uploadId=" + id}), 404, "NoSuchUpload")
	requireS3Error(t, do(t, h, mpPutReq("bkt", "k", id, 1, []byte("x"), nil)), 404, "NoSuchUpload")
}

func TestUploadPartValidation(t *testing.T) {
	h, b := newTestHandler(t)
	mustCreateBucket(t, b, "bkt")
	id := mpCreate(t, h, "bkt", "k")

	for _, n := range []string{"0", "10001", "-1", "abc", ""} {
		t.Run("partNumber="+n, func(t *testing.T) {
			rec := do(t, h, testReq{
				method: http.MethodPut,
				target: "/bkt/k?partNumber=" + n + "&uploadId=" + id,
				body:   strings.NewReader("x"),
			})
			requireS3Error(t, rec, 400, "InvalidArgument")
		})
	}
	for _, n := range []string{"1", "10000"} {
		mpPut(t, h, "bkt", "k", id, mustAtoi(t, n), []byte("x"))
	}

	t.Run("missing content length", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPut, "/bkt/k?partNumber=1&uploadId="+id, io.NopCloser(strings.NewReader("x")))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		requireS3Error(t, rec, 411, "MissingContentLength")
	})
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	require.NoError(t, err)
	return n
}

func TestUploadPartBodyChecks(t *testing.T) {
	data := []byte("hello part")

	tests := []struct {
		name    string
		opts    []Option
		req     func(id string) *http.Request
		status  int
		code    string
		wantETg string
	}{
		{
			name: "valid md5",
			req: func(id string) *http.Request {
				r := httptest.NewRequest(http.MethodPut, "/bkt/k?partNumber=1&uploadId="+id, bytes.NewReader(data))
				r.Header.Set("Content-MD5", mpMD5B64(data))
				return r
			},
			status: 200, wantETg: `"` + mpMD5Hex(data) + `"`,
		},
		{
			name: "md5 mismatch",
			req: func(id string) *http.Request {
				r := httptest.NewRequest(http.MethodPut, "/bkt/k?partNumber=1&uploadId="+id, bytes.NewReader(data))
				r.Header.Set("Content-MD5", mpMD5B64([]byte("other")))
				return r
			},
			status: 400, code: "BadDigest",
		},
		{
			name: "md5 not base64",
			req: func(id string) *http.Request {
				r := httptest.NewRequest(http.MethodPut, "/bkt/k?partNumber=1&uploadId="+id, bytes.NewReader(data))
				r.Header.Set("Content-MD5", "!!!")
				return r
			},
			status: 400, code: "InvalidDigest",
		},
		{
			name: "md5 wrong length",
			req: func(id string) *http.Request {
				r := httptest.NewRequest(http.MethodPut, "/bkt/k?partNumber=1&uploadId="+id, bytes.NewReader(data))
				r.Header.Set("Content-MD5", base64.StdEncoding.EncodeToString([]byte("short")))
				return r
			},
			status: 400, code: "InvalidDigest",
		},
		{
			name: "short body",
			req: func(id string) *http.Request {
				r := httptest.NewRequest(http.MethodPut, "/bkt/k?partNumber=1&uploadId="+id, bytes.NewReader(data))
				r.ContentLength = int64(len(data) + 5)
				return r
			},
			status: 400, code: "IncompleteBody",
		},
		{
			name: "declared size too large",
			opts: []Option{WithMaxObjectSize(5)},
			req: func(id string) *http.Request {
				return httptest.NewRequest(http.MethodPut, "/bkt/k?partNumber=1&uploadId="+id, bytes.NewReader(data))
			},
			status: 400, code: "EntityTooLarge",
		},
		{
			name: "streaming exceeds cap",
			opts: []Option{WithMaxObjectSize(5)},
			req: func(id string) *http.Request {
				r := httptest.NewRequest(http.MethodPut, "/bkt/k?partNumber=1&uploadId="+id, io.NopCloser(bytes.NewReader(data)))
				r.Header.Set("x-amz-content-sha256", "STREAMING-AWS4-HMAC-SHA256-PAYLOAD")
				return r
			},
			status: 400, code: "EntityTooLarge",
		},
		{
			name: "streaming decoded length ok",
			req: func(id string) *http.Request {
				r := httptest.NewRequest(http.MethodPut, "/bkt/k?partNumber=1&uploadId="+id, io.NopCloser(bytes.NewReader(data)))
				r.Header.Set("x-amz-content-sha256", "STREAMING-AWS4-HMAC-SHA256-PAYLOAD")
				r.Header.Set("x-amz-decoded-content-length", strconv.Itoa(len(data)))
				return r
			},
			status: 200, wantETg: `"` + mpMD5Hex(data) + `"`,
		},
		{
			name: "streaming decoded length mismatch",
			req: func(id string) *http.Request {
				r := httptest.NewRequest(http.MethodPut, "/bkt/k?partNumber=1&uploadId="+id, io.NopCloser(bytes.NewReader(data)))
				r.Header.Set("x-amz-decoded-content-length", strconv.Itoa(len(data)+1))
				return r
			},
			status: 400, code: "IncompleteBody",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, b := newTestHandler(t, tt.opts...)
			mustCreateBucket(t, b, "bkt")
			id := mpCreate(t, h, "bkt", "k")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, tt.req(id))
			if tt.code != "" {
				requireS3Error(t, rec, tt.status, tt.code)
				require.Empty(t, mpListParts(t, h, "/bkt/k?uploadId="+id).Parts, "failed part must not be stored")
				return
			}
			require.Equal(t, tt.status, rec.Code, rec.Body.String())
			require.Equal(t, tt.wantETg, rec.Header().Get("ETag"))
			require.Len(t, mpListParts(t, h, "/bkt/k?uploadId="+id).Parts, 1)
		})
	}
}

type mpCoderErr struct{}

func (mpCoderErr) Error() string   { return "bad signature" }
func (mpCoderErr) S3Code() string  { return "SignatureDoesNotMatch" }
func (mpCoderErr) HTTPStatus() int { return http.StatusForbidden }

type mpFailingReader struct {
	data []byte
	err  error
}

func (f *mpFailingReader) Read(p []byte) (int, error) {
	if len(f.data) == 0 {
		return 0, f.err
	}
	n := copy(p, f.data)
	f.data = f.data[n:]
	return n, nil
}

func TestUploadPartBodyErrorPropagates(t *testing.T) {
	h, b := newTestHandler(t)
	mustCreateBucket(t, b, "bkt")
	id := mpCreate(t, h, "bkt", "k")

	r := httptest.NewRequest(http.MethodPut, "/bkt/k?partNumber=1&uploadId="+id,
		io.NopCloser(&mpFailingReader{data: []byte("abc"), err: mpCoderErr{}}))
	r.ContentLength = 10
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	requireS3Error(t, rec, 403, "SignatureDoesNotMatch")
	require.Empty(t, mpListParts(t, h, "/bkt/k?uploadId="+id).Parts)
}

func TestUploadPartCopy(t *testing.T) {
	h, b := newTestHandler(t)
	mustCreateBucket(t, b, "src")
	mustCreateBucket(t, b, "bkt")
	srcData := "0123456789"
	srcInfo := mustPut(t, b, "src", "dir/obj one", srcData)
	id := mpCreate(t, h, "bkt", "dst")

	future := time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)
	past := time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)
	source := "/src/dir/obj%20one"
	etag := `"` + srcInfo.ETag + `"`

	tests := []struct {
		name    string
		headers map[string]string
		partNo  string
		status  int
		code    string
		want    string
	}{
		{"whole object", map[string]string{"x-amz-copy-source": source}, "1", 200, "", srcData},
		{"without leading slash", map[string]string{"x-amz-copy-source": "src/dir/obj%20one"}, "1", 200, "", srcData},
		{"range", map[string]string{"x-amz-copy-source": source, "x-amz-copy-source-range": "bytes=2-5"}, "1", 200, "", "2345"},
		{"range last byte", map[string]string{"x-amz-copy-source": source, "x-amz-copy-source-range": "bytes=9-9"}, "1", 200, "", "9"},
		{"range beyond size", map[string]string{"x-amz-copy-source": source, "x-amz-copy-source-range": "bytes=5-10"}, "1", 400, "InvalidRequest", ""},
		{"range start beyond size", map[string]string{"x-amz-copy-source": source, "x-amz-copy-source-range": "bytes=20-30"}, "1", 400, "InvalidRequest", ""},
		{"range open ended", map[string]string{"x-amz-copy-source": source, "x-amz-copy-source-range": "bytes=2-"}, "1", 400, "InvalidArgument", ""},
		{"range suffix", map[string]string{"x-amz-copy-source": source, "x-amz-copy-source-range": "bytes=-3"}, "1", 400, "InvalidArgument", ""},
		{"range garbage", map[string]string{"x-amz-copy-source": source, "x-amz-copy-source-range": "bytes=a-b"}, "1", 400, "InvalidArgument", ""},
		{"range reversed", map[string]string{"x-amz-copy-source": source, "x-amz-copy-source-range": "bytes=5-2"}, "1", 400, "InvalidArgument", ""},
		{"if-match ok", map[string]string{"x-amz-copy-source": source, "x-amz-copy-source-if-match": etag}, "1", 200, "", srcData},
		{"if-match fails", map[string]string{"x-amz-copy-source": source, "x-amz-copy-source-if-match": `"nope"`}, "1", 412, "PreconditionFailed", ""},
		{"if-none-match hits", map[string]string{"x-amz-copy-source": source, "x-amz-copy-source-if-none-match": etag}, "1", 412, "PreconditionFailed", ""},
		{"if-none-match misses", map[string]string{"x-amz-copy-source": source, "x-amz-copy-source-if-none-match": `"nope"`}, "1", 200, "", srcData},
		{"if-unmodified-since fails", map[string]string{"x-amz-copy-source": source, "x-amz-copy-source-if-unmodified-since": past}, "1", 412, "PreconditionFailed", ""},
		{"if-unmodified-since ok", map[string]string{"x-amz-copy-source": source, "x-amz-copy-source-if-unmodified-since": future}, "1", 200, "", srcData},
		{"if-modified-since fails", map[string]string{"x-amz-copy-source": source, "x-amz-copy-source-if-modified-since": future}, "1", 412, "PreconditionFailed", ""},
		{"if-modified-since ok", map[string]string{"x-amz-copy-source": source, "x-amz-copy-source-if-modified-since": past}, "1", 200, "", srcData},
		{"missing source key", map[string]string{"x-amz-copy-source": "/src/nope"}, "1", 404, "NoSuchKey", ""},
		{"missing source bucket", map[string]string{"x-amz-copy-source": "/nobkt/k"}, "1", 404, "NoSuchBucket", ""},
		{"bad source", map[string]string{"x-amz-copy-source": "justbucket"}, "1", 400, "InvalidArgument", ""},
		{"bad part number", map[string]string{"x-amz-copy-source": source}, "0", 400, "InvalidArgument", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, h, testReq{
				method:  http.MethodPut,
				target:  "/bkt/dst?partNumber=" + tt.partNo + "&uploadId=" + id,
				headers: tt.headers,
			})
			if tt.code != "" {
				requireS3Error(t, rec, tt.status, tt.code)
				return
			}
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			var res CopyPartResult
			require.NoError(t, xml.Unmarshal(rec.Body.Bytes(), &res))
			require.Equal(t, `"`+mpMD5Hex([]byte(tt.want))+`"`, res.ETag)
			require.NotEmpty(t, res.LastModified)
			require.Equal(t, s3Namespace, res.Xmlns)
		})
	}

	t.Run("copied part completes into object", func(t *testing.T) {
		id := mpCreate(t, h, "bkt", "assembled")
		rec := do(t, h, testReq{
			method:  http.MethodPut,
			target:  "/bkt/assembled?partNumber=1&uploadId=" + id,
			headers: map[string]string{"x-amz-copy-source": source, "x-amz-copy-source-range": "bytes=0-3"},
		})
		require.Equal(t, http.StatusOK, rec.Code)
		var res CopyPartResult
		require.NoError(t, xml.Unmarshal(rec.Body.Bytes(), &res))
		rec = mpComplete(h, "bkt", "assembled", id, mpCompleteBody(CompletePart{PartNumber: 1, ETag: res.ETag}))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		rc, _, err := b.GetObject(t.Context(), "bkt", "assembled", nil)
		require.NoError(t, err)
		got, err := io.ReadAll(rc)
		require.NoError(t, err)
		require.NoError(t, rc.Close())
		require.Equal(t, "0123", string(got))
	})

	t.Run("no such upload", func(t *testing.T) {
		rec := do(t, h, testReq{
			method:  http.MethodPut,
			target:  "/bkt/dst?partNumber=1&uploadId=nope",
			headers: map[string]string{"x-amz-copy-source": source},
		})
		requireS3Error(t, rec, 404, "NoSuchUpload")
	})
}
