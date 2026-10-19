package s3api

import (
	"net/http"

	"github.com/LeoMachado94/s3lab/internal/s3api/s3err"
	"github.com/LeoMachado94/s3lab/internal/s3op"
)

// Service and bucket handlers. Owner: task T2.A.

func (h *Handler) listBuckets(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	s3err.Write(w, r, s3err.NotImplemented)
}

func (h *Handler) createBucket(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	s3err.Write(w, r, s3err.NotImplemented)
}

func (h *Handler) deleteBucket(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	s3err.Write(w, r, s3err.NotImplemented)
}

func (h *Handler) headBucket(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	s3err.Write(w, r, s3err.NotImplemented)
}

func (h *Handler) getBucketLocation(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	s3err.Write(w, r, s3err.NotImplemented)
}

func (h *Handler) listObjectsV1(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	s3err.Write(w, r, s3err.NotImplemented)
}

func (h *Handler) listObjectsV2(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	s3err.Write(w, r, s3err.NotImplemented)
}
