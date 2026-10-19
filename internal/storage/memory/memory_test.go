package memory

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeoMachado94/s3lab/internal/storage"
	"github.com/LeoMachado94/s3lab/internal/storage/storagetest"
)

func TestSuite(t *testing.T) {
	storagetest.Run(t, func(t *testing.T) storage.Backend { return New() })
}

func TestMetaNotAliased(t *testing.T) {
	ctx := context.Background()
	b := New()
	require.NoError(t, b.CreateBucket(ctx, "bkt"))
	user := map[string]string{"a": "1"}
	info, err := b.PutObject(ctx, "bkt", "k", bytes.NewReader([]byte("x")), storage.PutOptions{Meta: storage.ObjectMeta{User: user}})
	require.NoError(t, err)

	user["a"] = "mutated"
	info.Meta.User["a"] = "mutated"
	got, err := b.HeadObject(ctx, "bkt", "k")
	require.NoError(t, err)
	assert.Equal(t, "1", got.Meta.User["a"])

	got.Meta.User["a"] = "again"
	list, err := b.ListObjects(ctx, "bkt", storage.ListOptions{})
	require.NoError(t, err)
	assert.Equal(t, "1", list.Objects[0].Meta.User["a"])
}

func TestCopyDoesNotShareMeta(t *testing.T) {
	ctx := context.Background()
	b := New()
	require.NoError(t, b.CreateBucket(ctx, "bkt"))
	_, err := b.PutObject(ctx, "bkt", "src", bytes.NewReader([]byte("abcdef")), storage.PutOptions{Meta: storage.ObjectMeta{User: map[string]string{"a": "1"}}})
	require.NoError(t, err)
	src := storage.ObjectRef{Bucket: "bkt", Key: "src"}
	dst := storage.ObjectRef{Bucket: "bkt", Key: "dst"}
	info, err := b.CopyObject(ctx, src, dst, storage.CopyOptions{})
	require.NoError(t, err)
	info.Meta.User["a"] = "changed"

	got, err := b.HeadObject(ctx, "bkt", "src")
	require.NoError(t, err)
	assert.Equal(t, "1", got.Meta.User["a"])

	_, err = b.CopyObject(ctx, src, dst, storage.CopyOptions{Range: &storage.ByteRange{Start: 1, End: 2}})
	require.NoError(t, err)
	rc, _, err := b.GetObject(ctx, "bkt", "dst", nil)
	require.NoError(t, err)
	data, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, "bc", string(data))
}

func TestListParts(t *testing.T) {
	ctx := context.Background()
	b := New()
	require.NoError(t, b.CreateBucket(ctx, "bkt"))
	id, err := b.CreateMultipartUpload(ctx, "bkt", "k", storage.ObjectMeta{})
	require.NoError(t, err)
	for _, n := range []int{3, 1, 2} {
		_, err := b.UploadPart(ctx, "bkt", "k", id, n, bytes.NewReader([]byte{byte(n)}), 1)
		require.NoError(t, err)
	}
	res, err := b.ListParts(ctx, "bkt", "k", id, storage.ListPartsOptions{MaxParts: 2})
	require.NoError(t, err)
	assert.True(t, res.IsTruncated)
	assert.Equal(t, 2, res.NextPartNumberMarker)
	require.Len(t, res.Parts, 2)

	res, err = b.ListParts(ctx, "bkt", "k", id, storage.ListPartsOptions{PartNumberMarker: 2})
	require.NoError(t, err)
	assert.False(t, res.IsTruncated)
	require.Len(t, res.Parts, 1)
	assert.Equal(t, 3, res.Parts[0].PartNumber)
}

func TestListMultipartUploadsOrderAndLimit(t *testing.T) {
	ctx := context.Background()
	b := New()
	require.NoError(t, b.CreateBucket(ctx, "bkt"))
	for _, k := range []string{"b", "a", "a", "c/x"} {
		_, err := b.CreateMultipartUpload(ctx, "bkt", k, storage.ObjectMeta{})
		require.NoError(t, err)
	}
	res, err := b.ListMultipartUploads(ctx, "bkt", storage.ListUploadsOptions{MaxUploads: 3})
	require.NoError(t, err)
	assert.True(t, res.IsTruncated)
	require.Len(t, res.Uploads, 3)
	assert.Equal(t, []string{"a", "a", "b"}, []string{res.Uploads[0].Key, res.Uploads[1].Key, res.Uploads[2].Key})
	assert.False(t, res.Uploads[1].Initiated.Before(res.Uploads[0].Initiated))

	res, err = b.ListMultipartUploads(ctx, "bkt", storage.ListUploadsOptions{Prefix: "c/"})
	require.NoError(t, err)
	require.Len(t, res.Uploads, 1)
}

func TestCanceledContextAcrossOps(t *testing.T) {
	b := New()
	require.NoError(t, b.CreateBucket(context.Background(), "bkt"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := b.ListBuckets(ctx)
	assert.ErrorIs(t, err, context.Canceled)
	_, err = b.ListObjects(ctx, "bkt", storage.ListOptions{})
	assert.ErrorIs(t, err, context.Canceled)
	_, _, err = b.GetObject(ctx, "bkt", "k", nil)
	assert.ErrorIs(t, err, context.Canceled)
	assert.ErrorIs(t, b.Reset(ctx, ""), context.Canceled)
	assert.ErrorIs(t, b.DeleteBucket(ctx, "bkt"), context.Canceled)
}
