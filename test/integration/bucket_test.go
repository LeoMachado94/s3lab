//go:build integration

package integration

import (
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"

	"github.com/LeoMachado94/s3lab/internal/config"
)

func TestBucketLifecycle(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st config.Storage) {
		h := startServer(t, st, func(c *config.Config) { c.Region = "eu-west-1" })
		ctx := t.Context()

		h.createBucket("alpha")
		h.createBucket("beta")

		_, err := h.s3.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("alpha")})
		requireAPIError(t, err, http.StatusConflict, "BucketAlreadyOwnedByYou")

		list, err := h.s3.ListBuckets(ctx, &s3.ListBucketsInput{})
		require.NoError(t, err)
		var names []string
		for _, b := range list.Buckets {
			names = append(names, aws.ToString(b.Name))
			require.NotNil(t, b.CreationDate)
		}
		require.ElementsMatch(t, []string{"alpha", "beta"}, names)

		_, err = h.s3.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String("alpha")})
		require.NoError(t, err)
		_, err = h.s3.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String("missing")})
		requireAPIError(t, err, http.StatusNotFound, "")

		loc, err := h.s3.GetBucketLocation(ctx, &s3.GetBucketLocationInput{Bucket: aws.String("alpha")})
		require.NoError(t, err)
		require.Equal(t, "eu-west-1", string(loc.LocationConstraint))

		h.putBytes("alpha", "k", []byte("v"))
		_, err = h.s3.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String("alpha")})
		requireAPIError(t, err, http.StatusConflict, "BucketNotEmpty")

		_, err = h.s3.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String("alpha"), Key: aws.String("k")})
		require.NoError(t, err)
		_, err = h.s3.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String("alpha")})
		require.NoError(t, err)

		_, err = h.s3.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String("alpha")})
		requireAPIError(t, err, http.StatusNotFound, "NoSuchBucket")

		log := h.waitRequests("", 10)
		got := ops(log)
		for _, op := range []string{"CreateBucket", "ListBuckets", "HeadBucket", "GetBucketLocation", "DeleteBucket"} {
			require.Contains(t, got, op)
		}
		require.Len(t, h.requests("op=CreateBucket&status=409"), 1)
		for _, e := range log {
			require.Equal(t, "header", e.Auth, "%s %s", e.Op, e.Path)
		}
	})
}

func TestCORSPreflight(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st config.Storage) {
		h := startServer(t, st, nil)
		h.createBucket("web")

		hdr := http.Header{}
		hdr.Set("Origin", "http://localhost:5173")
		hdr.Set("Access-Control-Request-Method", "PUT")
		hdr.Set("Access-Control-Request-Headers", "content-type,x-amz-meta-foo")
		resp, _ := plainDo(t, http.MethodOptions, h.s3URL+"/web/upload.png", nil, hdr)

		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.NotEmpty(t, resp.Header.Get("Access-Control-Allow-Origin"))
		require.Contains(t, resp.Header.Get("Access-Control-Allow-Methods"), "PUT")
		require.NotEmpty(t, resp.Header.Get("Access-Control-Allow-Headers"))

		log := h.waitRequests("op=CORSPreflight", 1)
		require.Equal(t, http.StatusOK, log[0].Status)
		require.Empty(t, log[0].Auth)
	})
}

func TestMinioHealthAliases(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st config.Storage) {
		h := startServer(t, st, nil)
		for _, p := range []string{"/minio/health/live", "/minio/health/ready"} {
			resp, _ := plainDo(t, http.MethodGet, h.s3URL+p, nil, nil)
			require.Equal(t, http.StatusOK, resp.StatusCode, p)
		}
	})
}
