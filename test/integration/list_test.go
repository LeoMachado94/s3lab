//go:build integration

package integration

import (
	"fmt"
	"sort"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"

	"github.com/LeoMachado94/s3lab/internal/config"
)

func TestListObjectsV2(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st config.Storage) {
		h := startServer(t, st, nil)
		ctx := t.Context()
		h.createBucket("list")

		const total = 1200
		want := make([]string, 0, total)
		for i := range total {
			want = append(want, fmt.Sprintf("bulk/%04d", i))
		}
		var wg sync.WaitGroup
		sem := make(chan struct{}, 16)
		errs := make(chan error, total)
		for _, k := range want {
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				_, err := h.s3.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String("list"), Key: aws.String(k), Body: bytesReader([]byte(k))})
				errs <- err
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			require.NoError(t, err)
		}

		var got []string
		pages := 0
		p := s3.NewListObjectsV2Paginator(h.s3, &s3.ListObjectsV2Input{
			Bucket: aws.String("list"), Prefix: aws.String("bulk/"), MaxKeys: aws.Int32(500),
		})
		for p.HasMorePages() {
			page, err := p.NextPage(ctx)
			require.NoError(t, err)
			require.LessOrEqual(t, len(page.Contents), 500)
			pages++
			for _, o := range page.Contents {
				got = append(got, aws.ToString(o.Key))
			}
		}
		require.Equal(t, 3, pages)
		require.True(t, sort.StringsAreSorted(got))
		require.Equal(t, want, got)

		require.Len(t, h.waitRequests("op=ListObjectsV2", 3), 3)

		for _, k := range []string{"tree/a/1", "tree/a/2", "tree/b/1", "tree/c.txt"} {
			h.putBytes("list", k, []byte(k))
		}
		out, err := h.s3.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket: aws.String("list"), Prefix: aws.String("tree/"), Delimiter: aws.String("/"),
		})
		require.NoError(t, err)
		var prefixes, keys []string
		for _, cp := range out.CommonPrefixes {
			prefixes = append(prefixes, aws.ToString(cp.Prefix))
		}
		for _, o := range out.Contents {
			keys = append(keys, aws.ToString(o.Key))
		}
		require.Equal(t, []string{"tree/a/", "tree/b/"}, prefixes)
		require.Equal(t, []string{"tree/c.txt"}, keys)

		after, err := h.s3.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket: aws.String("list"), Prefix: aws.String("tree/"), StartAfter: aws.String("tree/a/2"),
		})
		require.NoError(t, err)
		keys = nil
		for _, o := range after.Contents {
			keys = append(keys, aws.ToString(o.Key))
		}
		require.Equal(t, []string{"tree/b/1", "tree/c.txt"}, keys)
	})
}
