package s3api

import (
	"bytes"
	"crypto/md5"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"hash"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/LeoMachado94/s3lab/internal/s3api/s3err"
	"github.com/LeoMachado94/s3lab/internal/s3op"
	"github.com/LeoMachado94/s3lab/internal/storage"
)

// objectBodyReader enforces the size limit, detects short bodies and verifies
// Content-MD5 at EOF. Errors are sticky so backends abort before committing.
type objectBodyReader struct {
	r        io.Reader
	expected int64
	max      int64
	n        int64
	md5      hash.Hash
	wantMD5  []byte
	err      error
}

func (o *objectBodyReader) Read(p []byte) (int, error) {
	if o.err != nil {
		return 0, o.err
	}
	n, err := o.r.Read(p)
	o.n += int64(n)
	if o.max >= 0 && o.n > o.max {
		o.err = s3err.EntityTooLarge
		return 0, o.err
	}
	if o.md5 != nil && n > 0 {
		o.md5.Write(p[:n])
	}
	switch {
	case err == io.EOF:
		switch {
		case o.expected >= 0 && o.n < o.expected:
			o.err = s3err.IncompleteBody
		case o.md5 != nil && !bytes.Equal(o.md5.Sum(nil), o.wantMD5):
			o.err = s3err.BadDigest
		default:
			return n, io.EOF
		}
		return n, o.err
	case errors.Is(err, io.ErrUnexpectedEOF):
		o.err = s3err.IncompleteBody
		return n, o.err
	case err != nil:
		o.err = err
		return n, err
	}
	return n, nil
}

func objectIsStreaming(r *http.Request) bool {
	return strings.HasPrefix(r.Header.Get("x-amz-content-sha256"), "STREAMING-") ||
		r.Header.Get("x-amz-decoded-content-length") != ""
}

// objectBodySize returns the payload size (-1 if unknown) after validating it
// against the configured maximum.
func (h *Handler) objectBodySize(r *http.Request) (int64, error) {
	size := r.ContentLength
	if v := r.Header.Get("x-amz-decoded-content-length"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			return 0, s3err.InvalidArgument.WithMessage("Invalid x-amz-decoded-content-length")
		}
		size = n
	} else if size < 0 && !objectIsStreaming(r) {
		return 0, s3err.MissingContentLength
	}
	if size < 0 {
		size = -1
	}
	if size > h.maxObjectSize {
		return 0, s3err.EntityTooLarge
	}
	return size, nil
}

func objectParseMD5(v string) ([]byte, error) {
	if v == "" {
		return nil, nil
	}
	sum, err := base64.StdEncoding.DecodeString(v)
	if err != nil || len(sum) != md5.Size {
		return nil, s3err.InvalidDigest
	}
	return sum, nil
}

func (h *Handler) putObject(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	size, err := h.objectBodySize(r)
	if err != nil {
		s3err.Write(w, r, err)
		return
	}
	want, err := objectParseMD5(r.Header.Get("Content-MD5"))
	if err != nil {
		s3err.Write(w, r, err)
		return
	}
	body := &objectBodyReader{r: r.Body, expected: size, max: h.maxObjectSize}
	if want != nil {
		body.md5, body.wantMD5 = md5.New(), want
	}
	obj, err := h.backend.PutObject(r.Context(), info.Bucket, info.Key, body, storage.PutOptions{
		Size: size,
		Meta: metaFromHeaders(r.Header),
	})
	if err != nil {
		s3err.Write(w, r, err)
		return
	}
	w.Header().Set("ETag", quoteETag(obj.ETag))
	writeEmpty(w, http.StatusOK)
}

type objectCondResult int

const (
	objectCondOK objectCondResult = iota
	objectCondNotModified
	objectCondFailed
)

type objectConds struct {
	match, noneMatch, modifiedSince, unmodifiedSince string
}

func objectETagMatches(header, etag string) bool {
	for _, p := range strings.Split(header, ",") {
		p = strings.TrimPrefix(strings.TrimSpace(p), "W/")
		if p == "*" || unquoteETag(p) == etag {
			return true
		}
	}
	return false
}

// objectEval applies the AWS precedence rules: a true If-Match overrides
// If-Unmodified-Since, a false If-None-Match overrides If-Modified-Since.
func objectEval(c objectConds, obj storage.ObjectInfo) objectCondResult {
	lm := obj.LastModified.Truncate(time.Second)
	if c.match != "" {
		if !objectETagMatches(c.match, obj.ETag) {
			return objectCondFailed
		}
	} else if t, err := http.ParseTime(c.unmodifiedSince); err == nil && lm.After(t) {
		return objectCondFailed
	}
	if c.noneMatch != "" {
		if objectETagMatches(c.noneMatch, obj.ETag) {
			return objectCondNotModified
		}
	} else if t, err := http.ParseTime(c.modifiedSince); err == nil && !t.After(time.Now()) && !lm.After(t) {
		return objectCondNotModified
	}
	return objectCondOK
}

func objectReadConds(r *http.Request) objectConds {
	return objectConds{
		match:           r.Header.Get("If-Match"),
		noneMatch:       r.Header.Get("If-None-Match"),
		modifiedSince:   r.Header.Get("If-Modified-Since"),
		unmodifiedSince: r.Header.Get("If-Unmodified-Since"),
	}
}

var objectResponseOverrides = map[string]string{
	"response-content-type":        "Content-Type",
	"response-content-language":    "Content-Language",
	"response-expires":             "Expires",
	"response-cache-control":       "Cache-Control",
	"response-content-disposition": "Content-Disposition",
	"response-content-encoding":    "Content-Encoding",
}

// objectPrepare evaluates conditions and range against the current object.
// It returns done=true when a response (304/error) was already written.
func (h *Handler) objectPrepare(w http.ResponseWriter, r *http.Request, info *s3op.Info) (rng *storage.ByteRange, done bool) {
	head, err := h.backend.HeadObject(r.Context(), info.Bucket, info.Key)
	if err != nil {
		s3err.Write(w, r, err)
		return nil, true
	}
	switch objectEval(objectReadConds(r), head) {
	case objectCondFailed:
		s3err.Write(w, r, s3err.PreconditionFailed)
		return nil, true
	case objectCondNotModified:
		w.Header().Set("ETag", quoteETag(head.ETag))
		w.Header().Set("Last-Modified", timeHTTP(head.LastModified))
		writeEmpty(w, http.StatusNotModified)
		return nil, true
	}
	// Like S3 (RFC 9110), a syntactically invalid or multi-range header is ignored.
	if rng, err = parseRange(r.Header.Get("Range")); err != nil {
		rng, err = nil, nil
	}
	if rng != nil {
		_, _, err = rng.Resolve(head.Size)
	}
	if err != nil {
		w.Header().Set("Content-Range", "bytes */"+strconv.FormatInt(head.Size, 10))
		s3err.Write(w, r, err)
		return nil, true
	}
	return rng, false
}

func objectWriteHead(w http.ResponseWriter, obj storage.ObjectInfo, rng *storage.ByteRange) (int, error) {
	setObjectHeaders(w, obj)
	status, length := http.StatusOK, obj.Size
	if rng != nil {
		offset, n, err := rng.Resolve(obj.Size)
		if err != nil {
			return 0, err
		}
		w.Header().Set("Content-Range", contentRange(offset, n, obj.Size))
		status, length = http.StatusPartialContent, n
	}
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	return status, nil
}

func (h *Handler) getObject(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	rng, done := h.objectPrepare(w, r, info)
	if done {
		return
	}
	body, obj, err := h.backend.GetObject(r.Context(), info.Bucket, info.Key, rng)
	if err != nil {
		s3err.Write(w, r, err)
		return
	}
	defer func() { _ = body.Close() }()
	status, err := objectWriteHead(w, obj, rng)
	if err != nil {
		s3err.Write(w, r, err)
		return
	}
	q := r.URL.Query()
	for param, header := range objectResponseOverrides {
		if v := q.Get(param); v != "" {
			w.Header().Set(header, v)
		}
	}
	w.WriteHeader(status)
	_, _ = io.Copy(w, body)
}

func (h *Handler) headObject(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	rng, done := h.objectPrepare(w, r, info)
	if done {
		return
	}
	obj, err := h.backend.HeadObject(r.Context(), info.Bucket, info.Key)
	if err != nil {
		s3err.Write(w, r, err)
		return
	}
	status, err := objectWriteHead(w, obj, rng)
	if err != nil {
		s3err.Write(w, r, err)
		return
	}
	w.WriteHeader(status)
}

func (h *Handler) deleteObject(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	if err := h.backend.DeleteObject(r.Context(), info.Bucket, info.Key); err != nil {
		s3err.Write(w, r, err)
		return
	}
	writeEmpty(w, http.StatusNoContent)
}

func (h *Handler) copyObject(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	src, err := parseCopySource(r.Header.Get("x-amz-copy-source"))
	if err != nil {
		s3err.Write(w, r, err)
		return
	}
	var replace bool
	switch strings.ToUpper(r.Header.Get("x-amz-metadata-directive")) {
	case "", "COPY":
	case "REPLACE":
		replace = true
	default:
		s3err.Write(w, r, s3err.InvalidArgument.WithMessage("Unknown metadata directive."))
		return
	}
	dst := storage.ObjectRef{Bucket: info.Bucket, Key: info.Key}
	srcInfo, err := h.backend.HeadObject(r.Context(), src.Bucket, src.Key)
	if err != nil {
		s3err.Write(w, r, err)
		return
	}
	if objectEval(objectConds{
		match:           r.Header.Get("x-amz-copy-source-if-match"),
		noneMatch:       r.Header.Get("x-amz-copy-source-if-none-match"),
		modifiedSince:   r.Header.Get("x-amz-copy-source-if-modified-since"),
		unmodifiedSince: r.Header.Get("x-amz-copy-source-if-unmodified-since"),
	}, srcInfo) != objectCondOK {
		s3err.Write(w, r, s3err.PreconditionFailed)
		return
	}
	if src == dst && !replace {
		s3err.Write(w, r, s3err.InvalidRequest.WithMessage("This copy request is illegal because it is trying to copy an object to itself without changing the object's metadata, storage class, website redirect location or encryption attributes."))
		return
	}
	opts := storage.CopyOptions{ReplaceMeta: replace}
	if replace {
		opts.Meta = metaFromHeaders(r.Header)
	}
	obj, err := h.backend.CopyObject(r.Context(), src, dst, opts)
	if err != nil {
		s3err.Write(w, r, err)
		return
	}
	writeXML(w, r, http.StatusOK, CopyObjectResult{
		Xmlns:        s3Namespace,
		LastModified: timeISO(obj.LastModified),
		ETag:         quoteETag(obj.ETag),
	})
}

const (
	objectAllUsersURI  = "http://acs.amazonaws.com/groups/global/AllUsers"
	objectAuthUsersURI = "http://acs.amazonaws.com/groups/global/AuthenticatedUsers"
)

func (h *Handler) getObjectACL(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	obj, err := h.backend.HeadObject(r.Context(), info.Bucket, info.Key)
	if err != nil {
		s3err.Write(w, r, err)
		return
	}
	acp := AccessControlPolicy{Xmlns: s3Namespace, Owner: defaultOwner}
	acp.AccessControlList.Grants = objectGrants(obj.Meta.ACL)
	writeXML(w, r, http.StatusOK, acp)
}

func objectGrants(acl string) []Grant {
	owner := objectGrant("CanonicalUser", "", "FULL_CONTROL")
	owner.Grantee.ID = defaultOwner.ID
	owner.Grantee.DisplayName = defaultOwner.DisplayName
	grants := []Grant{owner}
	switch acl {
	case "public-read":
		grants = append(grants, objectGrant("Group", objectAllUsersURI, "READ"))
	case "public-read-write":
		grants = append(grants, objectGrant("Group", objectAllUsersURI, "READ"), objectGrant("Group", objectAllUsersURI, "WRITE"))
	case "authenticated-read":
		grants = append(grants, objectGrant("Group", objectAuthUsersURI, "READ"))
	}
	return grants
}

func objectGrant(xsiType, uri, permission string) Grant {
	var g Grant
	g.Grantee.XMLNSXSI = "http://www.w3.org/2001/XMLSchema-instance"
	g.Grantee.XSIType = xsiType
	g.Grantee.URI = uri
	g.Permission = permission
	return g
}

// putObjectACL accepts a canned x-amz-acl header or an AccessControlPolicy body;
// body grants are reduced to the closest canned ACL.
func (h *Handler) putObjectACL(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	obj, err := h.backend.HeadObject(r.Context(), info.Bucket, info.Key)
	if err != nil {
		s3err.Write(w, r, err)
		return
	}
	acl := cannedACL(r.Header.Get("x-amz-acl"))
	if r.Header.Get("x-amz-acl") == "" {
		if acl, err = objectACLFromBody(r); err != nil {
			s3err.Write(w, r, err)
			return
		}
	}
	if acl != obj.Meta.ACL {
		meta := obj.Meta
		meta.ACL = acl
		ref := storage.ObjectRef{Bucket: info.Bucket, Key: info.Key}
		if _, err := h.backend.CopyObject(r.Context(), ref, ref, storage.CopyOptions{ReplaceMeta: true, Meta: meta}); err != nil {
			s3err.Write(w, r, err)
			return
		}
	}
	writeEmpty(w, http.StatusOK)
}

func objectACLFromBody(r *http.Request) (string, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return "", nil
	}
	var acp struct {
		Grants []struct {
			URI        string `xml:"Grantee>URI"`
			Permission string `xml:"Permission"`
		} `xml:"AccessControlList>Grant"`
	}
	if err := xml.Unmarshal(body, &acp); err != nil {
		return "", s3err.MalformedXML
	}
	read, write, authRead := false, false, false
	for _, g := range acp.Grants {
		switch {
		case g.URI == objectAllUsersURI && (g.Permission == "READ" || g.Permission == "FULL_CONTROL"):
			read = true
			write = write || g.Permission == "FULL_CONTROL"
		case g.URI == objectAllUsersURI && g.Permission == "WRITE":
			write = true
		case g.URI == objectAuthUsersURI && g.Permission == "READ":
			authRead = true
		}
	}
	switch {
	case read && write:
		return "public-read-write", nil
	case read:
		return "public-read", nil
	case authRead:
		return "authenticated-read", nil
	}
	return "", nil
}
