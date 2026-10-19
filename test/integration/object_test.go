//go:build integration

package integration

import (
	"bytes"
	"crypto/sha256"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/require"

	"github.com/LeoMachado94/s3lab/internal/config"
)

func TestObjectCRUD(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st config.Storage) {
		h := startServer(t, st, nil)
		ctx := t.Context()
		h.createBucket("docs")

		body := []byte("0123456789abcdef")
		put, err := h.s3.PutObject(ctx, &s3.PutObjectInput{
			Bucket:       aws.String("docs"),
			Key:          aws.String("dir/file name+1.txt"),
			Body:         bytes.NewReader(body),
			ContentType:  aws.String("text/plain; charset=utf-8"),
			CacheControl: aws.String("max-age=60"),
			Metadata:     map[string]string{"owner": "leo", "Kind": "test"},
		})
		require.NoError(t, err)
		etag := aws.ToString(put.ETag)
		require.NotEmpty(t, etag)

		head, err := h.s3.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String("docs"), Key: aws.String("dir/file name+1.txt")})
		require.NoError(t, err)
		require.EqualValues(t, len(body), aws.ToInt64(head.ContentLength))
		require.Equal(t, "text/plain; charset=utf-8", aws.ToString(head.ContentType))
		require.Equal(t, "max-age=60", aws.ToString(head.CacheControl))
		require.Equal(t, etag, aws.ToString(head.ETag))
		require.Equal(t, map[string]string{"owner": "leo", "kind": "test"}, head.Metadata)

		got, err := h.s3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("docs"), Key: aws.String("dir/file name+1.txt")})
		require.NoError(t, err)
		data, err := io.ReadAll(got.Body)
		require.NoError(t, err)
		require.NoError(t, got.Body.Close())
		require.Equal(t, body, data)
		require.Equal(t, "leo", got.Metadata["owner"])

		rng, err := h.s3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("docs"), Key: aws.String("dir/file name+1.txt"), Range: aws.String("bytes=2-5")})
		require.NoError(t, err)
		part, err := io.ReadAll(rng.Body)
		require.NoError(t, err)
		require.NoError(t, rng.Body.Close())
		require.Equal(t, "2345", string(part))
		require.Equal(t, "bytes 2-5/16", aws.ToString(rng.ContentRange))

		_, err = h.s3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("docs"), Key: aws.String("dir/file name+1.txt"), IfNoneMatch: aws.String(etag)})
		require.Error(t, err)
		require.Equal(t, http.StatusNotModified, httpStatus(err))

		_, err = h.s3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("docs"), Key: aws.String("dir/file name+1.txt"), IfMatch: aws.String(`"nope"`)})
		requireAPIError(t, err, http.StatusPreconditionFailed, "PreconditionFailed")

		_, err = h.s3.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String("docs"), Key: aws.String("dir/file name+1.txt")})
		require.NoError(t, err)
		_, err = h.s3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("docs"), Key: aws.String("dir/file name+1.txt")})
		requireAPIError(t, err, http.StatusNotFound, "NoSuchKey")
		_, err = h.s3.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String("docs"), Key: aws.String("dir/file name+1.txt")})
		requireAPIError(t, err, http.StatusNotFound, "")

		log := h.waitRequests("bucket=docs", 8)
		puts := h.requests("op=PutObject")
		require.Len(t, puts, 1)
		require.Equal(t, "dir/file name+1.txt", puts[0].Key)
		require.Equal(t, http.StatusOK, puts[0].Status)
		require.Equal(t, testAccessKey, puts[0].AccessKey)
		require.Equal(t, 1, countOp(log, "DeleteObject"))
		require.Len(t, h.requests("op=GetObject&status=304"), 1)
		require.Len(t, h.requests("op=GetObject&status=206"), 1)
		require.Len(t, h.requests("op=GetObject&status=404"), 1)
	})
}

func TestDefaultChecksumUpload(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st config.Storage) {
		h := startServer(t, st, nil)
		ctx := t.Context()
		h.createBucket("sums")

		// Default RequestChecksumCalculation=when_supported makes the SDK add a CRC32 checksum
		// (aws-chunked trailer when it can stream, header otherwise).
		data := genBytes(1, 300_000)
		_, err := h.s3.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String("sums"), Key: aws.String("default"), Body: bytes.NewReader(data)})
		require.NoError(t, err)
		require.Equal(t, data, h.getBytes("sums", "default"))

		_, err = h.s3.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String("sums"), Key: aws.String("crc32c"), Body: bytes.NewReader(data),
			ChecksumAlgorithm: types.ChecksumAlgorithmCrc32c,
		})
		require.NoError(t, err)
		require.Equal(t, data, h.getBytes("sums", "crc32c"))

		_, err = h.s3.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String("sums"), Key: aws.String("sha256"), Body: bytes.NewReader(data),
			ChecksumAlgorithm: types.ChecksumAlgorithmSha256,
		})
		require.NoError(t, err)
		require.Equal(t, data, h.getBytes("sums", "sha256"))

		// The SDK refuses unseekable or streaming signed payloads over plain HTTP, so the
		// STREAMING-AWS4-HMAC-SHA256-PAYLOAD variant cannot be forced here without TLS.
		puts := h.waitRequests("op=PutObject", 3)
		for _, e := range puts {
			require.Equal(t, http.StatusOK, e.Status)
		}
	})
}

func TestCopyObject(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st config.Storage) {
		h := startServer(t, st, nil)
		ctx := t.Context()
		h.createBucket("src")
		h.createBucket("dst")

		body := genBytes(2, 4096)
		_, err := h.s3.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String("src"), Key: aws.String("orig.bin"), Body: bytes.NewReader(body),
			ContentType: aws.String("application/x-test"), Metadata: map[string]string{"a": "1"},
		})
		require.NoError(t, err)

		_, err = h.s3.CopyObject(ctx, &s3.CopyObjectInput{
			Bucket: aws.String("dst"), Key: aws.String("copied.bin"), CopySource: aws.String("src/orig.bin"),
		})
		require.NoError(t, err)
		head, err := h.s3.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String("dst"), Key: aws.String("copied.bin")})
		require.NoError(t, err)
		require.Equal(t, "application/x-test", aws.ToString(head.ContentType))
		require.Equal(t, map[string]string{"a": "1"}, head.Metadata)
		require.Equal(t, body, h.getBytes("dst", "copied.bin"))

		_, err = h.s3.CopyObject(ctx, &s3.CopyObjectInput{
			Bucket: aws.String("dst"), Key: aws.String("replaced.bin"), CopySource: aws.String("src/orig.bin"),
			MetadataDirective: types.MetadataDirectiveReplace,
			ContentType:       aws.String("text/csv"),
			Metadata:          map[string]string{"b": "2"},
		})
		require.NoError(t, err)
		head, err = h.s3.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String("dst"), Key: aws.String("replaced.bin")})
		require.NoError(t, err)
		require.Equal(t, "text/csv", aws.ToString(head.ContentType))
		require.Equal(t, map[string]string{"b": "2"}, head.Metadata)
		require.Equal(t, body, h.getBytes("dst", "replaced.bin"))

		_, err = h.s3.CopyObject(ctx, &s3.CopyObjectInput{
			Bucket: aws.String("dst"), Key: aws.String("x"), CopySource: aws.String("src/missing"),
		})
		requireAPIError(t, err, http.StatusNotFound, "NoSuchKey")

		require.Len(t, h.waitRequests("op=CopyObject", 3), 3)
	})
}

func TestDeleteObjectsBatch(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st config.Storage) {
		h := startServer(t, st, nil)
		ctx := t.Context()
		h.createBucket("batch")
		for _, k := range []string{"a", "b", "c", "keep"} {
			h.putBytes("batch", k, []byte(k))
		}

		out, err := h.s3.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String("batch"),
			Delete: &types.Delete{Objects: []types.ObjectIdentifier{
				{Key: aws.String("a")}, {Key: aws.String("b")}, {Key: aws.String("c")}, {Key: aws.String("never-existed")},
			}},
		})
		require.NoError(t, err)
		require.Empty(t, out.Errors)
		require.Len(t, out.Deleted, 4)
		require.Equal(t, []string{"keep"}, h.listKeys("batch", ""))

		quiet, err := h.s3.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String("batch"),
			Delete: &types.Delete{Quiet: aws.Bool(true), Objects: []types.ObjectIdentifier{{Key: aws.String("keep")}}},
		})
		require.NoError(t, err)
		require.Empty(t, quiet.Deleted)
		require.Empty(t, h.listKeys("batch", ""))

		require.Len(t, h.waitRequests("op=DeleteObjects", 2), 2)
	})
}

func TestLargeObjectStreaming(t *testing.T) {
	const size = 64 << 20
	t.Run("fs", func(t *testing.T) {
		t.Parallel()
		h := startServer(t, config.StorageFS, nil)
		ctx := t.Context()
		h.createBucket("big")

		want, err := sha256Of(newGenReader(42, size))
		require.NoError(t, err)

		_, err = h.s3.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String("big"), Key: aws.String("blob"), Body: newGenReader(42, size),
		}, func(o *s3.Options) { o.HTTPClient = &http.Client{Timeout: 60 * time.Second} })
		require.NoError(t, err)

		head, err := h.s3.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String("big"), Key: aws.String("blob")})
		require.NoError(t, err)
		require.EqualValues(t, size, aws.ToInt64(head.ContentLength))

		got, err := h.s3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("big"), Key: aws.String("blob")})
		require.NoError(t, err)
		defer got.Body.Close()
		hash := sha256.New()
		n, err := io.Copy(hash, got.Body)
		require.NoError(t, err)
		require.EqualValues(t, size, n)
		var sum [32]byte
		copy(sum[:], hash.Sum(nil))
		require.Equal(t, want, sum)

		puts := h.waitRequests("op=PutObject", 1)
		require.Equal(t, http.StatusOK, puts[0].Status)
	})
}
