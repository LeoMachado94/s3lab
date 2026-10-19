package fs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeoMachado94/s3lab/internal/storage"
	"github.com/LeoMachado94/s3lab/internal/storage/storagetest"
)

func newBackend(t *testing.T) *Backend {
	t.Helper()
	b, err := New(t.TempDir())
	require.NoError(t, err)
	return b
}

func TestBackendContract(t *testing.T) {
	storagetest.Run(t, func(t *testing.T) storage.Backend { return newBackend(t) })
}

func TestPersistenceAcrossReopen(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	b1, err := New(root)
	require.NoError(t, err)
	require.NoError(t, b1.CreateBucket(ctx, "bkt"))
	meta := storage.ObjectMeta{ContentType: "text/plain", User: map[string]string{"a": "1"}}
	_, err = b1.PutObject(ctx, "bkt", "dir/file", strings.NewReader("hello"), storage.PutOptions{Meta: meta})
	require.NoError(t, err)
	id, err := b1.CreateMultipartUpload(ctx, "bkt", "mp", meta)
	require.NoError(t, err)
	part, err := b1.UploadPart(ctx, "bkt", "mp", id, 1, strings.NewReader("part"), 4)
	require.NoError(t, err)

	b2, err := New(root)
	require.NoError(t, err)
	buckets, err := b2.ListBuckets(ctx)
	require.NoError(t, err)
	require.Len(t, buckets, 1)
	rc, info, err := b2.GetObject(ctx, "bkt", "dir/file", nil)
	require.NoError(t, err)
	defer rc.Close()
	body, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(body))
	assert.Equal(t, meta, info.Meta)

	done, err := b2.CompleteMultipartUpload(ctx, "bkt", "mp", id, []storage.CompletePart{{PartNumber: 1, ETag: part.ETag}})
	require.NoError(t, err)
	assert.Equal(t, 1, done.PartsCount)
}

func TestNewFailsWhenRootIsFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(f, nil, 0o600))
	_, err := New(f)
	assert.Error(t, err)
}

func TestTraversalStaysInsideRoot(t *testing.T) {
	ctx := context.Background()
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	b, err := New(root)
	require.NoError(t, err)
	require.NoError(t, b.CreateBucket(ctx, "bkt"))

	keys := []string{"../../etc/passwd", "../escape", "/abs", "a//b", ".", "..", "dir/", "dir/file"}
	for _, k := range keys {
		_, err := b.PutObject(ctx, "bkt", k, strings.NewReader("x"), storage.PutOptions{})
		require.NoError(t, err, k)
		id, err := b.CreateMultipartUpload(ctx, "bkt", k, storage.ObjectMeta{})
		require.NoError(t, err, k)
		_, err = b.UploadPart(ctx, "bkt", k, id, 1, strings.NewReader("x"), 1)
		require.NoError(t, err, k)
	}

	entries, err := os.ReadDir(parent)
	require.NoError(t, err)
	require.Len(t, entries, 1, "nothing written beside root")

	bucketPath := filepath.Join(root, bucketsDir, "bkt")
	dataFiles, err := filepath.Glob(filepath.Join(bucketPath, objectsDir, "*", "*"+dataSuffix))
	require.NoError(t, err)
	assert.Len(t, dataFiles, len(keys), "distinct keys never collide")
}

func TestInvalidUploadIDs(t *testing.T) {
	ctx := context.Background()
	b := newBackend(t)
	require.NoError(t, b.CreateBucket(ctx, "bkt"))
	for _, id := range []string{"../x", "", "ABC", strings.Repeat("g", 32), strings.Repeat("a", 31)} {
		_, err := b.UploadPart(ctx, "bkt", "k", id, 1, strings.NewReader("x"), 1)
		assert.ErrorIs(t, err, storage.ErrNoSuchUpload, id)
		assert.ErrorIs(t, b.AbortMultipartUpload(ctx, "bkt", "k", id), storage.ErrNoSuchUpload, id)
		_, err = b.ListParts(ctx, "bkt", "k", id, storage.ListPartsOptions{})
		assert.ErrorIs(t, err, storage.ErrNoSuchUpload, id)
		_, err = b.CompleteMultipartUpload(ctx, "bkt", "k", id, nil)
		assert.ErrorIs(t, err, storage.ErrNoSuchUpload, id)
	}
}

func tmpEntries(t *testing.T, b *Backend) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(b.tmpPath())
	require.NoError(t, err)
	return entries
}

// cancelAfterFirstRead cancels the context once the first chunk was handed out.
type cancelAfterFirstRead struct {
	cancel context.CancelFunc
	data   io.Reader
}

func (c *cancelAfterFirstRead) Read(p []byte) (int, error) {
	n, err := c.data.Read(p[:1])
	c.cancel()
	return n, err
}

type failingReader struct{ err error }

func (f failingReader) Read([]byte) (int, error) { return 0, f.err }

func TestCancelAndErrorCleanTempFiles(t *testing.T) {
	b := newBackend(t)
	require.NoError(t, b.CreateBucket(context.Background(), "bkt"))

	ctx, cancel := context.WithCancel(context.Background())
	_, err := b.PutObject(ctx, "bkt", "k", &cancelAfterFirstRead{cancel: cancel, data: strings.NewReader("abcdef")}, storage.PutOptions{})
	assert.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, tmpEntries(t, b))

	boom := errors.New("boom")
	_, err = b.PutObject(context.Background(), "bkt", "k", io.MultiReader(strings.NewReader("ab"), failingReader{boom}), storage.PutOptions{})
	assert.ErrorIs(t, err, boom)
	assert.Empty(t, tmpEntries(t, b))
	_, err = b.HeadObject(context.Background(), "bkt", "k")
	assert.ErrorIs(t, err, storage.ErrNoSuchKey)

	id, err := b.CreateMultipartUpload(context.Background(), "bkt", "mp", storage.ObjectMeta{})
	require.NoError(t, err)
	ctx2, cancel2 := context.WithCancel(context.Background())
	_, err = b.UploadPart(ctx2, "bkt", "mp", id, 1, &cancelAfterFirstRead{cancel: cancel2, data: strings.NewReader("abcdef")}, 6)
	assert.ErrorIs(t, err, context.Canceled)
	parts, err := b.ListParts(context.Background(), "bkt", "mp", id, storage.ListPartsOptions{})
	require.NoError(t, err)
	assert.Empty(t, parts.Parts)
}

func TestCompleteCanceledKeepsUpload(t *testing.T) {
	bg := context.Background()
	b := newBackend(t)
	require.NoError(t, b.CreateBucket(bg, "bkt"))
	id, err := b.CreateMultipartUpload(bg, "bkt", "mp", storage.ObjectMeta{})
	require.NoError(t, err)
	p, err := b.UploadPart(bg, "bkt", "mp", id, 1, bytes.NewReader([]byte("data")), 4)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(bg)
	cancel()
	_, err = b.CompleteMultipartUpload(ctx, "bkt", "mp", id, []storage.CompletePart{{PartNumber: 1, ETag: p.ETag}})
	assert.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, tmpEntries(t, b))

	_, err = b.CompleteMultipartUpload(bg, "bkt", "mp", id, []storage.CompletePart{{PartNumber: 1, ETag: p.ETag}})
	assert.NoError(t, err)
}

func TestInvalidBucketAndKeyArguments(t *testing.T) {
	ctx := context.Background()
	b := newBackend(t)
	_, err := b.HeadBucket(ctx, "A")
	assert.ErrorIs(t, err, storage.ErrInvalidBucketName)
	_, err = b.HeadBucket(ctx, "nobkt")
	assert.ErrorIs(t, err, storage.ErrNoSuchBucket)
	require.NoError(t, b.CreateBucket(ctx, "bkt"))

	_, err = b.HeadObject(ctx, "bkt", "")
	assert.ErrorIs(t, err, storage.ErrInvalidKey)
	assert.ErrorIs(t, b.DeleteObject(ctx, "bkt", ""), storage.ErrInvalidKey)
	_, _, err = b.GetObject(ctx, "bkt", "", nil)
	assert.ErrorIs(t, err, storage.ErrInvalidKey)
	_, err = b.CreateMultipartUpload(ctx, "bkt", "", storage.ObjectMeta{})
	assert.ErrorIs(t, err, storage.ErrInvalidKey)

	_, err = b.PutObject(ctx, "bkt", "k", strings.NewReader("abc"), storage.PutOptions{})
	require.NoError(t, err)
	ref := storage.ObjectRef{Bucket: "bkt", Key: "k"}
	_, err = b.CopyObject(ctx, ref, storage.ObjectRef{Bucket: "bkt", Key: ""}, storage.CopyOptions{})
	assert.ErrorIs(t, err, storage.ErrInvalidKey)
	_, err = b.CopyObject(ctx, ref, ref, storage.CopyOptions{Range: &storage.ByteRange{Start: 5, End: -1}})
	assert.ErrorIs(t, err, storage.ErrInvalidRange)
	_, _, err = b.GetObject(ctx, "bkt", "k", &storage.ByteRange{Start: 9, End: -1})
	assert.ErrorIs(t, err, storage.ErrInvalidRange)
}

func TestListUploadsAndPartsLimits(t *testing.T) {
	ctx := context.Background()
	b := newBackend(t)
	require.NoError(t, b.CreateBucket(ctx, "bkt"))
	for _, k := range []string{"b", "a", "a"} {
		_, err := b.CreateMultipartUpload(ctx, "bkt", k, storage.ObjectMeta{})
		require.NoError(t, err)
	}
	res, err := b.ListMultipartUploads(ctx, "bkt", storage.ListUploadsOptions{MaxUploads: 2})
	require.NoError(t, err)
	assert.True(t, res.IsTruncated)
	require.Len(t, res.Uploads, 2)
	assert.Equal(t, "a", res.Uploads[0].Key)
	assert.Equal(t, "a", res.Uploads[1].Key)
	assert.False(t, res.Uploads[1].Initiated.Before(res.Uploads[0].Initiated))
}

func TestResetBucketKeepsBucketUsable(t *testing.T) {
	ctx := context.Background()
	b := newBackend(t)
	require.NoError(t, b.CreateBucket(ctx, "bkt"))
	_, err := b.PutObject(ctx, "bkt", "k", strings.NewReader("x"), storage.PutOptions{})
	require.NoError(t, err)
	require.NoError(t, b.Reset(ctx, "bkt"))
	_, err = b.PutObject(ctx, "bkt", "k2", strings.NewReader("y"), storage.PutOptions{})
	require.NoError(t, err)
	require.NoError(t, b.DeleteObject(ctx, "bkt", "k2"))
	assert.NoError(t, b.DeleteBucket(ctx, "bkt"))
}

func TestCanceledContextRejectedEarly(t *testing.T) {
	b := newBackend(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := b.ListBuckets(ctx)
	assert.ErrorIs(t, err, context.Canceled)
	assert.ErrorIs(t, b.CreateBucket(ctx, "bkt"), context.Canceled)
	assert.ErrorIs(t, b.Reset(ctx, ""), context.Canceled)
	_, err = b.ListObjects(ctx, "bkt", storage.ListOptions{})
	assert.ErrorIs(t, err, context.Canceled)
}
