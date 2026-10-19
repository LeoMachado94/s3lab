//go:build integration

package integration

import (
	"bytes"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"

	"github.com/LeoMachado94/s3lab/internal/config"
)

func TestPresignedURLs(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st config.Storage) {
		h := startServer(t, st, nil)
		ctx := t.Context()
		h.createBucket("pre")
		ps := s3.NewPresignClient(h.s3)

		data := genBytes(3, 50_000)
		putReq, err := ps.PresignPutObject(ctx, &s3.PutObjectInput{Bucket: aws.String("pre"), Key: aws.String("up.bin")})
		require.NoError(t, err)
		hdr := http.Header{}
		for k, v := range putReq.SignedHeader {
			hdr[k] = v
		}
		resp, _ := plainDo(t, putReq.Method, putReq.URL, bytes.NewReader(data), hdr)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		getReq, err := ps.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("pre"), Key: aws.String("up.bin")})
		require.NoError(t, err)
		resp, body := plainDo(t, getReq.Method, getReq.URL, nil, nil)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Equal(t, data, body)

		log := h.waitRequests("bucket=pre", 3)
		for _, e := range log {
			if e.Op == "CreateBucket" {
				continue
			}
			require.Equal(t, "presigned", e.Auth, "%s", e.Op)
			require.Equal(t, http.StatusOK, e.Status)
			require.Contains(t, e.Query, "X-Amz-Signature=REDACTED")
		}
		u, err := url.Parse(getReq.URL)
		require.NoError(t, err)
		realSig := u.Query().Get("X-Amz-Signature")
		require.NotEmpty(t, realSig)
		for _, e := range log {
			require.False(t, strings.Contains(e.Query, realSig), "signature leaked into request log")
		}

		require.Equal(t, data, h.getBytes("pre", "up.bin"))
	})
}

func TestPresignedExpired(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st config.Storage) {
		h := startServer(t, st, nil)
		h.createBucket("pre")
		h.putBytes("pre", "k", []byte("v"))

		ps := s3.NewPresignClient(h.s3)
		getReq, err := ps.PresignGetObject(t.Context(), &s3.GetObjectInput{Bucket: aws.String("pre"), Key: aws.String("k")},
			s3.WithPresignExpires(time.Second))
		require.NoError(t, err)

		resp, _ := plainDo(t, http.MethodGet, getReq.URL, nil, nil)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		time.Sleep(2 * time.Second)
		resp, body := plainDo(t, http.MethodGet, getReq.URL, nil, nil)
		require.Equal(t, http.StatusForbidden, resp.StatusCode)
		require.Contains(t, string(body), "AccessDenied")

		failed := h.waitRequests("op=GetObject&status=403", 1)
		require.Contains(t, failed[0].Query, "X-Amz-Signature=REDACTED")
	})
}
