package console

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/LeoMachado94/s3lab/internal/storage"
)

const (
	defaultListKeys = 200
	maxListKeys     = 1000

	forceDeleteAttempts = 3
)

type bucketView struct {
	Name      string `json:"name"`
	CreatedAt string `json:"createdAt"`
}

type objectSummary struct {
	Key          string `json:"key"`
	Size         int64  `json:"size"`
	ETag         string `json:"etag"`
	LastModified string `json:"lastModified"`
	ContentType  string `json:"contentType"`
}

type objectDetail struct {
	Key          string            `json:"key"`
	Size         int64             `json:"size"`
	ETag         string            `json:"etag"`
	LastModified string            `json:"lastModified"`
	PartsCount   int               `json:"partsCount"`
	ContentType  string            `json:"contentType"`
	Headers      map[string]string `json:"headers"`
	Metadata     map[string]string `json:"metadata"`
}

func contentType(m storage.ObjectMeta) string {
	if m.ContentType == "" {
		return "application/octet-stream"
	}
	return m.ContentType
}

func summary(o storage.ObjectInfo) objectSummary {
	return objectSummary{o.Key, o.Size, o.ETag, ts(o.LastModified), contentType(o.Meta)}
}

func detail(o storage.ObjectInfo) objectDetail {
	headers := map[string]string{}
	for k, v := range map[string]string{
		"Content-Encoding":    o.Meta.ContentEncoding,
		"Content-Disposition": o.Meta.ContentDisposition,
		"Content-Language":    o.Meta.ContentLanguage,
		"Cache-Control":       o.Meta.CacheControl,
		"Expires":             o.Meta.Expires,
	} {
		if v != "" {
			headers[k] = v
		}
	}
	meta := map[string]string{}
	for k, v := range o.Meta.User {
		meta[k] = v
	}
	return objectDetail{o.Key, o.Size, o.ETag, ts(o.LastModified), o.PartsCount, contentType(o.Meta), headers, meta}
}

func (s *server) listBuckets(w http.ResponseWriter, r *http.Request) {
	bs, err := s.Backend.ListBuckets(r.Context())
	if err != nil {
		s.storageError(w, err)
		return
	}
	out := make([]bucketView, 0, len(bs))
	for _, b := range bs {
		out = append(out, bucketView{b.Name, ts(b.CreatedAt)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"buckets": out})
}

func (s *server) createBucket(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if err := s.Backend.CreateBucket(r.Context(), in.Name); err != nil {
		s.storageError(w, err)
		return
	}
	b, err := s.Backend.HeadBucket(r.Context(), in.Name)
	if err != nil {
		s.storageError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, bucketView{b.Name, ts(b.CreatedAt)})
}

func (s *server) deleteBucket(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue("bucket")
	attempts := 1
	if r.URL.Query().Get("force") == "true" {
		s.mu.Lock()
		defer s.mu.Unlock()
		attempts = forceDeleteAttempts
	}
	for i := 1; ; i++ {
		if attempts > 1 {
			if err := s.Backend.Reset(r.Context(), bucket); err != nil {
				s.storageError(w, err)
				return
			}
		}
		err := s.Backend.DeleteBucket(r.Context(), bucket)
		if err == nil {
			break
		}
		if !errors.Is(err, storage.ErrBucketNotEmpty) || i >= attempts {
			s.storageError(w, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) listObjects(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	maxKeys := defaultListKeys
	if v := q.Get("maxKeys"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "BadRequest", "invalid maxKeys")
			return
		}
		maxKeys = min(n, maxListKeys)
	}
	delimiter := "/"
	if q.Has("delimiter") {
		delimiter = q.Get("delimiter")
	}
	res, err := s.Backend.ListObjects(r.Context(), r.PathValue("bucket"), storage.ListOptions{
		Prefix: q.Get("prefix"), Delimiter: delimiter, StartAfter: q.Get("startAfter"), MaxKeys: maxKeys,
	})
	if err != nil {
		s.storageError(w, err)
		return
	}
	objs := make([]objectSummary, 0, len(res.Objects))
	for _, o := range res.Objects {
		objs = append(objs, summary(o))
	}
	prefixes := res.CommonPrefixes
	if prefixes == nil {
		prefixes = []string{}
	}
	next := ""
	if res.IsTruncated {
		next = res.NextStartAfter
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"objects": objs, "prefixes": prefixes, "isTruncated": res.IsTruncated, "nextStartAfter": next,
	})
}

func (s *server) deletePrefix(w http.ResponseWriter, r *http.Request) {
	bucket, prefix := r.PathValue("bucket"), r.URL.Query().Get("prefix")
	deleted, after := 0, ""
	for {
		res, err := s.Backend.ListObjects(r.Context(), bucket, storage.ListOptions{
			Prefix: prefix, StartAfter: after, MaxKeys: maxListKeys,
		})
		if err != nil {
			s.storageError(w, err)
			return
		}
		for _, o := range res.Objects {
			if err := s.Backend.DeleteObject(r.Context(), bucket, o.Key); err != nil {
				s.storageError(w, err)
				return
			}
			deleted++
		}
		if !res.IsTruncated {
			break
		}
		after = res.NextStartAfter
	}
	writeJSON(w, http.StatusOK, map[string]int{"deleted": deleted})
}

func keyParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := r.URL.Query().Get("key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "BadRequest", "missing key parameter")
		return "", false
	}
	return key, true
}

func (s *server) getObject(w http.ResponseWriter, r *http.Request) {
	key, ok := keyParam(w, r)
	if !ok {
		return
	}
	info, err := s.Backend.HeadObject(r.Context(), r.PathValue("bucket"), key)
	if err != nil {
		s.storageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail(info))
}

func (s *server) putObject(w http.ResponseWriter, r *http.Request) {
	key, ok := keyParam(w, r)
	if !ok {
		return
	}
	meta := storage.ObjectMeta{ContentType: r.Header.Get("Content-Type"), User: map[string]string{}}
	if meta.ContentType == "" {
		meta.ContentType = "application/octet-stream"
	}
	const prefix = "x-amz-meta-"
	for name, vals := range r.Header {
		if lower := strings.ToLower(name); strings.HasPrefix(lower, prefix) && len(vals) > 0 {
			meta.User[strings.TrimPrefix(lower, prefix)] = strings.Join(vals, ",")
		}
	}
	if r.ContentLength > s.MaxObjectSize {
		writeTooLarge(w, s.MaxObjectSize)
		return
	}
	body := http.MaxBytesReader(w, r.Body, s.MaxObjectSize)
	info, err := s.Backend.PutObject(r.Context(), r.PathValue("bucket"), key, body,
		storage.PutOptions{Size: r.ContentLength, Meta: meta})
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeTooLarge(w, s.MaxObjectSize)
			return
		}
		s.storageError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, detail(info))
}

func (s *server) deleteObject(w http.ResponseWriter, r *http.Request) {
	key, ok := keyParam(w, r)
	if !ok {
		return
	}
	if err := s.Backend.DeleteObject(r.Context(), r.PathValue("bucket"), key); err != nil {
		s.storageError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) download(w http.ResponseWriter, r *http.Request) {
	key, ok := keyParam(w, r)
	if !ok {
		return
	}
	body, info, err := s.Backend.GetObject(r.Context(), r.PathValue("bucket"), key, nil)
	if err != nil {
		s.storageError(w, err)
		return
	}
	defer func() { _ = body.Close() }()
	inline := r.URL.Query().Get("inline") == "1"
	h := w.Header()
	h.Set("Content-Type", contentType(info.Meta))
	h.Set("Content-Length", strconv.FormatInt(info.Size, 10))
	h.Set("ETag", `"`+info.ETag+`"`)
	h.Set("Last-Modified", info.LastModified.UTC().Format(http.TimeFormat))
	h.Set("X-Content-Type-Options", "nosniff")
	disposition := "attachment"
	if inline {
		disposition = "inline"
		// Uploaded HTML/SVG must not run scripts in the console origin.
		h.Set("Content-Security-Policy", "sandbox")
	}
	h.Set("Content-Disposition", contentDisposition(disposition, path.Base(key)))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, body)
}

func contentDisposition(kind, name string) string {
	var fallback strings.Builder
	ascii := true
	for _, r := range name {
		switch {
		case r < 0x20 || r == 0x7f || r == '"' || r == '\\' || r == '%':
			fallback.WriteByte('_')
			ascii = false
		case r > 0x7e:
			fallback.WriteByte('_')
			ascii = false
		default:
			fallback.WriteRune(r)
		}
	}
	out := fmt.Sprintf(`%s; filename="%s"`, kind, fallback.String())
	if !ascii && utf8.ValidString(name) {
		out += "; filename*=UTF-8''" + rfc5987(name)
	}
	return out
}

func rfc5987(s string) string {
	const attrChars = "!#$&+-.^_`|~"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte(attrChars, c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func writeTooLarge(w http.ResponseWriter, limit int64) {
	writeError(w, http.StatusRequestEntityTooLarge, "EntityTooLarge",
		fmt.Sprintf("object exceeds the console upload limit of %d bytes", limit))
}
