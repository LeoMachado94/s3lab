package fs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/LeoMachado94/s3lab/internal/storage"
)

// objectPaths maps a key to its data and metadata files via SHA-256, so no key
// can traverse, collide or hit reserved names.
func objectPaths(bucketDir, key string) (data, meta string) {
	sum := sha256.Sum256([]byte(key))
	h := hex.EncodeToString(sum[:])
	base := filepath.Join(bucketDir, objectsDir, h[:hashDirLen], h)
	return base + dataSuffix, base + metaSuffix
}

func objectLockName(bucket, key string) string { return bucket + "/" + key }

func (b *Backend) PutObject(ctx context.Context, bucket, key string, r io.Reader, opts storage.PutOptions) (storage.ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return storage.ObjectInfo{}, err
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	dir, err := b.bucketDir(bucket)
	if err != nil {
		return storage.ObjectInfo{}, err
	}
	if err := storage.ValidateKey(key); err != nil {
		return storage.ObjectInfo{}, err
	}
	return b.store(ctx, dir, bucket, key, r, opts.Meta)
}

// store stages r and commits it as bucket/key.
func (b *Backend) store(ctx context.Context, dir, bucket, key string, r io.Reader, meta storage.ObjectMeta) (storage.ObjectInfo, error) {
	st, err := stage(ctx, filepath.Join(dir, tmpDir), r)
	if err != nil {
		return storage.ObjectInfo{}, err
	}
	defer removeQuiet(st.path)
	info := storage.ObjectInfo{
		Bucket:       bucket,
		Key:          key,
		Size:         st.size,
		ETag:         st.etag,
		LastModified: time.Now().UTC(),
		Meta:         meta,
	}
	if err := b.commit(dir, info, st.path); err != nil {
		return storage.ObjectInfo{}, err
	}
	return info, nil
}

// commit atomically publishes the staged body and its metadata under the key lock.
func (b *Backend) commit(dir string, info storage.ObjectInfo, dataTmp string) error {
	metaTmp, err := writeJSONTemp(filepath.Join(dir, tmpDir), info)
	if err != nil {
		return err
	}
	defer removeQuiet(metaTmp)
	dataPath, metaPath := objectPaths(dir, info.Key)
	if err := os.MkdirAll(filepath.Dir(dataPath), dirPerm); err != nil {
		return err
	}
	l := b.keyLock(objectLockName(info.Bucket, info.Key))
	l.Lock()
	defer l.Unlock()
	if err := os.Rename(dataTmp, dataPath); err != nil {
		return err
	}
	return os.Rename(metaTmp, metaPath)
}

// openObject returns the open body and metadata; the file stays valid even if
// the key is overwritten afterwards.
func (b *Backend) openObject(dir, bucket, key string) (*os.File, storage.ObjectInfo, error) {
	if err := storage.ValidateKey(key); err != nil {
		return nil, storage.ObjectInfo{}, err
	}
	dataPath, metaPath := objectPaths(dir, key)
	l := b.keyLock(objectLockName(bucket, key))
	l.RLock()
	defer l.RUnlock()
	var info storage.ObjectInfo
	if err := readJSON(metaPath, &info); err != nil {
		return nil, storage.ObjectInfo{}, missingKey(err)
	}
	f, err := os.Open(dataPath)
	if err != nil {
		return nil, storage.ObjectInfo{}, missingKey(err)
	}
	return f, info, nil
}

func missingKey(err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return storage.ErrNoSuchKey
	}
	return err
}

type rangeReadCloser struct {
	io.Reader
	io.Closer
}

func (b *Backend) GetObject(ctx context.Context, bucket, key string, rng *storage.ByteRange) (io.ReadCloser, storage.ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, storage.ObjectInfo{}, err
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	dir, err := b.bucketDir(bucket)
	if err != nil {
		return nil, storage.ObjectInfo{}, err
	}
	f, info, err := b.openObject(dir, bucket, key)
	if err != nil {
		return nil, storage.ObjectInfo{}, err
	}
	if rng == nil {
		return f, info, nil
	}
	off, n, err := rng.Resolve(info.Size)
	if err != nil {
		_ = f.Close()
		return nil, storage.ObjectInfo{}, err
	}
	return rangeReadCloser{Reader: io.NewSectionReader(f, off, n), Closer: f}, info, nil
}

func (b *Backend) HeadObject(ctx context.Context, bucket, key string) (storage.ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return storage.ObjectInfo{}, err
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	dir, err := b.bucketDir(bucket)
	if err != nil {
		return storage.ObjectInfo{}, err
	}
	if err := storage.ValidateKey(key); err != nil {
		return storage.ObjectInfo{}, err
	}
	_, metaPath := objectPaths(dir, key)
	l := b.keyLock(objectLockName(bucket, key))
	l.RLock()
	defer l.RUnlock()
	var info storage.ObjectInfo
	if err := readJSON(metaPath, &info); err != nil {
		return storage.ObjectInfo{}, missingKey(err)
	}
	return info, nil
}

func (b *Backend) DeleteObject(ctx context.Context, bucket, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	dir, err := b.bucketDir(bucket)
	if err != nil {
		return err
	}
	if err := storage.ValidateKey(key); err != nil {
		return err
	}
	dataPath, metaPath := objectPaths(dir, key)
	l := b.keyLock(objectLockName(bucket, key))
	l.Lock()
	defer l.Unlock()
	// Metadata first: without it the object is already invisible.
	for _, p := range []string{metaPath, dataPath} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (b *Backend) CopyObject(ctx context.Context, src, dst storage.ObjectRef, opts storage.CopyOptions) (storage.ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return storage.ObjectInfo{}, err
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	srcDir, err := b.bucketDir(src.Bucket)
	if err != nil {
		return storage.ObjectInfo{}, err
	}
	dstDir, err := b.bucketDir(dst.Bucket)
	if err != nil {
		return storage.ObjectInfo{}, err
	}
	if err := storage.ValidateKey(dst.Key); err != nil {
		return storage.ObjectInfo{}, err
	}
	f, sinfo, err := b.openObject(srcDir, src.Bucket, src.Key)
	if err != nil {
		return storage.ObjectInfo{}, err
	}
	defer func() { _ = f.Close() }()

	var body io.Reader = f
	if opts.Range != nil {
		off, n, err := opts.Range.Resolve(sinfo.Size)
		if err != nil {
			return storage.ObjectInfo{}, err
		}
		body = io.NewSectionReader(f, off, n)
	}
	meta := sinfo.Meta
	if opts.ReplaceMeta {
		meta = opts.Meta
	}
	return b.store(ctx, dstDir, dst.Bucket, dst.Key, body, meta)
}

func (b *Backend) ListObjects(ctx context.Context, bucket string, opts storage.ListOptions) (storage.ListResult, error) {
	if err := ctx.Err(); err != nil {
		return storage.ListResult{}, err
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	dir, err := b.bucketDir(bucket)
	if err != nil {
		return storage.ListResult{}, err
	}
	objs, err := collectObjects(ctx, dir)
	if err != nil {
		return storage.ListResult{}, err
	}
	sort.Slice(objs, func(i, j int) bool { return objs[i].Key < objs[j].Key })
	return storage.ListFromSorted(objs, opts), nil
}

func collectObjects(ctx context.Context, dir string) ([]storage.ObjectInfo, error) {
	var objs []storage.ObjectInfo
	err := filepath.WalkDir(filepath.Join(dir, objectsDir), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), metaSuffix) {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		var info storage.ObjectInfo
		if err := readJSON(path, &info); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil // deleted while walking
			}
			return err
		}
		objs = append(objs, info)
		return nil
	})
	return objs, err
}
