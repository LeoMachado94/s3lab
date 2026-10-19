// Package fs is a filesystem storage backend.
//
// Layout (keys are never used as paths, only their SHA-256):
//
//	<root>/buckets/<bucket>/bucket.json
//	<root>/buckets/<bucket>/objects/<hash[:2]>/<hash>.data|.json
//	<root>/buckets/<bucket>/uploads/<uploadID>/upload.json, part-NNNNN(.json)
//	<root>/buckets/<bucket>/tmp/   staging area, same filesystem as the rest
package fs

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/LeoMachado94/s3lab/internal/storage"
)

const (
	bucketsDir  = "buckets"
	objectsDir  = "objects"
	uploadsDir  = "uploads"
	tmpDir      = "tmp"
	bucketFile  = "bucket.json"
	dirPerm     = 0o750
	dataSuffix  = ".data"
	metaSuffix  = ".json"
	tmpPattern  = "stage-*"
	hashDirLen  = 2
	jsonTmpName = "meta-*"
)

var _ storage.Backend = (*Backend)(nil)

// Backend stores buckets, objects and multipart uploads under a root directory.
type Backend struct {
	root string
	// mu guards bucket structure: object operations hold it shared,
	// bucket create/delete/reset hold it exclusively.
	mu    sync.RWMutex
	locks sync.Map // name -> *sync.RWMutex
}

type bucketRecord struct {
	Name      string
	CreatedAt time.Time
}

func New(root string) (*Backend, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("fs: resolve root: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(abs, bucketsDir), dirPerm); err != nil {
		return nil, fmt.Errorf("fs: create root: %w", err)
	}
	return &Backend{root: abs}, nil
}

func (b *Backend) bucketsPath() string { return filepath.Join(b.root, bucketsDir) }

// bucketDir validates the name and returns its directory; the bucket must exist.
func (b *Backend) bucketDir(name string) (string, error) {
	if err := storage.ValidateBucketName(name); err != nil {
		return "", err
	}
	dir := filepath.Join(b.bucketsPath(), name)
	if _, err := os.Stat(filepath.Join(dir, bucketFile)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", storage.ErrNoSuchBucket
		}
		return "", err
	}
	return dir, nil
}

// keyLock returns the lock for name, creating it on first use.
func (b *Backend) keyLock(name string) *sync.RWMutex {
	if l, ok := b.locks.Load(name); ok {
		return l.(*sync.RWMutex)
	}
	l, _ := b.locks.LoadOrStore(name, new(sync.RWMutex))
	return l.(*sync.RWMutex)
}

func (b *Backend) ListBuckets(ctx context.Context) ([]storage.Bucket, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	entries, err := os.ReadDir(b.bucketsPath())
	if err != nil {
		return nil, err
	}
	out := make([]storage.Bucket, 0, len(entries))
	for _, e := range entries {
		rec, err := readBucket(filepath.Join(b.bucketsPath(), e.Name()))
		if err != nil {
			continue // half-created bucket
		}
		out = append(out, storage.Bucket{Name: rec.Name, CreatedAt: rec.CreatedAt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func readBucket(dir string) (bucketRecord, error) {
	var rec bucketRecord
	err := readJSON(filepath.Join(dir, bucketFile), &rec)
	return rec, err
}

func (b *Backend) CreateBucket(ctx context.Context, bucket string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := storage.ValidateBucketName(bucket); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	dir := filepath.Join(b.bucketsPath(), bucket)
	if _, err := os.Stat(filepath.Join(dir, bucketFile)); err == nil {
		return storage.ErrBucketAlreadyOwnedByYou
	}
	if err := initBucketDirs(dir); err != nil {
		return err
	}
	// bucket.json is written last: it marks the bucket as fully created.
	tmp, err := writeJSONTemp(filepath.Join(dir, tmpDir), bucketRecord{Name: bucket, CreatedAt: time.Now().UTC()})
	if err != nil {
		return err
	}
	defer removeQuiet(tmp)
	return os.Rename(tmp, filepath.Join(dir, bucketFile))
}

func initBucketDirs(dir string) error {
	for _, sub := range []string{objectsDir, uploadsDir, tmpDir} {
		if err := os.MkdirAll(filepath.Join(dir, sub), dirPerm); err != nil {
			return err
		}
	}
	return nil
}

func (b *Backend) HeadBucket(ctx context.Context, bucket string) (storage.Bucket, error) {
	if err := ctx.Err(); err != nil {
		return storage.Bucket{}, err
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	dir, err := b.bucketDir(bucket)
	if err != nil {
		return storage.Bucket{}, err
	}
	rec, err := readBucket(dir)
	if err != nil {
		return storage.Bucket{}, err
	}
	return storage.Bucket{Name: rec.Name, CreatedAt: rec.CreatedAt}, nil
}

func (b *Backend) DeleteBucket(ctx context.Context, bucket string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	dir, err := b.bucketDir(bucket)
	if err != nil {
		return err
	}
	empty, err := bucketEmpty(dir)
	if err != nil {
		return err
	}
	if !empty {
		return storage.ErrBucketNotEmpty
	}
	return os.RemoveAll(dir)
}

func bucketEmpty(dir string) (bool, error) {
	ups, err := os.ReadDir(filepath.Join(dir, uploadsDir))
	if err != nil {
		return false, err
	}
	if len(ups) > 0 {
		return false, nil
	}
	found := false
	err = filepath.WalkDir(filepath.Join(dir, objectsDir), func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && filepath.Ext(d.Name()) == metaSuffix {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return !found, err
}

func (b *Backend) Reset(ctx context.Context, bucket string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if bucket == "" {
		if err := os.RemoveAll(b.bucketsPath()); err != nil {
			return err
		}
		return os.MkdirAll(b.bucketsPath(), dirPerm)
	}
	dir, err := b.bucketDir(bucket)
	if err != nil {
		return err
	}
	for _, sub := range []string{objectsDir, uploadsDir, tmpDir} {
		if err := os.RemoveAll(filepath.Join(dir, sub)); err != nil {
			return err
		}
	}
	return initBucketDirs(dir)
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// writeJSONTemp writes v to a new temp file in dir and returns its path.
func writeJSONTemp(dir string, v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, jsonTmpName)
	if err != nil {
		return "", err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		removeQuiet(f.Name())
		return "", err
	}
	return f.Name(), nil
}

func removeQuiet(path string) { _ = os.Remove(path) }

// ctxReader aborts reads once ctx is done.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

type staged struct {
	path string
	size int64
	etag string
}

// stage streams r into a temp file in dir, hashing MD5 on the way, and fsyncs it.
func stage(ctx context.Context, dir string, r io.Reader) (staged, error) {
	f, err := os.CreateTemp(dir, tmpPattern)
	if err != nil {
		return staged{}, err
	}
	h := md5.New()
	n, err := io.Copy(io.MultiWriter(f, h), ctxReader{ctx: ctx, r: r})
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		removeQuiet(f.Name())
		return staged{}, err
	}
	return staged{path: f.Name(), size: n, etag: hex.EncodeToString(h.Sum(nil))}, nil
}
