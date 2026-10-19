package seed

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeoMachado94/s3lab/internal/storage"
	"github.com/LeoMachado94/s3lab/internal/storage/memory"
)

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
}

func body(t *testing.T, b storage.Backend, bucket, key string) (string, storage.ObjectInfo) {
	t.Helper()
	rc, info, err := b.GetObject(context.Background(), bucket, key, nil)
	require.NoError(t, err)
	defer rc.Close()
	data, err := io.ReadAll(rc)
	require.NoError(t, err)
	return string(data), info
}

func TestApplyBucketsAndDir(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	write(t, dir, "avatars/default.png", "png")
	write(t, dir, "documents/terms/v1.pdf", "pdf")
	write(t, dir, "documents/noext", "x")
	write(t, dir, "documents/.DS_Store", "junk")
	write(t, dir, "toplevel.txt", "ignored")
	outside := t.TempDir()
	write(t, outside, "secret.txt", "secret")
	require.NoError(t, os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(dir, "documents", "link.txt")))

	b := memory.New()
	require.NoError(t, b.CreateBucket(ctx, "empty-one"))
	s := New(b, []string{"empty-one", "other"}, dir)
	require.NoError(t, s.Apply(ctx))
	require.NoError(t, s.Apply(ctx))

	got, info := body(t, b, "documents", "terms/v1.pdf")
	require.Equal(t, "pdf", got)
	require.Equal(t, "application/pdf", info.Meta.ContentType)
	_, info = body(t, b, "documents", "noext")
	require.Equal(t, "application/octet-stream", info.Meta.ContentType)

	res, err := b.ListObjects(ctx, "documents", storage.ListOptions{})
	require.NoError(t, err)
	require.Len(t, res.Objects, 2)
	_, err = b.HeadBucket(ctx, "other")
	require.NoError(t, err)
}

func TestApplyBucket(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	write(t, dir, "a-bucket/x.txt", "x")
	write(t, dir, "b-bucket/y.txt", "y")
	b := memory.New()
	s := New(b, []string{"c-bucket"}, dir)

	require.NoError(t, s.ApplyBucket(ctx, "a-bucket"))
	_, err := b.HeadBucket(ctx, "b-bucket")
	require.ErrorIs(t, err, storage.ErrNoSuchBucket)
	_, err = b.HeadBucket(ctx, "c-bucket")
	require.ErrorIs(t, err, storage.ErrNoSuchBucket)
	require.NoError(t, s.ApplyBucket(ctx, "c-bucket"))
	require.NoError(t, s.ApplyBucket(ctx, "unknown"))
	_, err = b.HeadBucket(ctx, "unknown")
	require.ErrorIs(t, err, storage.ErrNoSuchBucket)
}

func TestErrors(t *testing.T) {
	ctx := context.Background()
	require.Error(t, New(memory.New(), nil, filepath.Join(t.TempDir(), "missing")).Apply(ctx))

	dir := t.TempDir()
	write(t, dir, "BAD_NAME/x.txt", "x")
	err := New(memory.New(), nil, dir).Apply(ctx)
	require.ErrorContains(t, err, "BAD_NAME")

	require.Error(t, New(memory.New(), []string{"X"}, "").Apply(ctx))
}

func TestNoop(t *testing.T) {
	require.NoError(t, New(memory.New(), nil, "").Apply(context.Background()))
}
