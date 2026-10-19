package s3api

import (
	"net/http"

	"github.com/LeoMachado94/s3lab/internal/s3api/s3err"
	"github.com/LeoMachado94/s3lab/internal/s3op"
)

// Multipart upload handlers. Owner: task T2.C.

func (h *Handler) createMultipartUpload(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	s3err.Write(w, r, s3err.NotImplemented)
}

func (h *Handler) uploadPart(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	s3err.Write(w, r, s3err.NotImplemented)
}

func (h *Handler) uploadPartCopy(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	s3err.Write(w, r, s3err.NotImplemented)
}

func (h *Handler) completeMultipartUpload(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	s3err.Write(w, r, s3err.NotImplemented)
}

func (h *Handler) abortMultipartUpload(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	s3err.Write(w, r, s3err.NotImplemented)
}

func (h *Handler) listParts(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	s3err.Write(w, r, s3err.NotImplemented)
}

func (h *Handler) listMultipartUploads(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	s3err.Write(w, r, s3err.NotImplemented)
}
