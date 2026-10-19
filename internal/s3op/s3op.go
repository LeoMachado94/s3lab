// Package s3op identifies the S3 operation of an HTTP request and carries
// per-request state through the middleware chain.
package s3op

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
)

type Op string

const (
	ListBuckets             Op = "ListBuckets"
	CreateBucket            Op = "CreateBucket"
	DeleteBucket            Op = "DeleteBucket"
	HeadBucket              Op = "HeadBucket"
	GetBucketLocation       Op = "GetBucketLocation"
	ListObjects             Op = "ListObjects"
	ListObjectsV2           Op = "ListObjectsV2"
	ListMultipartUploads    Op = "ListMultipartUploads"
	DeleteObjects           Op = "DeleteObjects"
	PutObject               Op = "PutObject"
	GetObject               Op = "GetObject"
	HeadObject              Op = "HeadObject"
	DeleteObject            Op = "DeleteObject"
	CopyObject              Op = "CopyObject"
	GetObjectAcl            Op = "GetObjectAcl"
	PutObjectAcl            Op = "PutObjectAcl"
	CreateMultipartUpload   Op = "CreateMultipartUpload"
	UploadPart              Op = "UploadPart"
	UploadPartCopy          Op = "UploadPartCopy"
	CompleteMultipartUpload Op = "CompleteMultipartUpload"
	AbortMultipartUpload    Op = "AbortMultipartUpload"
	ListParts               Op = "ListParts"
	CORSPreflight           Op = "CORSPreflight"
	// Unsupported marks a recognised S3 request we do not implement.
	Unsupported Op = "Unsupported"
)

// AuthResult describes how the request was authenticated.
type AuthResult string

const (
	AuthNone      AuthResult = ""
	AuthHeader    AuthResult = "header"
	AuthPresigned AuthResult = "presigned"
	AuthFailed    AuthResult = "failed"
)

// Info is the mutable per-request state shared by middlewares and handlers.
// It is only touched by the goroutine serving the request.
type Info struct {
	RequestID string
	Op        Op
	Bucket    string
	Key       string
	// Set by later layers:
	AccessKey string
	Auth      AuthResult
	ErrorCode string
	FaultID   string
}

type ctxKey struct{}

func WithInfo(ctx context.Context, info *Info) context.Context {
	return context.WithValue(ctx, ctxKey{}, info)
}

// FromContext returns the request Info or nil.
func FromContext(ctx context.Context) *Info {
	info, _ := ctx.Value(ctxKey{}).(*Info)
	return info
}

// Resolver turns requests into Info. Domain enables virtual-host style
// addressing ("<bucket>.<domain>"); empty means path-style only.
type Resolver struct {
	Domain string
}

// Middleware resolves the operation once, sets x-amz-request-id and stores
// Info in the request context.
func (rv Resolver) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := rv.Resolve(r)
		w.Header().Set("x-amz-request-id", info.RequestID)
		w.Header().Set("Server", "s3lab")
		next.ServeHTTP(w, r.WithContext(WithInfo(r.Context(), info)))
	})
}

// Ensure returns the Info in r's context, resolving it if missing (handy for
// handler tests that bypass the middleware chain).
func (rv Resolver) Ensure(r *http.Request) (*http.Request, *Info) {
	if info := FromContext(r.Context()); info != nil {
		return r, info
	}
	info := rv.Resolve(r)
	return r.WithContext(WithInfo(r.Context(), info)), info
}

func (rv Resolver) Resolve(r *http.Request) *Info {
	bucket, key := rv.split(r)
	return &Info{
		RequestID: newRequestID(),
		Op:        classify(r, bucket, key),
		Bucket:    bucket,
		Key:       key,
	}
}

func (rv Resolver) split(r *http.Request) (bucket, key string) {
	path := r.URL.Path
	if rv.Domain != "" {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		suffix := "." + rv.Domain
		if strings.HasSuffix(host, suffix) && len(host) > len(suffix) {
			return host[:len(host)-len(suffix)], strings.TrimPrefix(path, "/")
		}
	}
	path = strings.TrimPrefix(path, "/")
	if path == "" {
		return "", ""
	}
	bucket, key, _ = strings.Cut(path, "/")
	return bucket, key
}

// Bucket subresources we recognise but do not implement.
var unsupportedBucketQueries = []string{
	"acl", "analytics", "cors", "encryption", "intelligent-tiering", "inventory",
	"lifecycle", "logging", "metrics", "notification", "object-lock", "ownershipControls",
	"policy", "policyStatus", "publicAccessBlock", "replication", "requestPayment",
	"tagging", "versioning", "versions", "website", "accelerate",
}

var unsupportedObjectQueries = []string{
	"tagging", "retention", "legal-hold", "torrent", "restore", "select", "attributes", "versionId",
}

func classify(r *http.Request, bucket, key string) Op {
	q := r.URL.Query()
	if r.Method == http.MethodOptions {
		return CORSPreflight
	}
	if bucket == "" {
		if r.Method == http.MethodGet {
			return ListBuckets
		}
		return Unsupported
	}
	if key == "" {
		for _, sub := range unsupportedBucketQueries {
			if q.Has(sub) {
				return Unsupported
			}
		}
		switch r.Method {
		case http.MethodPut:
			return CreateBucket
		case http.MethodDelete:
			return DeleteBucket
		case http.MethodHead:
			return HeadBucket
		case http.MethodPost:
			if q.Has("delete") {
				return DeleteObjects
			}
		case http.MethodGet:
			switch {
			case q.Has("location"):
				return GetBucketLocation
			case q.Has("uploads"):
				return ListMultipartUploads
			case q.Get("list-type") == "2":
				return ListObjectsV2
			default:
				return ListObjects
			}
		}
		return Unsupported
	}

	for _, sub := range unsupportedObjectQueries {
		if q.Has(sub) {
			return Unsupported
		}
	}
	copySource := r.Header.Get("x-amz-copy-source") != ""
	switch r.Method {
	case http.MethodPut:
		switch {
		case q.Has("uploadId") && q.Has("partNumber"):
			if copySource {
				return UploadPartCopy
			}
			return UploadPart
		case q.Has("acl"):
			return PutObjectAcl
		case copySource:
			return CopyObject
		default:
			return PutObject
		}
	case http.MethodGet:
		switch {
		case q.Has("uploadId"):
			return ListParts
		case q.Has("acl"):
			return GetObjectAcl
		default:
			return GetObject
		}
	case http.MethodHead:
		return HeadObject
	case http.MethodDelete:
		if q.Has("uploadId") {
			return AbortMultipartUpload
		}
		return DeleteObject
	case http.MethodPost:
		switch {
		case q.Has("uploads"):
			return CreateMultipartUpload
		case q.Has("uploadId"):
			return CompleteMultipartUpload
		}
	}
	return Unsupported
}

func newRequestID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return strings.ToUpper(hex.EncodeToString(b[:]))
}
