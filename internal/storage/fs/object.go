package fs

import (
	"context"
	"crypto/rand"
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

// objectRecord is the metadata file: the object info plus the data file of the
// published version.
type objectRecord struct {
	storage.ObjectInfo
	DataFile string
}

// keyHash maps a key to its SHA-256, so no key can traverse, collide or hit reserved names.
func keyHash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func shardDir(bucketDir, hash string) string {
	return filepath.Join(bucketDir, objectsDir, hash[:hashDirLen])
}

func objectLockName(bucket, key string) string { return bucket + "/" + key }

func (b *Backend) PutObject(ctx context.Context, bucket, key string, r io.Reader, opts storage.PutOptions) (storage.ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return storage.ObjectInfo{}, err
	}
	if err := b.requireBucket(bucket); err != nil {
		return storage.ObjectInfo{}, err
	}
	if err := storage.ValidateKey(key); err != nil {
		return storage.ObjectInfo{}, err
	}
	return b.store(ctx, bucket, key, r, opts.Meta)
}

// store stages r without holding any backend lock and commits it as bucket/key.
func (b *Backend) store(ctx context.Context, bucket, key string, r io.Reader, meta storage.ObjectMeta) (storage.ObjectInfo, error) {
	st, err := stage(ctx, b.tmpPath(), r)
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
	if err := b.commit(info, st.path, nil); err != nil {
		return storage.ObjectInfo{}, err
	}
	return info, nil
}

// commit publishes the staged body under a fresh data file name and then swaps
// the metadata in with one rename. check, if set, runs under the bucket lock
// before anything is published.
func (b *Backend) commit(info storage.ObjectInfo, dataTmp string, check func(bucketDir string) error) error {
	hash := keyHash(info.Key)
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return err
	}
	dataName := hash + "." + hex.EncodeToString(rnd[:]) + dataSuffix
	metaTmp, err := writeJSONTemp(b.tmpPath(), objectRecord{ObjectInfo: info, DataFile: dataName})
	if err != nil {
		return err
	}
	defer removeQuiet(metaTmp)

	b.mu.RLock()
	defer b.mu.RUnlock()
	dir, err := b.bucketDir(info.Bucket)
	if err != nil {
		return err
	}
	if check != nil {
		if err := check(dir); err != nil {
			return err
		}
	}
	shard := shardDir(dir, hash)
	if err := os.MkdirAll(shard, dirPerm); err != nil {
		return err
	}
	metaPath := filepath.Join(shard, hash+metaSuffix)
	dataPath := filepath.Join(shard, dataName)
	l := b.keyLock(objectLockName(info.Bucket, info.Key))
	l.Lock()
	defer l.Unlock()
	var prev objectRecord
	prevErr := readJSON(metaPath, &prev)
	if err := os.Rename(dataTmp, dataPath); err != nil {
		return err
	}
	if err := os.Rename(metaTmp, metaPath); err != nil {
		removeQuiet(dataPath)
		return err
	}
	if prevErr == nil && prev.DataFile != "" {
		removeQuiet(filepath.Join(shard, filepath.Base(prev.DataFile)))
	}
	return nil
}

// openObject returns the open body and metadata; the file stays valid even if
// the key is overwritten afterwards. Callers hold b.mu shared.
func (b *Backend) openObject(dir, bucket, key string) (*os.File, storage.ObjectInfo, error) {
	if err := storage.ValidateKey(key); err != nil {
		return nil, storage.ObjectInfo{}, err
	}
	hash := keyHash(key)
	shard := shardDir(dir, hash)
	l := b.keyLock(objectLockName(bucket, key))
	l.RLock()
	defer l.RUnlock()
	var rec objectRecord
	if err := readJSON(filepath.Join(shard, hash+metaSuffix), &rec); err != nil {
		return nil, storage.ObjectInfo{}, missingKey(err)
	}
	f, err := os.Open(filepath.Join(shard, filepath.Base(rec.DataFile)))
	if err != nil {
		return nil, storage.ObjectInfo{}, missingKey(err)
	}
	return f, rec.ObjectInfo, nil
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
	hash := keyHash(key)
	l := b.keyLock(objectLockName(bucket, key))
	l.RLock()
	defer l.RUnlock()
	var rec objectRecord
	if err := readJSON(filepath.Join(shardDir(dir, hash), hash+metaSuffix), &rec); err != nil {
		return storage.ObjectInfo{}, missingKey(err)
	}
	return rec.ObjectInfo, nil
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
	hash := keyHash(key)
	shard := shardDir(dir, hash)
	metaPath := filepath.Join(shard, hash+metaSuffix)
	l := b.keyLock(objectLockName(bucket, key))
	l.Lock()
	defer l.Unlock()
	var rec objectRecord
	_ = readJSON(metaPath, &rec)
	// Metadata first: without it the object is already invisible.
	if err := os.Remove(metaPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if rec.DataFile != "" {
		removeQuiet(filepath.Join(shard, filepath.Base(rec.DataFile)))
	}
	return nil
}

func (b *Backend) CopyObject(ctx context.Context, src, dst storage.ObjectRef, opts storage.CopyOptions) (storage.ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return storage.ObjectInfo{}, err
	}
	f, sinfo, err := b.openCopySource(src, dst)
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
	return b.store(ctx, dst.Bucket, dst.Key, body, meta)
}

func (b *Backend) SetObjectMeta(ctx context.Context, bucket, key string, meta storage.ObjectMeta) (storage.ObjectInfo, error) {
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
	hash := keyHash(key)
	metaPath := filepath.Join(shardDir(dir, hash), hash+metaSuffix)
	l := b.keyLock(objectLockName(bucket, key))
	l.Lock()
	defer l.Unlock()
	var rec objectRecord
	if err := readJSON(metaPath, &rec); err != nil {
		return storage.ObjectInfo{}, missingKey(err)
	}
	rec.Meta = meta
	tmp, err := writeJSONTemp(b.tmpPath(), rec)
	if err != nil {
		return storage.ObjectInfo{}, err
	}
	defer removeQuiet(tmp)
	if err := os.Rename(tmp, metaPath); err != nil {
		return storage.ObjectInfo{}, err
	}
	return rec.ObjectInfo, nil
}

func (b *Backend) openCopySource(src, dst storage.ObjectRef) (*os.File, storage.ObjectInfo, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	srcDir, err := b.bucketDir(src.Bucket)
	if err != nil {
		return nil, storage.ObjectInfo{}, err
	}
	if _, err := b.bucketDir(dst.Bucket); err != nil {
		return nil, storage.ObjectInfo{}, err
	}
	if err := storage.ValidateKey(dst.Key); err != nil {
		return nil, storage.ObjectInfo{}, err
	}
	return b.openObject(srcDir, src.Bucket, src.Key)
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
		var rec objectRecord
		if err := readJSON(path, &rec); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil // deleted while walking
			}
			return err
		}
		objs = append(objs, rec.ObjectInfo)
		return nil
	})
	return objs, err
}
