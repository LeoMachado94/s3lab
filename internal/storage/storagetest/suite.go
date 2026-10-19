// Package storagetest is the behavioural contract every storage.Backend must
// satisfy. Backends call Run from their own _test.go files.
package storagetest

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeoMachado94/s3lab/internal/storage"
)

// Factory returns a fresh, empty backend. Called once per subtest.
type Factory func(t *testing.T) storage.Backend

func Run(t *testing.T, newBackend Factory) {
	t.Helper()
	tests := []struct {
		name string
		fn   func(t *testing.T, b storage.Backend)
	}{
		{"BucketLifecycle", testBucketLifecycle},
		{"BucketValidation", testBucketValidation},
		{"DeleteNonEmptyBucket", testDeleteNonEmptyBucket},
		{"PutGetHeadDelete", testPutGetHeadDelete},
		{"OverwriteObject", testOverwriteObject},
		{"MissingBucketAndKey", testMissingBucketAndKey},
		{"KeyValidation", testKeyValidation},
		{"TrickyKeys", testTrickyKeys},
		{"Metadata", testMetadata},
		{"EmptyObject", testEmptyObject},
		{"Range", testRange},
		{"CopyObject", testCopyObject},
		{"ListObjects", testListObjects},
		{"ListPagination", testListPagination},
		{"Multipart", testMultipart},
		{"MultipartValidation", testMultipartValidation},
		{"MultipartListAndAbort", testMultipartListAndAbort},
		{"ConcurrentPuts", testConcurrentPuts},
		{"Reset", testReset},
		{"ContextCanceled", testContextCanceled},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.fn(t, newBackend(t))
		})
	}
}

func ctx() context.Context { return context.Background() }

func md5hex(b []byte) string {
	s := md5.Sum(b)
	return hex.EncodeToString(s[:])
}

func mustBucket(t *testing.T, b storage.Backend, name string) {
	t.Helper()
	require.NoError(t, b.CreateBucket(ctx(), name))
}

func put(t *testing.T, b storage.Backend, bucket, key string, data []byte) storage.ObjectInfo {
	t.Helper()
	info, err := b.PutObject(ctx(), bucket, key, bytes.NewReader(data), storage.PutOptions{Size: int64(len(data))})
	require.NoError(t, err)
	return info
}

func get(t *testing.T, b storage.Backend, bucket, key string, rng *storage.ByteRange) ([]byte, storage.ObjectInfo) {
	t.Helper()
	rc, info, err := b.GetObject(ctx(), bucket, key, rng)
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(rc)
	require.NoError(t, err)
	return data, info
}

func keysOf(objs []storage.ObjectInfo) []string {
	out := make([]string, 0, len(objs))
	for _, o := range objs {
		out = append(out, o.Key)
	}
	return out
}

func testBucketLifecycle(t *testing.T, b storage.Backend) {
	before := time.Now().Add(-time.Second)
	mustBucket(t, b, "beta")
	mustBucket(t, b, "alpha")
	assert.ErrorIs(t, b.CreateBucket(ctx(), "alpha"), storage.ErrBucketAlreadyOwnedByYou)

	list, err := b.ListBuckets(ctx())
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, "alpha", list[0].Name, "buckets sorted by name")
	assert.Equal(t, "beta", list[1].Name)
	assert.True(t, list[0].CreatedAt.After(before))

	hb, err := b.HeadBucket(ctx(), "alpha")
	require.NoError(t, err)
	assert.Equal(t, "alpha", hb.Name)

	require.NoError(t, b.DeleteBucket(ctx(), "alpha"))
	_, err = b.HeadBucket(ctx(), "alpha")
	assert.ErrorIs(t, err, storage.ErrNoSuchBucket)
	assert.ErrorIs(t, b.DeleteBucket(ctx(), "alpha"), storage.ErrNoSuchBucket)
}

func testBucketValidation(t *testing.T, b storage.Backend) {
	for _, n := range []string{"ab", "UPPER", "bad_name", "-x-", "../etc"} {
		assert.ErrorIs(t, b.CreateBucket(ctx(), n), storage.ErrInvalidBucketName, n)
	}
}

func testDeleteNonEmptyBucket(t *testing.T, b storage.Backend) {
	mustBucket(t, b, "full")
	put(t, b, "full", "k", []byte("x"))
	assert.ErrorIs(t, b.DeleteBucket(ctx(), "full"), storage.ErrBucketNotEmpty)
	require.NoError(t, b.DeleteObject(ctx(), "full", "k"))

	_, err := b.CreateMultipartUpload(ctx(), "full", "mp", storage.ObjectMeta{})
	require.NoError(t, err)
	assert.ErrorIs(t, b.DeleteBucket(ctx(), "full"), storage.ErrBucketNotEmpty, "pending upload blocks delete")
}

func testPutGetHeadDelete(t *testing.T, b storage.Backend) {
	mustBucket(t, b, "bkt")
	data := []byte("hello world")
	before := time.Now().Add(-time.Second)
	info := put(t, b, "bkt", "greet.txt", data)
	assert.Equal(t, "bkt", info.Bucket)
	assert.Equal(t, "greet.txt", info.Key)
	assert.Equal(t, int64(len(data)), info.Size)
	assert.Equal(t, md5hex(data), info.ETag)
	assert.True(t, info.LastModified.After(before))

	got, ginfo := get(t, b, "bkt", "greet.txt", nil)
	assert.Equal(t, data, got)
	assert.Equal(t, info.ETag, ginfo.ETag)
	assert.Equal(t, info.Size, ginfo.Size)

	hinfo, err := b.HeadObject(ctx(), "bkt", "greet.txt")
	require.NoError(t, err)
	assert.Equal(t, info.ETag, hinfo.ETag)
	assert.Equal(t, info.Size, hinfo.Size)

	require.NoError(t, b.DeleteObject(ctx(), "bkt", "greet.txt"))
	_, err = b.HeadObject(ctx(), "bkt", "greet.txt")
	assert.ErrorIs(t, err, storage.ErrNoSuchKey)
	assert.NoError(t, b.DeleteObject(ctx(), "bkt", "greet.txt"), "delete missing key is not an error")
}

func testOverwriteObject(t *testing.T, b storage.Backend) {
	mustBucket(t, b, "bkt")
	put(t, b, "bkt", "k", []byte("first"))
	info := put(t, b, "bkt", "k", []byte("second!"))
	got, _ := get(t, b, "bkt", "k", nil)
	assert.Equal(t, "second!", string(got))
	assert.Equal(t, md5hex([]byte("second!")), info.ETag)
}

func testMissingBucketAndKey(t *testing.T, b storage.Backend) {
	_, err := b.PutObject(ctx(), "nobkt", "k", strings.NewReader("x"), storage.PutOptions{Size: 1})
	assert.ErrorIs(t, err, storage.ErrNoSuchBucket)
	_, _, err = b.GetObject(ctx(), "nobkt", "k", nil)
	assert.ErrorIs(t, err, storage.ErrNoSuchBucket)
	_, err = b.HeadObject(ctx(), "nobkt", "k")
	assert.ErrorIs(t, err, storage.ErrNoSuchBucket)
	assert.ErrorIs(t, b.DeleteObject(ctx(), "nobkt", "k"), storage.ErrNoSuchBucket)
	_, err = b.ListObjects(ctx(), "nobkt", storage.ListOptions{})
	assert.ErrorIs(t, err, storage.ErrNoSuchBucket)
	_, err = b.CreateMultipartUpload(ctx(), "nobkt", "k", storage.ObjectMeta{})
	assert.ErrorIs(t, err, storage.ErrNoSuchBucket)

	mustBucket(t, b, "bkt")
	_, _, err = b.GetObject(ctx(), "bkt", "missing", nil)
	assert.ErrorIs(t, err, storage.ErrNoSuchKey)
	_, err = b.HeadObject(ctx(), "bkt", "missing")
	assert.ErrorIs(t, err, storage.ErrNoSuchKey)
}

func testKeyValidation(t *testing.T, b storage.Backend) {
	mustBucket(t, b, "bkt")
	_, err := b.PutObject(ctx(), "bkt", "", strings.NewReader("x"), storage.PutOptions{Size: 1})
	assert.ErrorIs(t, err, storage.ErrInvalidKey)
	_, err = b.PutObject(ctx(), "bkt", strings.Repeat("k", storage.MaxKeyBytes+1), strings.NewReader("x"), storage.PutOptions{Size: 1})
	assert.ErrorIs(t, err, storage.ErrKeyTooLong)
}

// Keys that are valid in S3 but dangerous or awkward on a filesystem.
func testTrickyKeys(t *testing.T, b storage.Backend) {
	mustBucket(t, b, "bkt")
	mustBucket(t, b, "other")
	keys := []string{
		"../other/escape",
		"../../etc/passwd",
		"a//double",
		"/leading-slash",
		"trailing-slash/",
		"dir/",
		"dir/file",
		"space and ünïcödé ✓",
		".",
		"..",
		"back\\slash",
		"percent%2Fencoded",
		".s3lab/reserved",
		"x.meta.json",
	}
	for _, k := range keys {
		put(t, b, "bkt", k, []byte("v:"+k))
	}
	for _, k := range keys {
		got, _ := get(t, b, "bkt", k, nil)
		assert.Equal(t, "v:"+k, string(got), k)
	}
	res, err := b.ListObjects(ctx(), "bkt", storage.ListOptions{})
	require.NoError(t, err)
	assert.ElementsMatch(t, keys, keysOf(res.Objects))

	other, err := b.ListObjects(ctx(), "other", storage.ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, other.Objects, "keys must not escape their bucket")
}

func testMetadata(t *testing.T, b storage.Backend) {
	mustBucket(t, b, "bkt")
	meta := storage.ObjectMeta{
		ContentType:        "image/png",
		ContentEncoding:    "gzip",
		ContentDisposition: `attachment; filename="a.png"`,
		ContentLanguage:    "pt-BR",
		CacheControl:       "max-age=60",
		Expires:            "Wed, 21 Oct 2026 07:28:00 GMT",
		User:               map[string]string{"owner": "leo", "trace-id": "123"},
	}
	_, err := b.PutObject(ctx(), "bkt", "img", strings.NewReader("png"), storage.PutOptions{Size: 3, Meta: meta})
	require.NoError(t, err)

	info, err := b.HeadObject(ctx(), "bkt", "img")
	require.NoError(t, err)
	assert.Equal(t, meta, info.Meta)

	_, ginfo := get(t, b, "bkt", "img", nil)
	assert.Equal(t, meta, ginfo.Meta)

	list, err := b.ListObjects(ctx(), "bkt", storage.ListOptions{})
	require.NoError(t, err)
	require.Len(t, list.Objects, 1)
	assert.Equal(t, int64(3), list.Objects[0].Size)
	assert.Equal(t, md5hex([]byte("png")), list.Objects[0].ETag)
}

func testEmptyObject(t *testing.T, b storage.Backend) {
	mustBucket(t, b, "bkt")
	info := put(t, b, "bkt", "empty", nil)
	assert.Equal(t, int64(0), info.Size)
	assert.Equal(t, "d41d8cd98f00b204e9800998ecf8427e", info.ETag)
	got, _ := get(t, b, "bkt", "empty", nil)
	assert.Empty(t, got)
}

func testRange(t *testing.T, b storage.Backend) {
	mustBucket(t, b, "bkt")
	data := []byte("0123456789")
	put(t, b, "bkt", "r", data)

	got, info := get(t, b, "bkt", "r", &storage.ByteRange{Start: 2, End: 4})
	assert.Equal(t, "234", string(got))
	assert.Equal(t, int64(10), info.Size, "info carries full size")

	got, _ = get(t, b, "bkt", "r", &storage.ByteRange{Start: 7, End: -1})
	assert.Equal(t, "789", string(got))

	got, _ = get(t, b, "bkt", "r", &storage.ByteRange{Start: -3, End: -1})
	assert.Equal(t, "789", string(got))

	_, _, err := b.GetObject(ctx(), "bkt", "r", &storage.ByteRange{Start: 10, End: -1})
	assert.ErrorIs(t, err, storage.ErrInvalidRange)
}

func testCopyObject(t *testing.T, b storage.Backend) {
	mustBucket(t, b, "src")
	mustBucket(t, b, "dst")
	meta := storage.ObjectMeta{ContentType: "text/plain", User: map[string]string{"a": "1"}}
	_, err := b.PutObject(ctx(), "src", "k", strings.NewReader("payload"), storage.PutOptions{Size: 7, Meta: meta})
	require.NoError(t, err)

	info, err := b.CopyObject(ctx(), storage.ObjectRef{Bucket: "src", Key: "k"}, storage.ObjectRef{Bucket: "dst", Key: "copy"}, storage.CopyOptions{})
	require.NoError(t, err)
	assert.Equal(t, md5hex([]byte("payload")), info.ETag)
	got, ginfo := get(t, b, "dst", "copy", nil)
	assert.Equal(t, "payload", string(got))
	assert.Equal(t, meta, ginfo.Meta, "COPY directive keeps metadata")

	repl := storage.ObjectMeta{ContentType: "application/json", User: map[string]string{"b": "2"}}
	_, err = b.CopyObject(ctx(), storage.ObjectRef{Bucket: "src", Key: "k"}, storage.ObjectRef{Bucket: "src", Key: "k"}, storage.CopyOptions{ReplaceMeta: true, Meta: repl})
	require.NoError(t, err, "in-place copy with REPLACE updates metadata")
	h, err := b.HeadObject(ctx(), "src", "k")
	require.NoError(t, err)
	assert.Equal(t, repl, h.Meta)
	got, _ = get(t, b, "src", "k", nil)
	assert.Equal(t, "payload", string(got))

	_, err = b.CopyObject(ctx(), storage.ObjectRef{Bucket: "src", Key: "k"}, storage.ObjectRef{Bucket: "dst", Key: "part"}, storage.CopyOptions{Range: &storage.ByteRange{Start: 0, End: 2}})
	require.NoError(t, err)
	got, _ = get(t, b, "dst", "part", nil)
	assert.Equal(t, "pay", string(got))

	_, err = b.CopyObject(ctx(), storage.ObjectRef{Bucket: "src", Key: "nope"}, storage.ObjectRef{Bucket: "dst", Key: "x"}, storage.CopyOptions{})
	assert.ErrorIs(t, err, storage.ErrNoSuchKey)
	_, err = b.CopyObject(ctx(), storage.ObjectRef{Bucket: "src", Key: "k"}, storage.ObjectRef{Bucket: "nobkt", Key: "x"}, storage.CopyOptions{})
	assert.ErrorIs(t, err, storage.ErrNoSuchBucket)
}

func testListObjects(t *testing.T, b storage.Backend) {
	mustBucket(t, b, "bkt")
	keys := []string{"z", "a.txt", "dir/b", "dir/a", "dir/sub/c", "other/x", "dir-sibling"}
	for _, k := range keys {
		put(t, b, "bkt", k, []byte(k))
	}

	res, err := b.ListObjects(ctx(), "bkt", storage.ListOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"a.txt", "dir-sibling", "dir/a", "dir/b", "dir/sub/c", "other/x", "z"}, keysOf(res.Objects), "lexicographic order")
	assert.False(t, res.IsTruncated)

	res, err = b.ListObjects(ctx(), "bkt", storage.ListOptions{Delimiter: "/"})
	require.NoError(t, err)
	assert.Equal(t, []string{"a.txt", "dir-sibling", "z"}, keysOf(res.Objects))
	assert.Equal(t, []string{"dir/", "other/"}, res.CommonPrefixes)

	res, err = b.ListObjects(ctx(), "bkt", storage.ListOptions{Prefix: "dir/", Delimiter: "/"})
	require.NoError(t, err)
	assert.Equal(t, []string{"dir/a", "dir/b"}, keysOf(res.Objects))
	assert.Equal(t, []string{"dir/sub/"}, res.CommonPrefixes)

	res, err = b.ListObjects(ctx(), "bkt", storage.ListOptions{Prefix: "dir"})
	require.NoError(t, err)
	assert.Equal(t, []string{"dir-sibling", "dir/a", "dir/b", "dir/sub/c"}, keysOf(res.Objects))

	res, err = b.ListObjects(ctx(), "bkt", storage.ListOptions{StartAfter: "dir/b"})
	require.NoError(t, err)
	assert.Equal(t, []string{"dir/sub/c", "other/x", "z"}, keysOf(res.Objects))
}

func testListPagination(t *testing.T, b storage.Backend) {
	mustBucket(t, b, "bkt")
	var want []string
	for i := 0; i < 25; i++ {
		k := fmt.Sprintf("k%03d", i)
		want = append(want, k)
		put(t, b, "bkt", k, []byte("x"))
	}
	var got []string
	after := ""
	pages := 0
	for {
		res, err := b.ListObjects(ctx(), "bkt", storage.ListOptions{MaxKeys: 10, StartAfter: after})
		require.NoError(t, err)
		pages++
		got = append(got, keysOf(res.Objects)...)
		if !res.IsTruncated {
			assert.Empty(t, res.NextStartAfter)
			break
		}
		assert.Len(t, res.Objects, 10)
		after = res.NextStartAfter
		require.Less(t, pages, 10)
	}
	assert.Equal(t, 3, pages)
	assert.Equal(t, want, got)
}

func testMultipart(t *testing.T, b storage.Backend) {
	mustBucket(t, b, "bkt")
	meta := storage.ObjectMeta{ContentType: "application/octet-stream", User: map[string]string{"mp": "yes"}}
	id, err := b.CreateMultipartUpload(ctx(), "bkt", "big", meta)
	require.NoError(t, err)
	require.NotEmpty(t, id)

	p1 := bytes.Repeat([]byte("a"), storage.MinPartSize)
	p2 := []byte("tail")
	// Upload out of order and overwrite part 1 to check replacement semantics.
	i2, err := b.UploadPart(ctx(), "bkt", "big", id, 2, bytes.NewReader(p2), int64(len(p2)))
	require.NoError(t, err)
	_, err = b.UploadPart(ctx(), "bkt", "big", id, 1, strings.NewReader("garbage"), 7)
	require.NoError(t, err)
	i1, err := b.UploadPart(ctx(), "bkt", "big", id, 1, bytes.NewReader(p1), int64(len(p1)))
	require.NoError(t, err)
	assert.Equal(t, md5hex(p1), i1.ETag)
	assert.Equal(t, int64(len(p1)), i1.Size)

	_, err = b.HeadObject(ctx(), "bkt", "big")
	assert.ErrorIs(t, err, storage.ErrNoSuchKey, "object invisible before completion")

	info, err := b.CompleteMultipartUpload(ctx(), "bkt", "big", id, []storage.CompletePart{
		{PartNumber: 1, ETag: `"` + i1.ETag + `"`},
		{PartNumber: 2, ETag: i2.ETag},
	})
	require.NoError(t, err)
	wantETag, _ := storage.MultipartETag([]string{i1.ETag, i2.ETag})
	assert.Equal(t, wantETag, info.ETag)
	assert.Equal(t, int64(len(p1)+len(p2)), info.Size)
	assert.Equal(t, 2, info.PartsCount)

	got, ginfo := get(t, b, "bkt", "big", nil)
	assert.Equal(t, append(append([]byte{}, p1...), p2...), got)
	assert.Equal(t, meta, ginfo.Meta)

	_, err = b.ListParts(ctx(), "bkt", "big", id, storage.ListPartsOptions{})
	assert.ErrorIs(t, err, storage.ErrNoSuchUpload, "upload gone after completion")
	assert.NoError(t, b.DeleteObject(ctx(), "bkt", "big"))
	assert.NoError(t, b.DeleteBucket(ctx(), "bkt"), "no leftover upload state")
}

func testMultipartValidation(t *testing.T, b storage.Backend) {
	mustBucket(t, b, "bkt")
	_, err := b.UploadPart(ctx(), "bkt", "k", "nope", 1, strings.NewReader("x"), 1)
	assert.ErrorIs(t, err, storage.ErrNoSuchUpload)

	id, err := b.CreateMultipartUpload(ctx(), "bkt", "k", storage.ObjectMeta{})
	require.NoError(t, err)
	_, err = b.UploadPart(ctx(), "bkt", "k", id, 0, strings.NewReader("x"), 1)
	assert.ErrorIs(t, err, storage.ErrInvalidPartNumber)
	_, err = b.UploadPart(ctx(), "bkt", "k", id, storage.MaxPartNumber+1, strings.NewReader("x"), 1)
	assert.ErrorIs(t, err, storage.ErrInvalidPartNumber)
	_, err = b.UploadPart(ctx(), "bkt", "other-key", id, 1, strings.NewReader("x"), 1)
	assert.ErrorIs(t, err, storage.ErrNoSuchUpload, "upload id bound to key")

	a, err := b.UploadPart(ctx(), "bkt", "k", id, 1, strings.NewReader("small"), 5)
	require.NoError(t, err)
	c, err := b.UploadPart(ctx(), "bkt", "k", id, 2, strings.NewReader("small"), 5)
	require.NoError(t, err)

	_, err = b.CompleteMultipartUpload(ctx(), "bkt", "k", id, []storage.CompletePart{{PartNumber: 1, ETag: a.ETag}, {PartNumber: 2, ETag: c.ETag}})
	assert.ErrorIs(t, err, storage.ErrEntityTooSmall)
	_, err = b.CompleteMultipartUpload(ctx(), "bkt", "k", id, []storage.CompletePart{{PartNumber: 2, ETag: c.ETag}, {PartNumber: 1, ETag: a.ETag}})
	assert.ErrorIs(t, err, storage.ErrInvalidPartOrder)
	_, err = b.CompleteMultipartUpload(ctx(), "bkt", "k", id, []storage.CompletePart{{PartNumber: 1, ETag: "deadbeef"}})
	assert.ErrorIs(t, err, storage.ErrInvalidPart)
	_, err = b.CompleteMultipartUpload(ctx(), "bkt", "k", id, []storage.CompletePart{{PartNumber: 3, ETag: a.ETag}})
	assert.ErrorIs(t, err, storage.ErrInvalidPart)

	info, err := b.CompleteMultipartUpload(ctx(), "bkt", "k", id, []storage.CompletePart{{PartNumber: 1, ETag: a.ETag}})
	require.NoError(t, err, "single small part is allowed (last part)")
	assert.Equal(t, int64(5), info.Size)
}

func testMultipartListAndAbort(t *testing.T, b storage.Backend) {
	mustBucket(t, b, "bkt")
	id1, err := b.CreateMultipartUpload(ctx(), "bkt", "logs/a", storage.ObjectMeta{})
	require.NoError(t, err)
	id2, err := b.CreateMultipartUpload(ctx(), "bkt", "img/b", storage.ObjectMeta{})
	require.NoError(t, err)
	assert.NotEqual(t, id1, id2)

	ups, err := b.ListMultipartUploads(ctx(), "bkt", storage.ListUploadsOptions{})
	require.NoError(t, err)
	require.Len(t, ups.Uploads, 2)
	assert.Equal(t, "img/b", ups.Uploads[0].Key, "sorted by key")

	ups, err = b.ListMultipartUploads(ctx(), "bkt", storage.ListUploadsOptions{Prefix: "logs/"})
	require.NoError(t, err)
	require.Len(t, ups.Uploads, 1)
	assert.Equal(t, id1, ups.Uploads[0].UploadID)

	for n := 1; n <= 5; n++ {
		_, err := b.UploadPart(ctx(), "bkt", "logs/a", id1, n, strings.NewReader("p"), 1)
		require.NoError(t, err)
	}
	parts, err := b.ListParts(ctx(), "bkt", "logs/a", id1, storage.ListPartsOptions{MaxParts: 2})
	require.NoError(t, err)
	require.Len(t, parts.Parts, 2)
	assert.True(t, parts.IsTruncated)
	assert.Equal(t, 2, parts.NextPartNumberMarker)
	parts, err = b.ListParts(ctx(), "bkt", "logs/a", id1, storage.ListPartsOptions{PartNumberMarker: 2})
	require.NoError(t, err)
	require.Len(t, parts.Parts, 3)
	assert.Equal(t, 3, parts.Parts[0].PartNumber)
	assert.False(t, parts.IsTruncated)

	require.NoError(t, b.AbortMultipartUpload(ctx(), "bkt", "logs/a", id1))
	assert.ErrorIs(t, b.AbortMultipartUpload(ctx(), "bkt", "logs/a", id1), storage.ErrNoSuchUpload)
	_, err = b.ListParts(ctx(), "bkt", "logs/a", id1, storage.ListPartsOptions{})
	assert.ErrorIs(t, err, storage.ErrNoSuchUpload)
}

func testConcurrentPuts(t *testing.T, b storage.Backend) {
	mustBucket(t, b, "bkt")
	const writers = 16
	payloads := make([][]byte, writers)
	for i := range payloads {
		payloads[i] = bytes.Repeat([]byte{byte('a' + i)}, 64<<10)
	}
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(2)
		go func(p []byte) {
			defer wg.Done()
			_, err := b.PutObject(ctx(), "bkt", "hot", bytes.NewReader(p), storage.PutOptions{Size: int64(len(p))})
			assert.NoError(t, err)
		}(payloads[i])
		go func() {
			defer wg.Done()
			rc, info, err := b.GetObject(ctx(), "bkt", "hot", nil)
			if err != nil {
				assert.ErrorIs(t, err, storage.ErrNoSuchKey)
				return
			}
			defer func() { _ = rc.Close() }()
			data, err := io.ReadAll(rc)
			assert.NoError(t, err)
			assert.Len(t, data, 64<<10, "no torn reads")
			assert.Equal(t, md5hex(data), info.ETag, "body matches etag")
		}()
	}
	wg.Wait()
	got, info := get(t, b, "bkt", "hot", nil)
	assert.Equal(t, md5hex(got), info.ETag)
}

func testReset(t *testing.T, b storage.Backend) {
	mustBucket(t, b, "one")
	mustBucket(t, b, "two")
	put(t, b, "one", "a", []byte("1"))
	put(t, b, "two", "b", []byte("2"))
	_, err := b.CreateMultipartUpload(ctx(), "one", "mp", storage.ObjectMeta{})
	require.NoError(t, err)

	require.NoError(t, b.Reset(ctx(), "one"))
	res, err := b.ListObjects(ctx(), "one", storage.ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, res.Objects)
	ups, err := b.ListMultipartUploads(ctx(), "one", storage.ListUploadsOptions{})
	require.NoError(t, err)
	assert.Empty(t, ups.Uploads)
	_, err = b.HeadObject(ctx(), "two", "b")
	assert.NoError(t, err, "other buckets untouched")

	assert.ErrorIs(t, b.Reset(ctx(), "nobkt"), storage.ErrNoSuchBucket)

	require.NoError(t, b.Reset(ctx(), ""))
	list, err := b.ListBuckets(ctx())
	require.NoError(t, err)
	assert.Empty(t, list)
}

func testContextCanceled(t *testing.T, b storage.Backend) {
	mustBucket(t, b, "bkt")
	c, cancel := context.WithCancel(ctx())
	cancel()
	_, err := b.PutObject(c, "bkt", "k", strings.NewReader("x"), storage.PutOptions{Size: 1})
	assert.ErrorIs(t, err, context.Canceled)
	_, err = b.HeadObject(ctx(), "bkt", "k")
	assert.ErrorIs(t, err, storage.ErrNoSuchKey, "canceled put leaves no object")
}
