package fs

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeoMachado94/s3lab/internal/storage"
)

type blockingReader struct {
	started chan struct{}
	release chan struct{}
	once    bool
}

func (r *blockingReader) Read([]byte) (int, error) {
	if !r.once {
		r.once = true
		close(r.started)
	}
	<-r.release
	return 0, io.EOF
}

func within(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s blocked behind a stalled upload", what)
	}
}

func startStalledPut(t *testing.T, b *Backend) (*blockingReader, chan error) {
	t.Helper()
	br := &blockingReader{started: make(chan struct{}), release: make(chan struct{})}
	errc := make(chan error, 1)
	go func() {
		_, err := b.PutObject(context.Background(), "bkt", "slow", br, storage.PutOptions{})
		errc <- err
	}()
	<-br.started
	return br, errc
}

func TestStalledPutDoesNotBlockOthers(t *testing.T) {
	ctx := context.Background()
	b := newBackend(t)
	require.NoError(t, b.CreateBucket(ctx, "bkt"))
	br, errc := startStalledPut(t, b)

	within(t, "PutObject", func() {
		_, err := b.PutObject(ctx, "bkt", "other", strings.NewReader("x"), storage.PutOptions{})
		assert.NoError(t, err)
	})
	within(t, "HeadBucket", func() {
		_, err := b.HeadBucket(ctx, "bkt")
		assert.NoError(t, err)
	})
	within(t, "CreateBucket", func() { assert.NoError(t, b.CreateBucket(ctx, "newbkt")) })

	close(br.release)
	require.NoError(t, <-errc)
	_, err := b.HeadObject(ctx, "bkt", "slow")
	assert.NoError(t, err)
}

func TestStalledPutIntoDeletedBucketFails(t *testing.T) {
	ctx := context.Background()
	b := newBackend(t)
	require.NoError(t, b.CreateBucket(ctx, "bkt"))
	br, errc := startStalledPut(t, b)
	require.NoError(t, b.DeleteBucket(ctx, "bkt"))
	close(br.release)
	assert.ErrorIs(t, <-errc, storage.ErrNoSuchBucket)
	assert.Empty(t, tmpEntries(t, b))
}

func TestUploadPartBodyErrorPropagatesUnchanged(t *testing.T) {
	ctx := context.Background()
	b := newBackend(t)
	require.NoError(t, b.CreateBucket(ctx, "bkt"))
	id, err := b.CreateMultipartUpload(ctx, "bkt", "mp", storage.ObjectMeta{})
	require.NoError(t, err)

	pe := &os.PathError{Op: "read", Path: "client", Err: os.ErrNotExist}
	_, err = b.UploadPart(ctx, "bkt", "mp", id, 1, failingReader{fmt.Errorf("wrapped: %w", pe)}, 1)
	require.Error(t, err)
	assert.NotErrorIs(t, err, storage.ErrNoSuchUpload)
	var got *os.PathError
	require.ErrorAs(t, err, &got)
	assert.Same(t, pe, got)
}

func TestNewSweepsCrashLeftovers(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	b, err := New(root)
	require.NoError(t, err)
	require.NoError(t, b.CreateBucket(ctx, "bkt"))
	_, err = b.PutObject(ctx, "bkt", "k", strings.NewReader("keep"), storage.PutOptions{})
	require.NoError(t, err)

	hash := keyHash("k")
	shard := shardDir(filepath.Join(b.bucketsPath(), "bkt"), hash)
	orphan := filepath.Join(shard, hash+".deadbeef"+dataSuffix)
	stray := filepath.Join(b.tmpPath(), "stage-123")
	strayDir := filepath.Join(b.tmpPath(), "upload-9")
	legacy := filepath.Join(b.bucketsPath(), "bkt", tmpDir)
	require.NoError(t, os.WriteFile(orphan, []byte("junk"), 0o600))
	require.NoError(t, os.WriteFile(stray, []byte("junk"), 0o600))
	require.NoError(t, os.MkdirAll(strayDir, 0o750))
	require.NoError(t, os.MkdirAll(legacy, 0o750))

	b2, err := New(root)
	require.NoError(t, err)
	for _, p := range []string{orphan, stray, strayDir, legacy} {
		_, err := os.Stat(p)
		assert.ErrorIs(t, err, os.ErrNotExist, p)
	}
	assert.Empty(t, tmpEntries(t, b2))
	rc, _, err := b2.GetObject(ctx, "bkt", "k", nil)
	require.NoError(t, err)
	defer rc.Close()
	body, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, "keep", string(body))
}

func TestOverwriteLeavesSingleDataFile(t *testing.T) {
	ctx := context.Background()
	b := newBackend(t)
	require.NoError(t, b.CreateBucket(ctx, "bkt"))
	for _, v := range []string{"one", "two", "three"} {
		_, err := b.PutObject(ctx, "bkt", "k", strings.NewReader(v), storage.PutOptions{})
		require.NoError(t, err)
	}
	pattern := filepath.Join(b.bucketsPath(), "bkt", objectsDir, "*", "*")
	files, err := filepath.Glob(pattern + dataSuffix)
	require.NoError(t, err)
	assert.Len(t, files, 1)
	require.NoError(t, b.DeleteObject(ctx, "bkt", "k"))
	files, err = filepath.Glob(pattern)
	require.NoError(t, err)
	assert.Empty(t, files)
}
