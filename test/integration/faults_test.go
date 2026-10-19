//go:build integration

package integration

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"

	"github.com/LeoMachado94/s3lab/internal/config"
)

func TestFaultRetriedByDefaultRetryer(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st config.Storage) {
		h := startServer(t, st, nil)
		h.createBucket("flaky")
		id := h.addFault(`{"op":"PutObject","status":503,"code":"SlowDown","count":2}`)

		client := newClient(t, h.s3URL, func(o *s3.Options) { o.RetryMaxAttempts = 5 })
		_, err := client.PutObject(t.Context(), &s3.PutObjectInput{
			Bucket: aws.String("flaky"), Key: aws.String("a.txt"), Body: bytesReader([]byte("x")),
		})
		require.NoError(t, err)

		puts := h.waitRequests("op=PutObject", 3)
		require.Len(t, puts, 3)
		for i, e := range puts[:2] {
			require.Equal(t, http.StatusServiceUnavailable, e.Status, i)
			require.Equal(t, "SlowDown", e.ErrorCode)
			require.Equal(t, id, e.FaultID)
		}
		require.Equal(t, http.StatusOK, puts[2].Status)
		require.Empty(t, puts[2].FaultID)

		rules := h.faultRules()
		require.Len(t, rules, 1)
		require.Equal(t, id, rules[0].ID)
		require.Equal(t, 2, rules[0].Hits)
		require.Equal(t, []byte("x"), h.getBytes("flaky", "a.txt"))
	})
}

func TestStorageTargetFault(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st config.Storage) {
		h := startServer(t, st, nil)
		h.createBucket("store")
		h.putBytes("store", "k", []byte("v"))
		id := h.addFault(`{"target":"storage","op":"GetObject","status":500}`)

		noRetry := newClient(t, h.s3URL, func(o *s3.Options) { o.Retryer = aws.NopRetryer{} })
		_, err := noRetry.GetObject(t.Context(), &s3.GetObjectInput{Bucket: aws.String("store"), Key: aws.String("k")})
		requireAPIError(t, err, http.StatusInternalServerError, "InternalError")

		failed := h.waitRequests("op=GetObject&status=500", 1)
		require.Equal(t, "InternalError", failed[0].ErrorCode)
		require.Equal(t, id, failed[0].FaultID)

		_, err = noRetry.HeadObject(t.Context(), &s3.HeadObjectInput{Bucket: aws.String("store"), Key: aws.String("k")})
		require.NoError(t, err)
	})
}

func TestDelayFaultTimesOutClient(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st config.Storage) {
		h := startServer(t, st, nil)
		h.createBucket("slow")
		h.putBytes("slow", "k", []byte("v"))
		h.addFault(`{"op":"GetObject","delayMs":2000}`)

		client := newClient(t, h.s3URL, func(o *s3.Options) { o.Retryer = aws.NopRetryer{} })
		ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("slow"), Key: aws.String("k")})
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.Less(t, time.Since(start), 1500*time.Millisecond)
	})
}
