package faults

import (
	"context"
	"io"

	"github.com/LeoMachado94/s3lab/internal/storage"
)

type backend struct {
	next storage.Backend
	eng  *Engine
}

var _ storage.Backend = (*backend)(nil)

// WrapBackend decorates b so storage rules can fail or delay backend calls.
func WrapBackend(b storage.Backend, e *Engine) storage.Backend {
	return &backend{next: b, eng: e}
}

func (b *backend) inject(ctx context.Context, method, bucket, key string) error {
	rule, ok := b.eng.Match(TargetStorage, method, bucket, key)
	if !ok {
		return nil
	}
	if !wait(ctx, rule.DelayMs) {
		return ctx.Err()
	}
	if rule.Status > 0 {
		return rule.injected()
	}
	return nil
}

func (b *backend) ListBuckets(ctx context.Context) ([]storage.Bucket, error) {
	if err := b.inject(ctx, "ListBuckets", "", ""); err != nil {
		return nil, err
	}
	return b.next.ListBuckets(ctx)
}

func (b *backend) CreateBucket(ctx context.Context, bucket string) error {
	if err := b.inject(ctx, "CreateBucket", bucket, ""); err != nil {
		return err
	}
	return b.next.CreateBucket(ctx, bucket)
}

func (b *backend) DeleteBucket(ctx context.Context, bucket string) error {
	if err := b.inject(ctx, "DeleteBucket", bucket, ""); err != nil {
		return err
	}
	return b.next.DeleteBucket(ctx, bucket)
}

func (b *backend) HeadBucket(ctx context.Context, bucket string) (storage.Bucket, error) {
	if err := b.inject(ctx, "HeadBucket", bucket, ""); err != nil {
		return storage.Bucket{}, err
	}
	return b.next.HeadBucket(ctx, bucket)
}

func (b *backend) PutObject(ctx context.Context, bucket, key string, r io.Reader, opts storage.PutOptions) (storage.ObjectInfo, error) {
	if err := b.inject(ctx, "PutObject", bucket, key); err != nil {
		return storage.ObjectInfo{}, err
	}
	return b.next.PutObject(ctx, bucket, key, r, opts)
}

func (b *backend) GetObject(ctx context.Context, bucket, key string, rng *storage.ByteRange) (io.ReadCloser, storage.ObjectInfo, error) {
	if err := b.inject(ctx, "GetObject", bucket, key); err != nil {
		return nil, storage.ObjectInfo{}, err
	}
	return b.next.GetObject(ctx, bucket, key, rng)
}

func (b *backend) HeadObject(ctx context.Context, bucket, key string) (storage.ObjectInfo, error) {
	if err := b.inject(ctx, "HeadObject", bucket, key); err != nil {
		return storage.ObjectInfo{}, err
	}
	return b.next.HeadObject(ctx, bucket, key)
}

func (b *backend) DeleteObject(ctx context.Context, bucket, key string) error {
	if err := b.inject(ctx, "DeleteObject", bucket, key); err != nil {
		return err
	}
	return b.next.DeleteObject(ctx, bucket, key)
}

func (b *backend) CopyObject(ctx context.Context, src, dst storage.ObjectRef, opts storage.CopyOptions) (storage.ObjectInfo, error) {
	if err := b.inject(ctx, "CopyObject", dst.Bucket, dst.Key); err != nil {
		return storage.ObjectInfo{}, err
	}
	return b.next.CopyObject(ctx, src, dst, opts)
}

func (b *backend) ListObjects(ctx context.Context, bucket string, opts storage.ListOptions) (storage.ListResult, error) {
	if err := b.inject(ctx, "ListObjects", bucket, ""); err != nil {
		return storage.ListResult{}, err
	}
	return b.next.ListObjects(ctx, bucket, opts)
}

func (b *backend) CreateMultipartUpload(ctx context.Context, bucket, key string, meta storage.ObjectMeta) (string, error) {
	if err := b.inject(ctx, "CreateMultipartUpload", bucket, key); err != nil {
		return "", err
	}
	return b.next.CreateMultipartUpload(ctx, bucket, key, meta)
}

func (b *backend) UploadPart(ctx context.Context, bucket, key, uploadID string, partNumber int, r io.Reader, size int64) (storage.PartInfo, error) {
	if err := b.inject(ctx, "UploadPart", bucket, key); err != nil {
		return storage.PartInfo{}, err
	}
	return b.next.UploadPart(ctx, bucket, key, uploadID, partNumber, r, size)
}

func (b *backend) CompleteMultipartUpload(ctx context.Context, bucket, key, uploadID string, parts []storage.CompletePart) (storage.ObjectInfo, error) {
	if err := b.inject(ctx, "CompleteMultipartUpload", bucket, key); err != nil {
		return storage.ObjectInfo{}, err
	}
	return b.next.CompleteMultipartUpload(ctx, bucket, key, uploadID, parts)
}

func (b *backend) AbortMultipartUpload(ctx context.Context, bucket, key, uploadID string) error {
	if err := b.inject(ctx, "AbortMultipartUpload", bucket, key); err != nil {
		return err
	}
	return b.next.AbortMultipartUpload(ctx, bucket, key, uploadID)
}

func (b *backend) ListParts(ctx context.Context, bucket, key, uploadID string, opts storage.ListPartsOptions) (storage.ListPartsResult, error) {
	if err := b.inject(ctx, "ListParts", bucket, key); err != nil {
		return storage.ListPartsResult{}, err
	}
	return b.next.ListParts(ctx, bucket, key, uploadID, opts)
}

func (b *backend) ListMultipartUploads(ctx context.Context, bucket string, opts storage.ListUploadsOptions) (storage.ListUploadsResult, error) {
	if err := b.inject(ctx, "ListMultipartUploads", bucket, ""); err != nil {
		return storage.ListUploadsResult{}, err
	}
	return b.next.ListMultipartUploads(ctx, bucket, opts)
}

func (b *backend) Reset(ctx context.Context, bucket string) error {
	if err := b.inject(ctx, "Reset", bucket, ""); err != nil {
		return err
	}
	return b.next.Reset(ctx, bucket)
}
