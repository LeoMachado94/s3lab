//go:build integration

package integration

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"

	"github.com/LeoMachado94/s3lab/internal/config"
)

func seedDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{
		"seeded/a.txt":         "alpha",
		"seeded/nested/b.json": `{"b":true}`,
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
	return dir
}

func TestSeedAndReset(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st config.Storage) {
		dir := seedDir(t)
		h := startServer(t, st, func(c *config.Config) {
			c.Buckets = []string{"seeded"}
			c.SeedDir = dir
		})
		ctx := t.Context()
		seeded := []string{"a.txt", "nested/b.json"}

		require.Equal(t, seeded, h.listKeys("seeded", ""))
		require.Equal(t, []byte("alpha"), h.getBytes("seeded", "a.txt"))

		for _, k := range seeded {
			_, err := h.s3.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String("seeded"), Key: aws.String(k)})
			require.NoError(t, err)
		}
		h.putBytes("seeded", "extra.txt", []byte("x"))
		h.createBucket("scratch")
		h.putBytes("scratch", "k", []byte("v"))
		require.NotEmpty(t, h.requests(""))

		h.reset("requests=true")
		require.Empty(t, h.requests(""))

		require.Equal(t, seeded, h.listKeys("seeded", ""))
		require.Equal(t, []byte(`{"b":true}`), h.getBytes("seeded", "nested/b.json"))
		_, err := h.s3.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String("scratch")})
		requireAPIError(t, err, 404, "")
	})
}

func TestResetSingleBucket(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st config.Storage) {
		dir := seedDir(t)
		h := startServer(t, st, func(c *config.Config) {
			c.Buckets = []string{"seeded"}
			c.SeedDir = dir
		})
		ctx := t.Context()

		_, err := h.s3.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String("seeded"), Key: aws.String("a.txt")})
		require.NoError(t, err)
		h.putBytes("seeded", "extra.txt", []byte("x"))
		h.createBucket("other")
		h.putBytes("other", "keep", []byte("v"))

		h.reset("bucket=seeded")

		require.Equal(t, []string{"a.txt", "nested/b.json"}, h.listKeys("seeded", ""))
		require.Equal(t, []string{"keep"}, h.listKeys("other", ""))
		require.NotEmpty(t, h.requests(""), "request log kept without requests=true")
	})
}
