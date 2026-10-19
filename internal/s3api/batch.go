package s3api

import (
	"bytes"
	"crypto/md5"
	"crypto/subtle"
	"encoding/base64"
	"io"
	"net/http"

	"github.com/LeoMachado94/s3lab/internal/s3api/s3err"
	"github.com/LeoMachado94/s3lab/internal/s3op"
)

const batchMaxObjects = 1000

// deleteObjects does not require Content-MD5 or x-amz-checksum-* (SDKs
// differ); a Content-MD5 that is present is verified.
func (h *Handler) deleteObjects(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	ctx := r.Context()
	if _, err := h.backend.HeadBucket(ctx, info.Bucket); err != nil {
		s3err.Write(w, r, err)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20+1))
	if err != nil {
		s3err.Write(w, r, err)
		return
	}
	if len(raw) > 1<<20 {
		s3err.Write(w, r, s3err.MalformedXML)
		return
	}
	if v := r.Header.Get("Content-MD5"); v != "" {
		want, dErr := base64.StdEncoding.DecodeString(v)
		if dErr != nil || len(want) != md5.Size {
			s3err.Write(w, r, s3err.InvalidDigest)
			return
		}
		got := md5.Sum(raw)
		if subtle.ConstantTimeCompare(got[:], want) != 1 {
			s3err.Write(w, r, s3err.BadDigest)
			return
		}
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var req DeleteRequest
	if err := decodeXMLBody(r, &req); err != nil {
		s3err.Write(w, r, err)
		return
	}
	if len(req.Objects) == 0 || len(req.Objects) > batchMaxObjects {
		s3err.Write(w, r, s3err.MalformedXML)
		return
	}

	out := DeleteResult{Xmlns: s3Namespace}
	for _, o := range req.Objects {
		if err := h.backend.DeleteObject(ctx, info.Bucket, o.Key); err != nil {
			e := s3err.From(err)
			out.Errors = append(out.Errors, DeleteError{Key: o.Key, Code: e.Code, Message: e.Message})
			continue
		}
		if !req.Quiet {
			out.Deleted = append(out.Deleted, DeletedObject{Key: o.Key})
		}
	}
	writeXML(w, r, http.StatusOK, out)
}
