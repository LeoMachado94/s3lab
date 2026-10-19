package s3api

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/LeoMachado94/s3lab/internal/s3api/s3err"
	"github.com/LeoMachado94/s3lab/internal/storage"
)

// Shared helpers for every handler file. Group-specific helpers live in their
// own file, prefixed with the group name (bucketX, objectX, mpX, batchX).

func writeXML(w http.ResponseWriter, r *http.Request, status int, v any) {
	out, err := xml.Marshal(v)
	if err != nil {
		s3err.Write(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("Content-Length", strconv.Itoa(len(xml.Header)+len(out)))
	w.WriteHeader(status)
	_, _ = w.Write([]byte(xml.Header))
	_, _ = w.Write(out)
}

func writeEmpty(w http.ResponseWriter, status int) {
	w.WriteHeader(status)
}

// decodeXMLBody parses a small XML request body (max 1 MiB).
func decodeXMLBody(r *http.Request, v any) error {
	dec := xml.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		return s3err.MalformedXML
	}
	return nil
}

func quoteETag(etag string) string { return `"` + etag + `"` }

func unquoteETag(etag string) string { return strings.Trim(strings.TrimSpace(etag), `"`) }

// metaFromHeaders extracts system and x-amz-meta-* metadata from a request.
func metaFromHeaders(h http.Header) storage.ObjectMeta {
	m := storage.ObjectMeta{
		ContentType:        h.Get("Content-Type"),
		ContentEncoding:    stripAWSChunked(h.Get("Content-Encoding")),
		ContentDisposition: h.Get("Content-Disposition"),
		ContentLanguage:    h.Get("Content-Language"),
		CacheControl:       h.Get("Cache-Control"),
		Expires:            h.Get("Expires"),
	}
	for name, vals := range h {
		lower := strings.ToLower(name)
		if !strings.HasPrefix(lower, "x-amz-meta-") || len(vals) == 0 {
			continue
		}
		if m.User == nil {
			m.User = map[string]string{}
		}
		m.User[strings.TrimPrefix(lower, "x-amz-meta-")] = strings.Join(vals, ",")
	}
	if m.ContentType == "" {
		m.ContentType = "binary/octet-stream"
	}
	return m
}

// aws-chunked is a transfer detail and must not be persisted.
func stripAWSChunked(enc string) string {
	parts := strings.Split(enc, ",")
	kept := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" && p != "aws-chunked" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, ",")
}

// setObjectHeaders writes the standard response headers for an object.
func setObjectHeaders(w http.ResponseWriter, info storage.ObjectInfo) {
	h := w.Header()
	h.Set("ETag", quoteETag(info.ETag))
	h.Set("Last-Modified", timeHTTP(info.LastModified))
	h.Set("Accept-Ranges", "bytes")
	set := func(k, v string) {
		if v != "" {
			h.Set(k, v)
		}
	}
	set("Content-Type", info.Meta.ContentType)
	set("Content-Encoding", info.Meta.ContentEncoding)
	set("Content-Disposition", info.Meta.ContentDisposition)
	set("Content-Language", info.Meta.ContentLanguage)
	set("Cache-Control", info.Meta.CacheControl)
	set("Expires", info.Meta.Expires)
	for k, v := range info.Meta.User {
		h["x-amz-meta-"+k] = []string{v}
	}
	if info.PartsCount > 0 {
		h.Set("x-amz-mp-parts-count", strconv.Itoa(info.PartsCount))
	}
}

// parseRange parses a single "bytes=a-b" range. Multi-range requests are
// rejected, as S3 does.
func parseRange(v string) (*storage.ByteRange, error) {
	if v == "" {
		return nil, nil
	}
	spec, ok := strings.CutPrefix(v, "bytes=")
	if !ok || strings.Contains(spec, ",") {
		return nil, storage.ErrInvalidRange
	}
	a, b, ok := strings.Cut(spec, "-")
	if !ok {
		return nil, storage.ErrInvalidRange
	}
	if a == "" {
		n, err := strconv.ParseInt(b, 10, 64)
		if err != nil || n <= 0 {
			return nil, storage.ErrInvalidRange
		}
		return &storage.ByteRange{Start: -n, End: -1}, nil
	}
	start, err := strconv.ParseInt(a, 10, 64)
	if err != nil || start < 0 {
		return nil, storage.ErrInvalidRange
	}
	end := int64(-1)
	if b != "" {
		if end, err = strconv.ParseInt(b, 10, 64); err != nil || end < start {
			return nil, storage.ErrInvalidRange
		}
	}
	return &storage.ByteRange{Start: start, End: end}, nil
}

// contentRange renders the Content-Range header for a resolved range.
func contentRange(offset, length, size int64) string {
	return fmt.Sprintf("bytes %d-%d/%d", offset, offset+length-1, size)
}

// parseCopySource parses x-amz-copy-source ("/bucket/key" or "bucket/key",
// URL-encoded, optional "?versionId=").
func parseCopySource(v string) (storage.ObjectRef, error) {
	v, _, _ = strings.Cut(v, "?")
	dec, err := url.PathUnescape(v)
	if err != nil {
		return storage.ObjectRef{}, s3err.InvalidArgument.WithMessage("Invalid copy source encoding")
	}
	bucket, key, ok := strings.Cut(strings.TrimPrefix(dec, "/"), "/")
	if !ok || bucket == "" || key == "" {
		return storage.ObjectRef{}, s3err.InvalidArgument.WithMessage("Copy Source must mention the source bucket and key: sourcebucket/sourcekey")
	}
	return storage.ObjectRef{Bucket: bucket, Key: key}, nil
}

// queryInt reads an integer query param with a default and bounds.
func queryInt(r *http.Request, name string, def, maxV int) (int, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, s3err.InvalidArgument.WithMessage(name + " must be a non-negative integer")
	}
	if maxV > 0 && n > maxV {
		n = maxV
	}
	return n, nil
}
