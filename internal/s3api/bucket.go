package s3api

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/LeoMachado94/s3lab/internal/s3api/s3err"
	"github.com/LeoMachado94/s3lab/internal/s3op"
	"github.com/LeoMachado94/s3lab/internal/storage"
)

// Service and bucket handlers.

func (h *Handler) listBuckets(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	buckets, err := h.backend.ListBuckets(r.Context())
	if err != nil {
		s3err.Write(w, r, err)
		return
	}
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].Name < buckets[j].Name })
	out := ListAllMyBucketsResult{Xmlns: s3Namespace, Owner: defaultOwner, Buckets: []BucketEntry{}}
	for _, b := range buckets {
		out.Buckets = append(out.Buckets, BucketEntry{Name: b.Name, CreationDate: timeISO(b.CreatedAt)})
	}
	writeXML(w, r, http.StatusOK, out)
}

func (h *Handler) createBucket(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	if strings.EqualFold(r.Header.Get("x-amz-bucket-object-lock-enabled"), "true") {
		s3err.Write(w, r, s3err.NotImplemented)
		return
	}
	if err := h.backend.CreateBucket(r.Context(), info.Bucket); err != nil {
		s3err.Write(w, r, err)
		return
	}
	w.Header().Set("Location", "/"+info.Bucket)
	writeEmpty(w, http.StatusOK)
}

func (h *Handler) deleteBucket(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	if err := h.backend.DeleteBucket(r.Context(), info.Bucket); err != nil {
		s3err.Write(w, r, err)
		return
	}
	writeEmpty(w, http.StatusNoContent)
}

func (h *Handler) headBucket(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	if _, err := h.backend.HeadBucket(r.Context(), info.Bucket); err != nil {
		s3err.Write(w, r, err)
		return
	}
	w.Header().Set("x-amz-bucket-region", h.region)
	writeEmpty(w, http.StatusOK)
}

func (h *Handler) getBucketLocation(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	if _, err := h.backend.HeadBucket(r.Context(), info.Bucket); err != nil {
		s3err.Write(w, r, err)
		return
	}
	loc := h.region
	if loc == "us-east-1" {
		loc = ""
	}
	writeXML(w, r, http.StatusOK, LocationConstraint{Xmlns: s3Namespace, Value: loc})
}

func (h *Handler) listObjectsV1(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	q := r.URL.Query()
	maxKeys, err := queryInt(r, "max-keys", storage.DefaultMaxKeys, storage.DefaultMaxKeys)
	if err != nil {
		s3err.Write(w, r, err)
		return
	}
	marker := q.Get("marker")
	res, err := h.bucketList(r, info.Bucket, storage.ListOptions{
		Prefix: q.Get("prefix"), Delimiter: q.Get("delimiter"), StartAfter: marker, MaxKeys: maxKeys,
	})
	if err != nil {
		s3err.Write(w, r, err)
		return
	}
	enc := bucketEncoder(q.Get("encoding-type"))
	out := bucketResult(info.Bucket, q.Get("prefix"), q.Get("delimiter"), maxKeys, enc, q.Get("fetch-owner") == "true", res)
	encMarker := enc.apply(marker)
	out.Marker = &encMarker
	if res.IsTruncated {
		out.NextMarker = enc.apply(res.NextStartAfter)
	}
	writeXML(w, r, http.StatusOK, out)
}

func (h *Handler) listObjectsV2(w http.ResponseWriter, r *http.Request, info *s3op.Info) {
	q := r.URL.Query()
	if lt := q.Get("list-type"); lt != "" && lt != "2" {
		s3err.Write(w, r, s3err.InvalidArgument.WithMessage("Invalid list-type"))
		return
	}
	maxKeys, err := queryInt(r, "max-keys", storage.DefaultMaxKeys, storage.DefaultMaxKeys)
	if err != nil {
		s3err.Write(w, r, err)
		return
	}
	startAfter := q.Get("start-after")
	token := q.Get("continuation-token")
	from := startAfter
	if q.Has("continuation-token") {
		raw, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil || token == "" {
			s3err.Write(w, r, s3err.InvalidArgument.WithMessage("The continuation token provided is incorrect"))
			return
		}
		from = string(raw)
	}
	res, err := h.bucketList(r, info.Bucket, storage.ListOptions{
		Prefix: q.Get("prefix"), Delimiter: q.Get("delimiter"), StartAfter: from, MaxKeys: maxKeys,
	})
	if err != nil {
		s3err.Write(w, r, err)
		return
	}
	enc := bucketEncoder(q.Get("encoding-type"))
	out := bucketResult(info.Bucket, q.Get("prefix"), q.Get("delimiter"), maxKeys, enc, q.Get("fetch-owner") == "true", res)
	count := len(out.Contents) + len(out.CommonPrefixes)
	out.KeyCount = &count
	out.StartAfter = enc.apply(startAfter)
	out.ContinuationToken = token
	if res.IsTruncated {
		out.NextContinuationToken = base64.RawURLEncoding.EncodeToString([]byte(res.NextStartAfter))
	}
	writeXML(w, r, http.StatusOK, out)
}

// bucketList lists objects; max-keys=0 yields an empty result (the storage
// port treats 0 as "default") after checking the bucket exists.
func (h *Handler) bucketList(r *http.Request, bucket string, opts storage.ListOptions) (storage.ListResult, error) {
	if opts.MaxKeys == 0 {
		_, err := h.backend.HeadBucket(r.Context(), bucket)
		return storage.ListResult{}, err
	}
	return h.backend.ListObjects(r.Context(), bucket, opts)
}

type bucketEnc bool

// apply encodes so that url.QueryUnescape (used by SDKs) round-trips; '/' stays.
func (e bucketEnc) apply(s string) string {
	if !e {
		return s
	}
	return strings.ReplaceAll(url.QueryEscape(s), "%2F", "/")
}

func bucketEncoder(encodingType string) bucketEnc { return bucketEnc(encodingType == "url") }

func bucketResult(bucket, prefix, delimiter string, maxKeys int, enc bucketEnc, fetchOwner bool, res storage.ListResult) ListBucketResult {
	out := ListBucketResult{
		Xmlns:          s3Namespace,
		Name:           bucket,
		Prefix:         enc.apply(prefix),
		Delimiter:      enc.apply(delimiter),
		MaxKeys:        maxKeys,
		IsTruncated:    res.IsTruncated,
		Contents:       []ObjectEntry{},
		CommonPrefixes: []CommonPrefix{},
	}
	if enc {
		out.EncodingType = "url"
	}
	for _, o := range res.Objects {
		e := ObjectEntry{
			Key:          enc.apply(o.Key),
			LastModified: timeISO(o.LastModified),
			ETag:         quoteETag(o.ETag),
			Size:         o.Size,
			StorageClass: "STANDARD",
		}
		if fetchOwner {
			owner := defaultOwner
			e.Owner = &owner
		}
		out.Contents = append(out.Contents, e)
	}
	for _, p := range res.CommonPrefixes {
		out.CommonPrefixes = append(out.CommonPrefixes, CommonPrefix{Prefix: enc.apply(p)})
	}
	return out
}
