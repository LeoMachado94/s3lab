// Package memory is an in-memory storage.Backend. State is lost on exit.
package memory

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/LeoMachado94/s3lab/internal/storage"
)

var _ storage.Backend = (*Backend)(nil)

// Backend keeps buckets, objects and multipart uploads in memory.
type Backend struct {
	mu      sync.RWMutex
	buckets map[string]*bucket
}

type bucket struct {
	created time.Time
	objects map[string]*object
	uploads map[string]*upload
}

// object is immutable once stored; data is never modified after commit.
type object struct {
	info storage.ObjectInfo
	data []byte
}

type upload struct {
	key       string
	initiated time.Time
	meta      storage.ObjectMeta
	parts     map[int]*part
}

type part struct {
	info storage.PartInfo
	data []byte
}

// New returns an empty Backend.
func New() *Backend {
	return &Backend{buckets: make(map[string]*bucket)}
}

func cloneMeta(m storage.ObjectMeta) storage.ObjectMeta {
	m.User = maps.Clone(m.User)
	return m
}

func (o *object) snapshot() storage.ObjectInfo {
	info := o.info
	info.Meta = cloneMeta(info.Meta)
	return info
}

func newObject(bucketName, key string, data []byte, etag string, parts int, meta storage.ObjectMeta) *object {
	return &object{
		data: data,
		info: storage.ObjectInfo{
			Bucket:       bucketName,
			Key:          key,
			Size:         int64(len(data)),
			ETag:         etag,
			LastModified: time.Now().UTC(),
			Meta:         meta,
			PartsCount:   parts,
		},
	}
}

// readAll drains r, returning the data and its hex MD5.
func readAll(ctx context.Context, r io.Reader) ([]byte, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	h := md5.New()
	data, err := io.ReadAll(io.TeeReader(r, h))
	if err != nil {
		return nil, "", err
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	return data, hex.EncodeToString(h.Sum(nil)), nil
}

func newUploadID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generate upload id: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}

func (b *Backend) bucket(name string) (*bucket, error) {
	bk, ok := b.buckets[name]
	if !ok {
		return nil, storage.ErrNoSuchBucket
	}
	return bk, nil
}

func (b *Backend) lookup(bucketName, key string) (*object, error) {
	bk, err := b.bucket(bucketName)
	if err != nil {
		return nil, err
	}
	obj, ok := bk.objects[key]
	if !ok {
		return nil, storage.ErrNoSuchKey
	}
	return obj, nil
}

func (b *Backend) findUpload(bucketName, key, id string) (*bucket, *upload, error) {
	bk, err := b.bucket(bucketName)
	if err != nil {
		return nil, nil, err
	}
	up, ok := bk.uploads[id]
	if !ok || up.key != key {
		return nil, nil, storage.ErrNoSuchUpload
	}
	return bk, up, nil
}

func (b *Backend) ListBuckets(ctx context.Context) ([]storage.Bucket, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]storage.Bucket, 0, len(b.buckets))
	for name, bk := range b.buckets {
		out = append(out, storage.Bucket{Name: name, CreatedAt: bk.created})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (b *Backend) CreateBucket(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := storage.ValidateBucketName(name); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.buckets[name]; ok {
		return storage.ErrBucketAlreadyOwnedByYou
	}
	b.buckets[name] = &bucket{
		created: time.Now().UTC(),
		objects: make(map[string]*object),
		uploads: make(map[string]*upload),
	}
	return nil
}

func (b *Backend) DeleteBucket(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	bk, err := b.bucket(name)
	if err != nil {
		return err
	}
	if len(bk.objects) > 0 || len(bk.uploads) > 0 {
		return storage.ErrBucketNotEmpty
	}
	delete(b.buckets, name)
	return nil
}

func (b *Backend) HeadBucket(ctx context.Context, name string) (storage.Bucket, error) {
	if err := ctx.Err(); err != nil {
		return storage.Bucket{}, err
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	bk, err := b.bucket(name)
	if err != nil {
		return storage.Bucket{}, err
	}
	return storage.Bucket{Name: name, CreatedAt: bk.created}, nil
}

func (b *Backend) PutObject(ctx context.Context, bucketName, key string, r io.Reader, opts storage.PutOptions) (storage.ObjectInfo, error) {
	if err := storage.ValidateKey(key); err != nil {
		return storage.ObjectInfo{}, err
	}
	if err := ctx.Err(); err != nil {
		return storage.ObjectInfo{}, err
	}
	b.mu.RLock()
	_, err := b.bucket(bucketName)
	b.mu.RUnlock()
	if err != nil {
		return storage.ObjectInfo{}, err
	}
	data, etag, err := readAll(ctx, r)
	if err != nil {
		return storage.ObjectInfo{}, err
	}
	obj := newObject(bucketName, key, data, etag, 0, cloneMeta(opts.Meta))

	b.mu.Lock()
	defer b.mu.Unlock()
	bk, err := b.bucket(bucketName)
	if err != nil {
		return storage.ObjectInfo{}, err
	}
	if err := ctx.Err(); err != nil {
		return storage.ObjectInfo{}, err
	}
	bk.objects[key] = obj
	return obj.snapshot(), nil
}

func (b *Backend) GetObject(ctx context.Context, bucketName, key string, rng *storage.ByteRange) (io.ReadCloser, storage.ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, storage.ObjectInfo{}, err
	}
	b.mu.RLock()
	obj, err := b.lookup(bucketName, key)
	b.mu.RUnlock()
	if err != nil {
		return nil, storage.ObjectInfo{}, err
	}
	data := obj.data
	if rng != nil {
		off, n, err := rng.Resolve(obj.info.Size)
		if err != nil {
			return nil, storage.ObjectInfo{}, err
		}
		data = data[off : off+n]
	}
	return io.NopCloser(bytes.NewReader(data)), obj.snapshot(), nil
}

func (b *Backend) HeadObject(ctx context.Context, bucketName, key string) (storage.ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return storage.ObjectInfo{}, err
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	obj, err := b.lookup(bucketName, key)
	if err != nil {
		return storage.ObjectInfo{}, err
	}
	return obj.snapshot(), nil
}

func (b *Backend) DeleteObject(ctx context.Context, bucketName, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	bk, err := b.bucket(bucketName)
	if err != nil {
		return err
	}
	delete(bk.objects, key)
	return nil
}

func (b *Backend) CopyObject(ctx context.Context, src, dst storage.ObjectRef, opts storage.CopyOptions) (storage.ObjectInfo, error) {
	if err := storage.ValidateKey(dst.Key); err != nil {
		return storage.ObjectInfo{}, err
	}
	if err := ctx.Err(); err != nil {
		return storage.ObjectInfo{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	srcObj, err := b.lookup(src.Bucket, src.Key)
	if err != nil {
		return storage.ObjectInfo{}, err
	}
	dstBucket, err := b.bucket(dst.Bucket)
	if err != nil {
		return storage.ObjectInfo{}, err
	}
	data := srcObj.data
	if opts.Range != nil {
		off, n, err := opts.Range.Resolve(srcObj.info.Size)
		if err != nil {
			return storage.ObjectInfo{}, err
		}
		data = bytes.Clone(data[off : off+n])
	}
	sum := md5.Sum(data)
	etag := hex.EncodeToString(sum[:])
	meta := srcObj.info.Meta
	if opts.ReplaceMeta {
		meta = opts.Meta
	}
	obj := newObject(dst.Bucket, dst.Key, data, etag, 0, cloneMeta(meta))
	dstBucket.objects[dst.Key] = obj
	return obj.snapshot(), nil
}

func (b *Backend) ListObjects(ctx context.Context, bucketName string, opts storage.ListOptions) (storage.ListResult, error) {
	if err := ctx.Err(); err != nil {
		return storage.ListResult{}, err
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	bk, err := b.bucket(bucketName)
	if err != nil {
		return storage.ListResult{}, err
	}
	objs := make([]storage.ObjectInfo, 0, len(bk.objects))
	for _, o := range bk.objects {
		objs = append(objs, o.snapshot())
	}
	sort.Slice(objs, func(i, j int) bool { return objs[i].Key < objs[j].Key })
	return storage.ListFromSorted(objs, opts), nil
}

func (b *Backend) CreateMultipartUpload(ctx context.Context, bucketName, key string, meta storage.ObjectMeta) (string, error) {
	if err := storage.ValidateKey(key); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	id, err := newUploadID()
	if err != nil {
		return "", err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	bk, err := b.bucket(bucketName)
	if err != nil {
		return "", err
	}
	bk.uploads[id] = &upload{
		key:       key,
		initiated: time.Now().UTC(),
		meta:      cloneMeta(meta),
		parts:     make(map[int]*part),
	}
	return id, nil
}

func (b *Backend) UploadPart(ctx context.Context, bucketName, key, uploadID string, partNumber int, r io.Reader, _ int64) (storage.PartInfo, error) {
	if partNumber < 1 || partNumber > storage.MaxPartNumber {
		return storage.PartInfo{}, storage.ErrInvalidPartNumber
	}
	if err := ctx.Err(); err != nil {
		return storage.PartInfo{}, err
	}
	b.mu.RLock()
	_, _, err := b.findUpload(bucketName, key, uploadID)
	b.mu.RUnlock()
	if err != nil {
		return storage.PartInfo{}, err
	}
	data, etag, err := readAll(ctx, r)
	if err != nil {
		return storage.PartInfo{}, err
	}
	info := storage.PartInfo{
		PartNumber:   partNumber,
		ETag:         etag,
		Size:         int64(len(data)),
		LastModified: time.Now().UTC(),
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	_, up, err := b.findUpload(bucketName, key, uploadID)
	if err != nil {
		return storage.PartInfo{}, err
	}
	if err := ctx.Err(); err != nil {
		return storage.PartInfo{}, err
	}
	up.parts[partNumber] = &part{info: info, data: data}
	return info, nil
}

func (b *Backend) CompleteMultipartUpload(ctx context.Context, bucketName, key, uploadID string, parts []storage.CompletePart) (storage.ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return storage.ObjectInfo{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	bk, up, err := b.findUpload(bucketName, key, uploadID)
	if err != nil {
		return storage.ObjectInfo{}, err
	}
	uploaded := make(map[int]storage.PartInfo, len(up.parts))
	for n, p := range up.parts {
		uploaded[n] = p.info
	}
	if err := storage.ValidateCompleteParts(parts, uploaded); err != nil {
		return storage.ObjectInfo{}, err
	}
	data, etags := assemble(up, parts)
	etag, err := storage.MultipartETag(etags)
	if err != nil {
		return storage.ObjectInfo{}, err
	}
	obj := newObject(bucketName, key, data, etag, len(parts), cloneMeta(up.meta))
	bk.objects[key] = obj
	delete(bk.uploads, uploadID)
	return obj.snapshot(), nil
}

func assemble(up *upload, parts []storage.CompletePart) ([]byte, []string) {
	var size int
	for _, cp := range parts {
		size += len(up.parts[cp.PartNumber].data)
	}
	data := make([]byte, 0, size)
	etags := make([]string, 0, len(parts))
	for _, cp := range parts {
		p := up.parts[cp.PartNumber]
		data = append(data, p.data...)
		etags = append(etags, p.info.ETag)
	}
	return data, etags
}

func (b *Backend) AbortMultipartUpload(ctx context.Context, bucketName, key, uploadID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	bk, _, err := b.findUpload(bucketName, key, uploadID)
	if err != nil {
		return err
	}
	delete(bk.uploads, uploadID)
	return nil
}

func (b *Backend) ListParts(ctx context.Context, bucketName, key, uploadID string, opts storage.ListPartsOptions) (storage.ListPartsResult, error) {
	if err := ctx.Err(); err != nil {
		return storage.ListPartsResult{}, err
	}
	maxParts := opts.MaxParts
	if maxParts <= 0 {
		maxParts = storage.DefaultMaxParts
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	_, up, err := b.findUpload(bucketName, key, uploadID)
	if err != nil {
		return storage.ListPartsResult{}, err
	}
	var all []storage.PartInfo
	for n, p := range up.parts {
		if n > opts.PartNumberMarker {
			all = append(all, p.info)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].PartNumber < all[j].PartNumber })
	var res storage.ListPartsResult
	if len(all) > maxParts {
		all = all[:maxParts]
		res.IsTruncated = true
		res.NextPartNumberMarker = all[len(all)-1].PartNumber
	}
	res.Parts = all
	return res, nil
}

func (b *Backend) ListMultipartUploads(ctx context.Context, bucketName string, opts storage.ListUploadsOptions) (storage.ListUploadsResult, error) {
	if err := ctx.Err(); err != nil {
		return storage.ListUploadsResult{}, err
	}
	maxUploads := opts.MaxUploads
	if maxUploads <= 0 {
		maxUploads = storage.DefaultMaxKeys
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	bk, err := b.bucket(bucketName)
	if err != nil {
		return storage.ListUploadsResult{}, err
	}
	var all []storage.Upload
	for id, up := range bk.uploads {
		if strings.HasPrefix(up.key, opts.Prefix) {
			all = append(all, storage.Upload{Key: up.key, UploadID: id, Initiated: up.initiated})
		}
	}
	slices.SortFunc(all, func(a, c storage.Upload) int {
		if d := strings.Compare(a.Key, c.Key); d != 0 {
			return d
		}
		return a.Initiated.Compare(c.Initiated)
	})
	var res storage.ListUploadsResult
	if len(all) > maxUploads {
		all = all[:maxUploads]
		res.IsTruncated = true
	}
	res.Uploads = all
	return res, nil
}

func (b *Backend) Reset(ctx context.Context, bucketName string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if bucketName == "" {
		clear(b.buckets)
		return nil
	}
	bk, err := b.bucket(bucketName)
	if err != nil {
		return err
	}
	clear(bk.objects)
	clear(bk.uploads)
	return nil
}
