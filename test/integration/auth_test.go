//go:build integration

package integration

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"

	"github.com/LeoMachado94/s3lab/internal/config"
)

func TestAuthFailures(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st config.Storage) {
		h := startServer(t, st, nil)
		h.createBucket("secure")
		h.putBytes("secure", "k", []byte("v"))
		ctx := t.Context()

		badSecret := newClientWithCreds(t, h.s3URL, testAccessKey, "wrong-secret")
		_, err := badSecret.ListBuckets(ctx, &s3.ListBucketsInput{})
		requireAPIError(t, err, http.StatusForbidden, "SignatureDoesNotMatch")

		unknown := newClientWithCreds(t, h.s3URL, "nobody", testSecretKey)
		_, err = unknown.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("secure"), Key: aws.String("k")})
		requireAPIError(t, err, http.StatusForbidden, "InvalidAccessKeyId")

		resp, body := plainDo(t, http.MethodGet, h.s3URL+"/secure/k", nil, nil)
		require.Equal(t, http.StatusForbidden, resp.StatusCode)
		require.Contains(t, string(body), "AccessDenied")

		failed := h.waitRequests("errors=true", 3)
		codes := map[string]bool{}
		for _, e := range failed {
			require.Equal(t, http.StatusForbidden, e.Status)
			codes[e.ErrorCode] = true
		}
		require.True(t, codes["SignatureDoesNotMatch"] && codes["InvalidAccessKeyId"] && codes["AccessDenied"], "%v", codes)
		require.Len(t, h.requests("op=ListBuckets&status=403"), 1)
		require.Equal(t, "failed", h.requests("op=ListBuckets&status=403")[0].Auth)

		require.Equal(t, []byte("v"), h.getBytes("secure", "k"))
	})
}

func TestAnonymousAccess(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st config.Storage) {
		h := startServer(t, st, func(c *config.Config) { c.Anonymous = true })
		h.createBucket("open")
		h.putBytes("open", "k", []byte("public"))

		resp, body := plainDo(t, http.MethodGet, h.s3URL+"/open/k", nil, nil)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Equal(t, "public", string(body))

		log := h.waitRequests("op=GetObject", 1)
		require.Empty(t, log[0].Auth)
	})
}

func TestVirtualHostStyle(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st config.Storage) {
		h := startServer(t, st, func(c *config.Config) { c.Domain = "s3.test" })
		h.createBucket("vhost")
		h.putBytes("vhost", "hello.txt", []byte("hi"))

		u, err := url.Parse(h.s3URL)
		require.NoError(t, err)
		listener := u.Host
		httpClient := &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, listener)
			},
		}}
		t.Cleanup(httpClient.CloseIdleConnections)

		vh := newClient(t, "http://s3.test:"+u.Port(), func(o *s3.Options) {
			o.UsePathStyle = false
			o.HTTPClient = httpClient
		})
		out, err := vh.ListObjectsV2(t.Context(), &s3.ListObjectsV2Input{Bucket: aws.String("vhost")})
		require.NoError(t, err)
		require.Len(t, out.Contents, 1)
		require.Equal(t, "hello.txt", aws.ToString(out.Contents[0].Key))

		got, err := vh.HeadObject(t.Context(), &s3.HeadObjectInput{Bucket: aws.String("vhost"), Key: aws.String("hello.txt")})
		require.NoError(t, err)
		require.EqualValues(t, 2, aws.ToInt64(got.ContentLength))

		log := h.waitRequests("bucket=vhost&op=ListObjectsV2", 1)
		require.Equal(t, http.StatusOK, log[0].Status)
		require.Equal(t, "header", log[0].Auth)
		require.Len(t, h.requests("bucket=vhost&op=HeadObject&key=hello.txt"), 1)
	})
}
