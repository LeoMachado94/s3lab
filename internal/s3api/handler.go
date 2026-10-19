// Package s3api is the S3 HTTP transport: it parses requests, calls the
// storage port and renders S3 XML responses. Handlers are grouped per file:
// bucket.go, object.go, multipart.go, batch.go, cors.go.
package s3api

import (
	"log/slog"
	"net/http"

	"github.com/LeoMachado94/s3lab/internal/s3api/s3err"
	"github.com/LeoMachado94/s3lab/internal/s3op"
	"github.com/LeoMachado94/s3lab/internal/storage"
)

type Handler struct {
	backend       storage.Backend
	region        string
	resolver      s3op.Resolver
	cors          CORSConfig
	log           *slog.Logger
	maxObjectSize int64
	routes        map[s3op.Op]opHandler
}

type opHandler func(w http.ResponseWriter, r *http.Request, info *s3op.Info)

type Option func(*Handler)

func WithRegion(region string) Option { return func(h *Handler) { h.region = region } }

// WithVirtualHostDomain enables "<bucket>.<domain>" addressing when the
// handler resolves requests itself (no s3op middleware upstream).
func WithVirtualHostDomain(domain string) Option {
	return func(h *Handler) { h.resolver = s3op.Resolver{Domain: domain} }
}

func WithCORS(cfg CORSConfig) Option { return func(h *Handler) { h.cors = cfg } }

func WithLogger(l *slog.Logger) Option { return func(h *Handler) { h.log = l } }

// WithMaxObjectSize caps single PUT / part bodies (default 5 GiB).
func WithMaxObjectSize(n int64) Option { return func(h *Handler) { h.maxObjectSize = n } }

func New(backend storage.Backend, opts ...Option) *Handler {
	h := &Handler{
		backend:       backend,
		region:        "us-east-1",
		cors:          DefaultCORS(),
		log:           slog.Default(),
		maxObjectSize: 5 << 30,
	}
	for _, o := range opts {
		o(h)
	}
	h.routes = map[s3op.Op]opHandler{
		s3op.ListBuckets:             h.listBuckets,
		s3op.CreateBucket:            h.createBucket,
		s3op.DeleteBucket:            h.deleteBucket,
		s3op.HeadBucket:              h.headBucket,
		s3op.GetBucketLocation:       h.getBucketLocation,
		s3op.ListObjects:             h.listObjectsV1,
		s3op.ListObjectsV2:           h.listObjectsV2,
		s3op.PutObject:               h.putObject,
		s3op.GetObject:               h.getObject,
		s3op.HeadObject:              h.headObject,
		s3op.DeleteObject:            h.deleteObject,
		s3op.CopyObject:              h.copyObject,
		s3op.GetObjectAcl:            h.getObjectACL,
		s3op.PutObjectAcl:            h.putObjectACL,
		s3op.CreateMultipartUpload:   h.createMultipartUpload,
		s3op.UploadPart:              h.uploadPart,
		s3op.UploadPartCopy:          h.uploadPartCopy,
		s3op.CompleteMultipartUpload: h.completeMultipartUpload,
		s3op.AbortMultipartUpload:    h.abortMultipartUpload,
		s3op.ListParts:               h.listParts,
		s3op.ListMultipartUploads:    h.listMultipartUploads,
		s3op.DeleteObjects:           h.deleteObjects,
		s3op.CORSPreflight:           h.corsPreflight,
	}
	return h
}

// Resolver exposes the resolver so the server can install the same
// addressing rules as upstream middleware.
func (h *Handler) Resolver() s3op.Resolver { return h.resolver }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r, info := h.resolver.Ensure(r)
	fn, ok := h.routes[info.Op]
	if !ok {
		s3err.Write(w, r, s3err.NotImplemented)
		return
	}
	fn(w, r, info)
}
