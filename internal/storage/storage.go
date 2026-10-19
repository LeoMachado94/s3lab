// Package storage defines the persistence port used by the S3 API layer.
//
// Backends (fs, memory) implement Backend and must pass storagetest.Run.
// This package must not import net/http: protocol concerns belong to s3api.
package storage

import (
	"context"
	"io"
	"time"
)

// Backend is the storage port. All methods must be safe for concurrent use.
//
// Semantics every implementation must follow (enforced by storagetest):
//   - Bucket and key names are validated with ValidateBucketName / ValidateKey.
//   - Object operations on a missing bucket return ErrNoSuchBucket.
//   - Concurrent PutObject on the same key: last writer wins, readers never
//     observe a partially written object.
//   - DeleteObject on a missing key is not an error.
//   - ETag is the lowercase hex MD5 of the content (unquoted); multipart
//     objects use MultipartETag.
type Backend interface {
	ListBuckets(ctx context.Context) ([]Bucket, error)
	CreateBucket(ctx context.Context, bucket string) error
	// DeleteBucket returns ErrBucketNotEmpty if the bucket holds objects or
	// in-progress multipart uploads.
	DeleteBucket(ctx context.Context, bucket string) error
	HeadBucket(ctx context.Context, bucket string) (Bucket, error)

	// PutObject reads r until EOF. opts.Size is a hint (-1 if unknown).
	PutObject(ctx context.Context, bucket, key string, r io.Reader, opts PutOptions) (ObjectInfo, error)
	// GetObject returns the object body. When rng is non-nil only that slice is
	// returned; ObjectInfo.Size is always the full object size.
	GetObject(ctx context.Context, bucket, key string, rng *ByteRange) (io.ReadCloser, ObjectInfo, error)
	HeadObject(ctx context.Context, bucket, key string) (ObjectInfo, error)
	DeleteObject(ctx context.Context, bucket, key string) error
	CopyObject(ctx context.Context, src, dst ObjectRef, opts CopyOptions) (ObjectInfo, error)
	ListObjects(ctx context.Context, bucket string, opts ListOptions) (ListResult, error)

	CreateMultipartUpload(ctx context.Context, bucket, key string, meta ObjectMeta) (uploadID string, err error)
	// UploadPart replaces any previous part with the same number.
	UploadPart(ctx context.Context, bucket, key, uploadID string, partNumber int, r io.Reader, size int64) (PartInfo, error)
	CompleteMultipartUpload(ctx context.Context, bucket, key, uploadID string, parts []CompletePart) (ObjectInfo, error)
	AbortMultipartUpload(ctx context.Context, bucket, key, uploadID string) error
	ListParts(ctx context.Context, bucket, key, uploadID string, opts ListPartsOptions) (ListPartsResult, error)
	ListMultipartUploads(ctx context.Context, bucket string, opts ListUploadsOptions) (ListUploadsResult, error)

	// Reset deletes every bucket, object and upload. A non-empty bucket limits
	// the reset to emptying that bucket (the bucket itself is kept).
	Reset(ctx context.Context, bucket string) error
}

type Bucket struct {
	Name      string
	CreatedAt time.Time
}

type ObjectRef struct {
	Bucket string
	Key    string
}

// ObjectMeta is the metadata supplied by the client and returned verbatim.
type ObjectMeta struct {
	ContentType        string
	ContentEncoding    string
	ContentDisposition string
	ContentLanguage    string
	CacheControl       string
	Expires            string
	// User holds x-amz-meta-* values keyed by lowercase name without prefix.
	User map[string]string
}

type ObjectInfo struct {
	Bucket       string
	Key          string
	Size         int64
	ETag         string
	LastModified time.Time
	Meta         ObjectMeta
	// PartsCount is > 0 for objects created through multipart upload.
	PartsCount int
}

type PutOptions struct {
	Size int64
	Meta ObjectMeta
}

type CopyOptions struct {
	// ReplaceMeta selects the REPLACE metadata directive; otherwise source
	// metadata is copied (COPY directive).
	ReplaceMeta bool
	Meta        ObjectMeta
	// Range, when set, copies only a slice (used by UploadPartCopy callers).
	Range *ByteRange
}

type ListOptions struct {
	Prefix    string
	Delimiter string
	// StartAfter is exclusive; it carries marker, start-after or a decoded
	// continuation token.
	StartAfter string
	// MaxKeys <= 0 means DefaultMaxKeys. Keys and common prefixes both count.
	MaxKeys int
}

type ListResult struct {
	Objects        []ObjectInfo
	CommonPrefixes []string
	IsTruncated    bool
	// NextStartAfter is the last key or common prefix returned when truncated.
	NextStartAfter string
}

type PartInfo struct {
	PartNumber   int
	ETag         string
	Size         int64
	LastModified time.Time
}

type CompletePart struct {
	PartNumber int
	ETag       string
}

type ListPartsOptions struct {
	PartNumberMarker int
	MaxParts         int
}

type ListPartsResult struct {
	Parts                []PartInfo
	IsTruncated          bool
	NextPartNumberMarker int
}

type Upload struct {
	Key       string
	UploadID  string
	Initiated time.Time
}

type ListUploadsOptions struct {
	Prefix     string
	MaxUploads int
}

type ListUploadsResult struct {
	Uploads     []Upload
	IsTruncated bool
}

const (
	DefaultMaxKeys  = 1000
	DefaultMaxParts = 1000
	MaxPartNumber   = 10000
	// MinPartSize applies to every part except the last on completion.
	MinPartSize = 5 << 20
	MaxKeyBytes = 1024
)
