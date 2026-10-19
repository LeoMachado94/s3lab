package s3api

import (
	"net/http"

	"github.com/LeoMachado94/s3lab/internal/s3api/s3err"
	"github.com/LeoMachado94/s3lab/internal/s3op"
)

// DeleteObjects handler. Owner: task T2.C.

func (h *Handler) deleteObjects(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	s3err.Write(w, r, s3err.NotImplemented)
}
