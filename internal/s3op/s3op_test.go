package s3op

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		method, target string
		copySrc        bool
		want           Op
		bucket, key    string
	}{
		{"GET", "/", false, ListBuckets, "", ""},
		{"PUT", "/b", false, CreateBucket, "b", ""},
		{"PUT", "/b/", false, CreateBucket, "b", ""},
		{"DELETE", "/b", false, DeleteBucket, "b", ""},
		{"HEAD", "/b", false, HeadBucket, "b", ""},
		{"GET", "/b?location", false, GetBucketLocation, "b", ""},
		{"GET", "/b?uploads", false, ListMultipartUploads, "b", ""},
		{"GET", "/b?list-type=2&prefix=x", false, ListObjectsV2, "b", ""},
		{"GET", "/b?prefix=x", false, ListObjects, "b", ""},
		{"POST", "/b?delete", false, DeleteObjects, "b", ""},
		{"GET", "/b?versioning", false, Unsupported, "b", ""},
		{"PUT", "/b/k/with/slash", false, PutObject, "b", "k/with/slash"},
		{"PUT", "/b/k", true, CopyObject, "b", "k"},
		{"PUT", "/b/k?partNumber=1&uploadId=u", false, UploadPart, "b", "k"},
		{"PUT", "/b/k?partNumber=1&uploadId=u", true, UploadPartCopy, "b", "k"},
		{"PUT", "/b/k?acl", false, PutObjectAcl, "b", "k"},
		{"GET", "/b/k", false, GetObject, "b", "k"},
		{"GET", "/b/k?acl", false, GetObjectAcl, "b", "k"},
		{"GET", "/b/k?uploadId=u", false, ListParts, "b", "k"},
		{"HEAD", "/b/k", false, HeadObject, "b", "k"},
		{"DELETE", "/b/k", false, DeleteObject, "b", "k"},
		{"DELETE", "/b/k?uploadId=u", false, AbortMultipartUpload, "b", "k"},
		{"POST", "/b/k?uploads", false, CreateMultipartUpload, "b", "k"},
		{"POST", "/b/k?uploadId=u", false, CompleteMultipartUpload, "b", "k"},
		{"GET", "/b/k?tagging", false, Unsupported, "b", "k"},
		{"OPTIONS", "/b/k", false, CORSPreflight, "b", "k"},
		{"GET", "/b/a%20b%2Fc", false, GetObject, "b", "a b/c"},
		{"GET", "/b//double", false, GetObject, "b", "/double"},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, c.target, nil)
		if c.copySrc {
			r.Header.Set("x-amz-copy-source", "/src/k")
		}
		info := Resolver{}.Resolve(r)
		assert.Equal(t, c.want, info.Op, "%s %s", c.method, c.target)
		assert.Equal(t, c.bucket, info.Bucket, "%s %s", c.method, c.target)
		assert.Equal(t, c.key, info.Key, "%s %s", c.method, c.target)
		assert.Len(t, info.RequestID, 16)
	}
}

func TestVirtualHost(t *testing.T) {
	rv := Resolver{Domain: "s3.local"}
	r := httptest.NewRequest("GET", "http://photos.s3.local:9000/a/b.jpg", nil)
	info := rv.Resolve(r)
	assert.Equal(t, "photos", info.Bucket)
	assert.Equal(t, "a/b.jpg", info.Key)
	assert.Equal(t, GetObject, info.Op)

	r = httptest.NewRequest("GET", "http://s3.local:9000/photos/a", nil)
	info = rv.Resolve(r)
	assert.Equal(t, "photos", info.Bucket, "falls back to path-style")
	assert.Equal(t, "a", info.Key)
}

func TestMiddleware(t *testing.T) {
	var got *Info
	h := Resolver{}.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = FromContext(r.Context())
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/b/k", nil))
	assert.NotNil(t, got)
	assert.Equal(t, got.RequestID, rec.Header().Get("x-amz-request-id"))

	r, info := Resolver{}.Ensure(httptest.NewRequest("HEAD", "/b", nil))
	assert.Equal(t, HeadBucket, info.Op)
	assert.Same(t, info, FromContext(r.Context()))
}
