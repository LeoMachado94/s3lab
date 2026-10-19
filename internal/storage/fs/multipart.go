package fs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/LeoMachado94/s3lab/internal/storage"
)

const (
	uploadFile   = "upload.json"
	partPrefix   = "part-"
	uploadIDSize = 16
)

// uploadIDRe gates client-supplied IDs before they reach paths or the lock map.
var uploadIDRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

type uploadRecord struct {
	Key       string
	UploadID  string
	Initiated time.Time
	Meta      storage.ObjectMeta
}

type partRecord struct {
	ETag         string
	Size         int64
	LastModified time.Time
}

func partName(n int) string { return fmt.Sprintf("%s%05d", partPrefix, n) }

// uploadLock validates the ID and returns the lock guarding that upload.
func (b *Backend) uploadLock(bucket, id string) (*sync.RWMutex, error) {
	if !uploadIDRe.MatchString(id) {
		return nil, storage.ErrNoSuchUpload
	}
	// The "upload:" prefix cannot clash with object locks (bucket names have no colon).
	return b.keyLock("upload:" + bucket + "/" + id), nil
}

// loadUpload resolves an upload bound to key; callers hold the upload lock.
func loadUpload(bucketDir, key, id string) (string, uploadRecord, error) {
	var rec uploadRecord
	dir := filepath.Join(bucketDir, uploadsDir, id)
	if err := readJSON(filepath.Join(dir, uploadFile), &rec); err != nil {
		return "", rec, noUpload(err)
	}
	if rec.Key != key {
		return "", rec, storage.ErrNoSuchUpload
	}
	return dir, rec, nil
}

// noUpload maps a vanished upload dir (completed or aborted concurrently).
func noUpload(err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return storage.ErrNoSuchUpload
	}
	return err
}

// withUpload resolves the bucket and upload under b.mu shared and runs fn. The
// caller holds the upload lock (lock order: upload lock, then b.mu).
func (b *Backend) withUpload(bucket, key, id string, fn func(dir, udir string, rec uploadRecord) error) error {
	b.mu.RLock()
	defer b.mu.RUnlock()
	dir, err := b.bucketDir(bucket)
	if err != nil {
		return err
	}
	udir, rec, err := loadUpload(dir, key, id)
	if err != nil {
		return err
	}
	return fn(dir, udir, rec)
}

func (b *Backend) CreateMultipartUpload(ctx context.Context, bucket, key string, meta storage.ObjectMeta) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := b.requireBucket(bucket); err != nil {
		return "", err
	}
	if err := storage.ValidateKey(key); err != nil {
		return "", err
	}
	raw := make([]byte, uploadIDSize)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	id := hex.EncodeToString(raw)
	// Built in tmp and renamed so listings never see a half-written upload.
	staging, err := os.MkdirTemp(b.tmpPath(), "upload-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(staging) }()
	rec := uploadRecord{Key: key, UploadID: id, Initiated: time.Now().UTC(), Meta: meta}
	tmp, err := writeJSONTemp(staging, rec)
	if err != nil {
		return "", err
	}
	if err := os.Rename(tmp, filepath.Join(staging, uploadFile)); err != nil {
		return "", err
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	dir, err := b.bucketDir(bucket)
	if err != nil {
		return "", err
	}
	if err := os.Rename(staging, filepath.Join(dir, uploadsDir, id)); err != nil {
		return "", noUpload(err)
	}
	return id, nil
}

func (b *Backend) UploadPart(ctx context.Context, bucket, key, uploadID string, partNumber int, r io.Reader, _ int64) (storage.PartInfo, error) {
	if err := ctx.Err(); err != nil {
		return storage.PartInfo{}, err
	}
	if partNumber < 1 || partNumber > storage.MaxPartNumber {
		return storage.PartInfo{}, storage.ErrInvalidPartNumber
	}
	if err := b.requireBucket(bucket); err != nil {
		return storage.PartInfo{}, err
	}
	l, err := b.uploadLock(bucket, uploadID)
	if err != nil {
		return storage.PartInfo{}, err
	}
	l.RLock()
	err = b.withUpload(bucket, key, uploadID, func(string, string, uploadRecord) error { return nil })
	l.RUnlock()
	if err != nil {
		return storage.PartInfo{}, err
	}
	// Body errors come from the caller's reader and are returned untouched.
	st, err := stage(ctx, b.tmpPath(), r)
	if err != nil {
		return storage.PartInfo{}, err
	}
	defer removeQuiet(st.path)
	info := storage.PartInfo{PartNumber: partNumber, ETag: st.etag, Size: st.size, LastModified: time.Now().UTC()}
	metaTmp, err := writeJSONTemp(b.tmpPath(), partRecord{ETag: info.ETag, Size: info.Size, LastModified: info.LastModified})
	if err != nil {
		return storage.PartInfo{}, err
	}
	defer removeQuiet(metaTmp)

	l.Lock()
	defer l.Unlock()
	err = b.withUpload(bucket, key, uploadID, func(_, udir string, _ uploadRecord) error {
		if err := os.Rename(st.path, filepath.Join(udir, partName(partNumber))); err != nil {
			return noUpload(err)
		}
		return noUpload(os.Rename(metaTmp, filepath.Join(udir, partName(partNumber)+metaSuffix)))
	})
	if err != nil {
		return storage.PartInfo{}, err
	}
	return info, nil
}

func readParts(udir string) (map[int]storage.PartInfo, error) {
	entries, err := os.ReadDir(udir)
	if err != nil {
		return nil, noUpload(err)
	}
	parts := make(map[int]storage.PartInfo)
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, partPrefix) || !strings.HasSuffix(name, metaSuffix) {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(strings.TrimSuffix(name, metaSuffix), partPrefix+"%d", &n); err != nil {
			continue
		}
		var rec partRecord
		if err := readJSON(filepath.Join(udir, name), &rec); err != nil {
			return nil, noUpload(err)
		}
		parts[n] = storage.PartInfo{PartNumber: n, ETag: rec.ETag, Size: rec.Size, LastModified: rec.LastModified}
	}
	return parts, nil
}

func (b *Backend) CompleteMultipartUpload(ctx context.Context, bucket, key, uploadID string, parts []storage.CompletePart) (storage.ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return storage.ObjectInfo{}, err
	}
	if err := b.requireBucket(bucket); err != nil {
		return storage.ObjectInfo{}, err
	}
	l, err := b.uploadLock(bucket, uploadID)
	if err != nil {
		return storage.ObjectInfo{}, err
	}
	l.Lock()
	defer l.Unlock()
	var (
		udir     string
		rec      uploadRecord
		uploaded map[int]storage.PartInfo
	)
	err = b.withUpload(bucket, key, uploadID, func(_, d string, r uploadRecord) error {
		udir, rec = d, r
		var err error
		uploaded, err = readParts(d)
		return err
	})
	if err != nil {
		return storage.ObjectInfo{}, err
	}
	if err := storage.ValidateCompleteParts(parts, uploaded); err != nil {
		return storage.ObjectInfo{}, err
	}
	etags := make([]string, len(parts))
	paths := make([]string, len(parts))
	for i, p := range parts {
		etags[i] = uploaded[p.PartNumber].ETag
		paths[i] = filepath.Join(udir, partName(p.PartNumber))
	}
	etag, err := storage.MultipartETag(etags)
	if err != nil {
		return storage.ObjectInfo{}, err
	}
	pr := &partsReader{paths: paths}
	defer pr.close()
	st, err := stage(ctx, b.tmpPath(), pr)
	if err != nil {
		return storage.ObjectInfo{}, err
	}
	defer removeQuiet(st.path)
	info := storage.ObjectInfo{
		Bucket:       bucket,
		Key:          key,
		Size:         st.size,
		ETag:         etag,
		LastModified: time.Now().UTC(),
		Meta:         rec.Meta,
		PartsCount:   len(parts),
	}
	uploadStillThere := func(string) error {
		return noUpload(statErr(filepath.Join(udir, uploadFile)))
	}
	if err := b.commit(info, st.path, uploadStillThere); err != nil {
		return storage.ObjectInfo{}, err
	}
	_ = os.RemoveAll(udir)
	return info, nil
}

func statErr(path string) error {
	_, err := os.Stat(path)
	return err
}

// partsReader reads files back to back, one open at a time.
type partsReader struct {
	paths []string
	cur   *os.File
}

func (p *partsReader) Read(buf []byte) (int, error) {
	for {
		if p.cur == nil {
			if len(p.paths) == 0 {
				return 0, io.EOF
			}
			f, err := os.Open(p.paths[0])
			if err != nil {
				return 0, err
			}
			p.cur, p.paths = f, p.paths[1:]
		}
		n, err := p.cur.Read(buf)
		if errors.Is(err, io.EOF) {
			p.close()
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (p *partsReader) close() {
	if p.cur != nil {
		_ = p.cur.Close()
		p.cur = nil
	}
}

func (b *Backend) AbortMultipartUpload(ctx context.Context, bucket, key, uploadID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := b.requireBucket(bucket); err != nil {
		return err
	}
	l, err := b.uploadLock(bucket, uploadID)
	if err != nil {
		return err
	}
	l.Lock()
	defer l.Unlock()
	return b.withUpload(bucket, key, uploadID, func(_, udir string, _ uploadRecord) error {
		return os.RemoveAll(udir)
	})
}

func (b *Backend) ListParts(ctx context.Context, bucket, key, uploadID string, opts storage.ListPartsOptions) (storage.ListPartsResult, error) {
	if err := ctx.Err(); err != nil {
		return storage.ListPartsResult{}, err
	}
	if err := b.requireBucket(bucket); err != nil {
		return storage.ListPartsResult{}, err
	}
	l, err := b.uploadLock(bucket, uploadID)
	if err != nil {
		return storage.ListPartsResult{}, err
	}
	l.RLock()
	defer l.RUnlock()
	var uploaded map[int]storage.PartInfo
	err = b.withUpload(bucket, key, uploadID, func(_, udir string, _ uploadRecord) error {
		var err error
		uploaded, err = readParts(udir)
		return err
	})
	if err != nil {
		return storage.ListPartsResult{}, err
	}
	maxParts := opts.MaxParts
	if maxParts <= 0 {
		maxParts = storage.DefaultMaxParts
	}
	all := make([]storage.PartInfo, 0, len(uploaded))
	for n, p := range uploaded {
		if n > opts.PartNumberMarker {
			all = append(all, p)
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

func (b *Backend) ListMultipartUploads(ctx context.Context, bucket string, opts storage.ListUploadsOptions) (storage.ListUploadsResult, error) {
	if err := ctx.Err(); err != nil {
		return storage.ListUploadsResult{}, err
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	dir, err := b.bucketDir(bucket)
	if err != nil {
		return storage.ListUploadsResult{}, err
	}
	entries, err := os.ReadDir(filepath.Join(dir, uploadsDir))
	if err != nil {
		return storage.ListUploadsResult{}, err
	}
	var ups []storage.Upload
	for _, e := range entries {
		var rec uploadRecord
		if err := readJSON(filepath.Join(dir, uploadsDir, e.Name(), uploadFile), &rec); err != nil {
			continue // removed while listing
		}
		if strings.HasPrefix(rec.Key, opts.Prefix) {
			ups = append(ups, storage.Upload{Key: rec.Key, UploadID: rec.UploadID, Initiated: rec.Initiated})
		}
	}
	sort.Slice(ups, func(i, j int) bool {
		if ups[i].Key != ups[j].Key {
			return ups[i].Key < ups[j].Key
		}
		if !ups[i].Initiated.Equal(ups[j].Initiated) {
			return ups[i].Initiated.Before(ups[j].Initiated)
		}
		return ups[i].UploadID < ups[j].UploadID
	})
	maxUploads := opts.MaxUploads
	if maxUploads <= 0 {
		maxUploads = storage.DefaultMaxKeys
	}
	res := storage.ListUploadsResult{Uploads: ups}
	if len(ups) > maxUploads {
		res.Uploads = ups[:maxUploads]
		res.IsTruncated = true
	}
	return res, nil
}
