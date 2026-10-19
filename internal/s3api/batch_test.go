package s3api

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/require"

	"github.com/LeoMachado94/s3lab/internal/storage"
)

func batchBody(quiet bool, keys ...string) string {
	req := DeleteRequest{Quiet: quiet}
	for _, k := range keys {
		req.Objects = append(req.Objects, ObjectIdentifier{Key: k})
	}
	out, _ := xml.Marshal(req)
	return string(out)
}

func batchDelete(t *testing.T, h http.Handler, bucket, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	return do(t, h, testReq{method: http.MethodPost, target: "/" + bucket + "?delete", body: strings.NewReader(body), headers: headers})
}

func batchParse(t *testing.T, rec *httptest.ResponseRecorder) DeleteResult {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var res DeleteResult
	require.NoError(t, xml.Unmarshal(rec.Body.Bytes(), &res))
	require.Equal(t, s3Namespace, res.Xmlns)
	return res
}

func TestDeleteObjects(t *testing.T) {
	setup := func(t *testing.T) (http.Handler, storage.Backend) {
		h, b := newTestHandler(t)
		mustCreateBucket(t, b, "bkt")
		for _, k := range []string{"a", "b", "dir/c"} {
			mustPut(t, b, "bkt", k, "data")
		}
		return h, b
	}
	exists := func(t *testing.T, b storage.Backend, key string) bool {
		_, err := b.HeadObject(t.Context(), "bkt", key)
		return err == nil
	}

	t.Run("normal", func(t *testing.T) {
		h, b := setup(t)
		res := batchParse(t, batchDelete(t, h, "bkt", batchBody(false, "a", "dir/c"), nil))
		require.Equal(t, []DeletedObject{{Key: "a"}, {Key: "dir/c"}}, res.Deleted)
		require.Empty(t, res.Errors)
		require.False(t, exists(t, b, "a"))
		require.False(t, exists(t, b, "dir/c"))
		require.True(t, exists(t, b, "b"))
	})

	t.Run("quiet omits deleted", func(t *testing.T) {
		h, b := setup(t)
		res := batchParse(t, batchDelete(t, h, "bkt", batchBody(true, "a", "b"), nil))
		require.Empty(t, res.Deleted)
		require.Empty(t, res.Errors)
		require.False(t, exists(t, b, "a"))
		require.False(t, exists(t, b, "b"))
	})

	t.Run("missing keys count as deleted", func(t *testing.T) {
		h, _ := setup(t)
		res := batchParse(t, batchDelete(t, h, "bkt", batchBody(false, "a", "ghost"), nil))
		require.Equal(t, []DeletedObject{{Key: "a"}, {Key: "ghost"}}, res.Deleted)
		require.Empty(t, res.Errors)
	})

	t.Run("per key error", func(t *testing.T) {
		_, b := setup(t)
		h := New(batchFailBackend{Backend: b, failKey: "bad"})
		res := batchParse(t, batchDelete(t, h, "bkt", batchBody(false, "a", "bad", "b"), nil))
		require.Equal(t, []DeletedObject{{Key: "a"}, {Key: "b"}}, res.Deleted)
		require.Len(t, res.Errors, 1)
		require.Equal(t, "bad", res.Errors[0].Key)
		require.Equal(t, "InternalError", res.Errors[0].Code)
		require.NotEmpty(t, res.Errors[0].Message)
		require.False(t, exists(t, b, "b"))
	})

	t.Run("valid content-md5", func(t *testing.T) {
		h, _ := setup(t)
		body := batchBody(false, "a")
		res := batchParse(t, batchDelete(t, h, "bkt", body, map[string]string{"Content-MD5": mpMD5B64([]byte(body))}))
		require.Len(t, res.Deleted, 1)
	})

	t.Run("max objects", func(t *testing.T) {
		h, _ := setup(t)
		keys := make([]string, 1000)
		for i := range keys {
			keys[i] = fmt.Sprintf("k%04d", i)
		}
		res := batchParse(t, batchDelete(t, h, "bkt", batchBody(true, keys...), nil))
		require.Empty(t, res.Errors)
	})

	errTests := []struct {
		name    string
		bucket  string
		body    string
		headers map[string]string
		status  int
		code    string
	}{
		{"too many", "bkt", batchBody(false, func() []string {
			keys := make([]string, 1001)
			for i := range keys {
				keys[i] = fmt.Sprintf("k%04d", i)
			}
			return keys
		}()...), nil, 400, "MalformedXML"},
		{"empty list", "bkt", batchBody(false), nil, 400, "MalformedXML"},
		{"empty body", "bkt", "", nil, 400, "MalformedXML"},
		{"malformed", "bkt", "<Delete><Object>", nil, 400, "MalformedXML"},
		{"missing bucket", "nobkt", batchBody(false, "a"), nil, 404, "NoSuchBucket"},
		{"bad md5", "bkt", batchBody(false, "a"), map[string]string{"Content-MD5": mpMD5B64([]byte("other"))}, 400, "BadDigest"},
		{"invalid md5", "bkt", batchBody(false, "a"), map[string]string{"Content-MD5": "###"}, 400, "InvalidDigest"},
	}
	for _, tt := range errTests {
		t.Run(tt.name, func(t *testing.T) {
			h, b := setup(t)
			requireS3Error(t, batchDelete(t, h, tt.bucket, tt.body, tt.headers), tt.status, tt.code)
			require.True(t, exists(t, b, "a"), "nothing may be deleted on request errors")
		})
	}
}

type batchFailBackend struct {
	storage.Backend
	failKey string
}

func (f batchFailBackend) DeleteObject(ctx context.Context, bucket, key string) error {
	if key == f.failKey {
		return errors.New("disk on fire")
	}
	return f.Backend.DeleteObject(ctx, bucket, key)
}

func batchSDKClient(t *testing.T, h http.Handler) *s3.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return s3.New(s3.Options{
		Region:                     "us-east-1",
		BaseEndpoint:               aws.String(srv.URL),
		UsePathStyle:               true,
		Credentials:                aws.AnonymousCredentials{},
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
}

func TestSDKMultipartUploadAndDeleteObjects(t *testing.T) {
	h, b := newTestHandler(t)
	mustCreateBucket(t, b, "bkt")
	client := batchSDKClient(t, h)
	ctx := t.Context()

	payload := make([]byte, 12*mpMiB)
	for i := range payload {
		payload[i] = byte(i * 7)
	}
	uploader := manager.NewUploader(client, func(u *manager.Uploader) { u.PartSize = 5 * mpMiB }) //nolint:staticcheck // deprecated, still the common SDK path
	out, err := uploader.Upload(ctx, &s3.PutObjectInput{                                          //nolint:staticcheck // see above
		Bucket:      aws.String("bkt"),
		Key:         aws.String("big/file name.bin"),
		Body:        bytes.NewReader(payload),
		ContentType: aws.String("application/x-test"),
	})
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(aws.ToString(out.ETag), `-3"`), aws.ToString(out.ETag))

	rc, info, err := b.GetObject(ctx, "bkt", "big/file name.bin", nil)
	require.NoError(t, err)
	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.NoError(t, rc.Close())
	require.True(t, bytes.Equal(payload, got), "uploaded bytes differ")
	require.True(t, strings.HasSuffix(info.ETag, "-3"), info.ETag)
	require.Equal(t, 3, info.PartsCount)
	require.Equal(t, "application/x-test", info.Meta.ContentType)

	uploads, err := client.ListMultipartUploads(ctx, &s3.ListMultipartUploadsInput{Bucket: aws.String("bkt")})
	require.NoError(t, err)
	require.Empty(t, uploads.Uploads)

	t.Run("abort and list", func(t *testing.T) {
		create, err := client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String("bkt"), Key: aws.String("pending")})
		require.NoError(t, err)
		_, err = client.UploadPart(ctx, &s3.UploadPartInput{
			Bucket: aws.String("bkt"), Key: aws.String("pending"), UploadId: create.UploadId,
			PartNumber: aws.Int32(1), Body: strings.NewReader("part"),
		})
		require.NoError(t, err)

		parts, err := client.ListParts(ctx, &s3.ListPartsInput{Bucket: aws.String("bkt"), Key: aws.String("pending"), UploadId: create.UploadId})
		require.NoError(t, err)
		require.Len(t, parts.Parts, 1)
		uploads, err := client.ListMultipartUploads(ctx, &s3.ListMultipartUploadsInput{Bucket: aws.String("bkt"), Prefix: aws.String("pend")})
		require.NoError(t, err)
		require.Len(t, uploads.Uploads, 1)

		_, err = client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{Bucket: aws.String("bkt"), Key: aws.String("pending"), UploadId: create.UploadId})
		require.NoError(t, err)
	})

	t.Run("delete objects", func(t *testing.T) {
		keys := []string{"x/1", "x/2", "x/3", "x/ünï"}
		var ids []s3types.ObjectIdentifier
		for _, k := range keys {
			mustPut(t, b, "bkt", k, "v")
			ids = append(ids, s3types.ObjectIdentifier{Key: aws.String(k)})
		}
		ids = append(ids, s3types.ObjectIdentifier{Key: aws.String("never-existed")})

		res, err := client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String("bkt"), Delete: &s3types.Delete{Objects: ids},
		})
		require.NoError(t, err)
		require.Len(t, res.Deleted, len(ids))
		require.Empty(t, res.Errors)
		for _, k := range keys {
			_, err := b.HeadObject(ctx, "bkt", k)
			require.ErrorIs(t, err, storage.ErrNoSuchKey)
		}

		mustPut(t, b, "bkt", "q", "v")
		res, err = client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String("bkt"),
			Delete: &s3types.Delete{Quiet: aws.Bool(true), Objects: []s3types.ObjectIdentifier{{Key: aws.String("q")}}},
		})
		require.NoError(t, err)
		require.Empty(t, res.Deleted)
		_, err = b.HeadObject(ctx, "bkt", "q")
		require.ErrorIs(t, err, storage.ErrNoSuchKey)
	})
}
