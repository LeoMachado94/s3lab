//go:build integration

package integration

import (
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"

	"github.com/LeoMachado94/s3lab/internal/config"
)

// manager.Uploader is deprecated in favour of a preview package; the suite targets the stable one.
//
//nolint:staticcheck
func TestMultipartUploader(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st config.Storage) {
		h := startServer(t, st, nil)
		ctx := t.Context()
		h.createBucket("mpu")

		data := genBytes(7, 15<<20)
		up := manager.NewUploader(h.s3, func(u *manager.Uploader) {
			u.PartSize = 5 << 20
			u.Concurrency = 3
		})
		out, err := up.Upload(ctx, &s3.PutObjectInput{
			Bucket: aws.String("mpu"), Key: aws.String("big.bin"), Body: bytesReader(data),
		})
		require.NoError(t, err)
		require.True(t, strings.HasSuffix(strings.Trim(aws.ToString(out.ETag), `"`), "-3"), aws.ToString(out.ETag))

		head, err := h.s3.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String("mpu"), Key: aws.String("big.bin")})
		require.NoError(t, err)
		require.EqualValues(t, len(data), aws.ToInt64(head.ContentLength))
		require.Equal(t, aws.ToString(out.ETag), aws.ToString(head.ETag))
		require.Equal(t, data, h.getBytes("mpu", "big.bin"))

		log := h.waitRequests("bucket=mpu", 5)
		require.Equal(t, 1, countOp(log, "CreateMultipartUpload"))
		require.Equal(t, 3, countOp(log, "UploadPart"))
		require.Equal(t, 1, countOp(log, "CompleteMultipartUpload"))
		require.Equal(t, 0, countOp(log, "AbortMultipartUpload"))
		require.Empty(t, h.requests("bucket=mpu&errors=true"))

		uploads, err := h.s3.ListMultipartUploads(ctx, &s3.ListMultipartUploadsInput{Bucket: aws.String("mpu")})
		require.NoError(t, err)
		require.Empty(t, uploads.Uploads)
	})
}
