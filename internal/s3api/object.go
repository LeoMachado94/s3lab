package s3api

import (
	"net/http"

	"github.com/LeoMachado94/s3lab/internal/s3api/s3err"
	"github.com/LeoMachado94/s3lab/internal/s3op"
)

// Object handlers. Owner: task T2.B.

func (h *Handler) putObject(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	s3err.Write(w, r, s3err.NotImplemented)
}

func (h *Handler) getObject(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	s3err.Write(w, r, s3err.NotImplemented)
}

func (h *Handler) headObject(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	s3err.Write(w, r, s3err.NotImplemented)
}

func (h *Handler) deleteObject(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	s3err.Write(w, r, s3err.NotImplemented)
}

func (h *Handler) copyObject(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	s3err.Write(w, r, s3err.NotImplemented)
}

func (h *Handler) getObjectACL(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	s3err.Write(w, r, s3err.NotImplemented)
}

func (h *Handler) putObjectACL(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	s3err.Write(w, r, s3err.NotImplemented)
}
