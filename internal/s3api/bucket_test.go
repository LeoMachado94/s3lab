package s3api

import (
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/require"
)

func bucketDecodeList(t *testing.T, body string) ListBucketResult {
	t.Helper()
	var out ListBucketResult
	require.NoError(t, xml.Unmarshal([]byte(body), &out), body)
	return out
}

func bucketSeed(t *testing.T, keys ...string) *Handler {
	t.Helper()
	h, b := newTestHandler(t)
	mustCreateBucket(t, b, "lab")
	for _, k := range keys {
		mustPut(t, b, "lab", k, "data")
	}
	return h
}

func TestListBuckets(t *testing.T) {
	h, b := newTestHandler(t)
	rec := do(t, h, testReq{method: "GET", target: "/"})
	require.Equal(t, 200, rec.Code)
	var empty ListAllMyBucketsResult
	require.NoError(t, xml.Unmarshal(rec.Body.Bytes(), &empty))
	require.Empty(t, empty.Buckets)

	mustCreateBucket(t, b, "zeta")
	mustCreateBucket(t, b, "alpha")
	rec = do(t, h, testReq{method: "GET", target: "/"})
	require.Equal(t, 200, rec.Code)
	var out ListAllMyBucketsResult
	require.NoError(t, xml.Unmarshal(rec.Body.Bytes(), &out))
	require.Equal(t, s3Namespace, out.Xmlns)
	require.Equal(t, defaultOwner, out.Owner)
	require.Len(t, out.Buckets, 2)
	require.Equal(t, "alpha", out.Buckets[0].Name)
	require.Equal(t, "zeta", out.Buckets[1].Name)
	require.Regexp(t, `^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z$`, out.Buckets[0].CreationDate)
}

func TestCreateBucket(t *testing.T) {
	cfg := `<CreateBucketConfiguration><LocationConstraint>eu-west-1</LocationConstraint></CreateBucketConfiguration>`
	tests := []struct {
		name    string
		target  string
		body    string
		headers map[string]string
		status  int
		code    string
	}{
		{"empty body", "/abc", "", nil, 200, ""},
		{"with location config", "/cfg", cfg, nil, 200, ""},
		{"garbage body ignored", "/junk", "not xml", nil, 200, ""},
		{"acl header ignored", "/acl-b", "", map[string]string{"x-amz-acl": "public-read"}, 200, ""},
		{"invalid name", "/ab", "", nil, 400, "InvalidBucketName"},
		{"object lock", "/lock", "", map[string]string{"x-amz-bucket-object-lock-enabled": "true"}, 501, "NotImplemented"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _ := newTestHandler(t)
			rec := do(t, h, testReq{method: "PUT", target: tt.target, body: strings.NewReader(tt.body), headers: tt.headers})
			if tt.code != "" {
				requireS3Error(t, rec, tt.status, tt.code)
				return
			}
			require.Equal(t, tt.status, rec.Code)
			require.Equal(t, tt.target, rec.Header().Get("Location"))
		})
	}

	t.Run("already exists", func(t *testing.T) {
		h, b := newTestHandler(t)
		mustCreateBucket(t, b, "dup")
		requireS3Error(t, do(t, h, testReq{method: "PUT", target: "/dup"}), 409, "BucketAlreadyOwnedByYou")
	})
}

func TestDeleteBucket(t *testing.T) {
	h, b := newTestHandler(t)
	mustCreateBucket(t, b, "gone")
	mustCreateBucket(t, b, "full")
	mustPut(t, b, "full", "k", "v")

	rec := do(t, h, testReq{method: "DELETE", target: "/gone"})
	require.Equal(t, 204, rec.Code)
	require.Empty(t, rec.Body.String())
	requireS3Error(t, do(t, h, testReq{method: "DELETE", target: "/gone"}), 404, "NoSuchBucket")
	requireS3Error(t, do(t, h, testReq{method: "DELETE", target: "/full"}), 409, "BucketNotEmpty")
}

func TestHeadBucket(t *testing.T) {
	h, b := newTestHandler(t, WithRegion("sa-east-1"))
	mustCreateBucket(t, b, "here")

	rec := do(t, h, testReq{method: "HEAD", target: "/here"})
	require.Equal(t, 200, rec.Code)
	require.Equal(t, "sa-east-1", rec.Header().Get("x-amz-bucket-region"))

	rec = do(t, h, testReq{method: "HEAD", target: "/missing"})
	require.Equal(t, 404, rec.Code)
	require.Empty(t, rec.Body.String())
}

func TestGetBucketLocation(t *testing.T) {
	tests := []struct {
		name, region, want string
	}{
		{"us-east-1 is empty", "us-east-1", ""},
		{"other region", "eu-central-1", "eu-central-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, b := newTestHandler(t, WithRegion(tt.region))
			mustCreateBucket(t, b, "loc")
			rec := do(t, h, testReq{method: "GET", target: "/loc?location"})
			require.Equal(t, 200, rec.Code)
			var out LocationConstraint
			require.NoError(t, xml.Unmarshal(rec.Body.Bytes(), &out))
			require.Equal(t, tt.want, out.Value)
		})
	}
	h, _ := newTestHandler(t)
	requireS3Error(t, do(t, h, testReq{method: "GET", target: "/nope?location"}), 404, "NoSuchBucket")
}

func TestListObjectsV2Basics(t *testing.T) {
	h := bucketSeed(t, "a.txt", "b/1", "b/2", "c/d/3", "z")

	t.Run("default", func(t *testing.T) {
		rec := do(t, h, testReq{method: "GET", target: "/lab?list-type=2"})
		require.Equal(t, 200, rec.Code)
		out := bucketDecodeList(t, rec.Body.String())
		require.Equal(t, s3Namespace, out.Xmlns)
		require.Equal(t, "lab", out.Name)
		require.Equal(t, 1000, out.MaxKeys)
		require.False(t, out.IsTruncated)
		require.Len(t, out.Contents, 5)
		require.Equal(t, 5, *out.KeyCount)
		require.Equal(t, "STANDARD", out.Contents[0].StorageClass)
		require.Regexp(t, `^"[0-9a-f]{32}"$`, out.Contents[0].ETag)
		require.Nil(t, out.Contents[0].Owner)
		require.Empty(t, out.NextContinuationToken)
	})

	t.Run("delimiter", func(t *testing.T) {
		rec := do(t, h, testReq{method: "GET", target: "/lab?list-type=2&delimiter=/"})
		out := bucketDecodeList(t, rec.Body.String())
		require.Equal(t, "/", out.Delimiter)
		require.Len(t, out.Contents, 2)
		require.Equal(t, []CommonPrefix{{"b/"}, {"c/"}}, out.CommonPrefixes)
		require.Equal(t, 4, *out.KeyCount)
	})

	t.Run("prefix and delimiter", func(t *testing.T) {
		rec := do(t, h, testReq{method: "GET", target: "/lab?list-type=2&prefix=c/&delimiter=/"})
		out := bucketDecodeList(t, rec.Body.String())
		require.Equal(t, "c/", out.Prefix)
		require.Empty(t, out.Contents)
		require.Equal(t, []CommonPrefix{{"c/d/"}}, out.CommonPrefixes)
	})

	t.Run("start-after echoed", func(t *testing.T) {
		rec := do(t, h, testReq{method: "GET", target: "/lab?list-type=2&start-after=b/1"})
		out := bucketDecodeList(t, rec.Body.String())
		require.Equal(t, "b/1", out.StartAfter)
		require.Len(t, out.Contents, 3)
		require.Equal(t, "b/2", out.Contents[0].Key)
	})

	t.Run("fetch-owner", func(t *testing.T) {
		rec := do(t, h, testReq{method: "GET", target: "/lab?list-type=2&fetch-owner=true&max-keys=1"})
		out := bucketDecodeList(t, rec.Body.String())
		require.NotNil(t, out.Contents[0].Owner)
		require.Equal(t, "s3lab", out.Contents[0].Owner.ID)
	})

	t.Run("max-keys zero", func(t *testing.T) {
		rec := do(t, h, testReq{method: "GET", target: "/lab?list-type=2&max-keys=0"})
		require.Equal(t, 200, rec.Code)
		out := bucketDecodeList(t, rec.Body.String())
		require.Empty(t, out.Contents)
		require.False(t, out.IsTruncated)
		require.Equal(t, 0, out.MaxKeys)
		require.Equal(t, 0, *out.KeyCount)
	})

	t.Run("max-keys capped", func(t *testing.T) {
		rec := do(t, h, testReq{method: "GET", target: "/lab?list-type=2&max-keys=5000"})
		require.Equal(t, 1000, bucketDecodeList(t, rec.Body.String()).MaxKeys)
	})
}

func TestListObjectsErrors(t *testing.T) {
	h := bucketSeed(t, "a")
	tests := []struct {
		name, target string
		status       int
		code         string
	}{
		{"v2 missing bucket", "/nope?list-type=2", 404, "NoSuchBucket"},
		{"v1 missing bucket", "/nope", 404, "NoSuchBucket"},
		{"v2 max-keys zero missing bucket", "/nope?list-type=2&max-keys=0", 404, "NoSuchBucket"},
		{"v2 bad max-keys", "/lab?list-type=2&max-keys=abc", 400, "InvalidArgument"},
		{"v2 negative max-keys", "/lab?list-type=2&max-keys=-1", 400, "InvalidArgument"},
		{"v1 bad max-keys", "/lab?max-keys=x", 400, "InvalidArgument"},
		{"bad token", "/lab?list-type=2&continuation-token=%21%21", 400, "InvalidArgument"},
		{"empty token", "/lab?list-type=2&continuation-token=", 400, "InvalidArgument"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireS3Error(t, do(t, h, testReq{method: "GET", target: tt.target}), tt.status, tt.code)
		})
	}
}

func TestListObjectsV2Pagination(t *testing.T) {
	var keys []string
	for i := range 25 {
		keys = append(keys, fmt.Sprintf("key-%02d", i))
	}
	h := bucketSeed(t, keys...)

	var got []string
	token := ""
	pages := 0
	for {
		target := "/lab?list-type=2&max-keys=10"
		if token != "" {
			target += "&continuation-token=" + url.QueryEscape(token)
		}
		out := bucketDecodeList(t, do(t, h, testReq{method: "GET", target: target}).Body.String())
		pages++
		require.Equal(t, token, out.ContinuationToken)
		for _, c := range out.Contents {
			got = append(got, c.Key)
		}
		if !out.IsTruncated {
			require.Empty(t, out.NextContinuationToken)
			break
		}
		require.NotEmpty(t, out.NextContinuationToken)
		token = out.NextContinuationToken
	}
	require.Equal(t, 3, pages)
	require.Equal(t, keys, got)
}

func TestListObjectsV2TokenPrecedence(t *testing.T) {
	h := bucketSeed(t, "a", "b", "c")
	first := bucketDecodeList(t, do(t, h, testReq{method: "GET", target: "/lab?list-type=2&max-keys=1"}).Body.String())
	target := "/lab?list-type=2&start-after=c&continuation-token=" + url.QueryEscape(first.NextContinuationToken)
	out := bucketDecodeList(t, do(t, h, testReq{method: "GET", target: target}).Body.String())
	require.Len(t, out.Contents, 2)
	require.Equal(t, "b", out.Contents[0].Key)
}

func TestListObjectsV2PaginationWithPrefixes(t *testing.T) {
	h := bucketSeed(t, "a/1", "a/2", "b/1", "c")
	var all []string
	token := ""
	for {
		target := "/lab?list-type=2&max-keys=1&delimiter=/"
		if token != "" {
			target += "&continuation-token=" + url.QueryEscape(token)
		}
		out := bucketDecodeList(t, do(t, h, testReq{method: "GET", target: target}).Body.String())
		for _, c := range out.Contents {
			all = append(all, c.Key)
		}
		for _, p := range out.CommonPrefixes {
			all = append(all, p.Prefix)
		}
		if !out.IsTruncated {
			break
		}
		token = out.NextContinuationToken
	}
	require.Equal(t, []string{"a/", "b/", "c"}, all)
}

func TestListObjectsEncodingTypeURL(t *testing.T) {
	keys := []string{"dir/with space.txt", "plus+sign", "ünï/çödé é.txt", "plain"}
	h := bucketSeed(t, keys...)

	for _, v := range []string{"2", "1"} {
		t.Run("v"+v, func(t *testing.T) {
			target := "/lab?encoding-type=url&delimiter=" + url.QueryEscape("/")
			if v == "2" {
				target += "&list-type=2&start-after=" + url.QueryEscape("a b")
			} else {
				target += "&marker=" + url.QueryEscape("a b")
			}
			rec := do(t, h, testReq{method: "GET", target: target})
			require.Equal(t, 200, rec.Code)
			body := rec.Body.String()
			require.Contains(t, body, "<EncodingType>url</EncodingType>")
			require.Contains(t, body, "<Key>plus%2Bsign</Key>")
			require.NotContains(t, body, "ünï")
			out := bucketDecodeList(t, body)
			require.Equal(t, "url", out.EncodingType)
			var decoded []string
			for _, c := range out.Contents {
				k, err := url.QueryUnescape(c.Key)
				require.NoError(t, err)
				decoded = append(decoded, k)
			}
			for _, p := range out.CommonPrefixes {
				k, err := url.QueryUnescape(p.Prefix)
				require.NoError(t, err)
				decoded = append(decoded, k)
			}
			require.ElementsMatch(t, []string{"plus+sign", "plain", "dir/", "ünï/"}, decoded)
			marker := out.StartAfter
			if v == "1" {
				marker = *out.Marker
			}
			require.Equal(t, "a+b", marker)
		})
	}

	t.Run("prefix with space", func(t *testing.T) {
		rec := do(t, h, testReq{method: "GET", target: "/lab?list-type=2&encoding-type=url&prefix=" + url.QueryEscape("dir/with ")})
		out := bucketDecodeList(t, rec.Body.String())
		require.Equal(t, "dir/with+", out.Prefix)
		require.Len(t, out.Contents, 1)
		require.Equal(t, "dir/with+space.txt", out.Contents[0].Key)
	})

	t.Run("without encoding raw keys", func(t *testing.T) {
		out := bucketDecodeList(t, do(t, h, testReq{method: "GET", target: "/lab?list-type=2"}).Body.String())
		require.Empty(t, out.EncodingType)
		var got []string
		for _, c := range out.Contents {
			got = append(got, c.Key)
		}
		require.ElementsMatch(t, keys, got)
	})
}

func TestListObjectsV1(t *testing.T) {
	var keys []string
	for i := range 25 {
		keys = append(keys, fmt.Sprintf("key-%02d", i))
	}
	h := bucketSeed(t, keys...)

	t.Run("empty marker echoed", func(t *testing.T) {
		rec := do(t, h, testReq{method: "GET", target: "/lab?max-keys=1"})
		require.Contains(t, rec.Body.String(), "<Marker></Marker>")
		out := bucketDecodeList(t, rec.Body.String())
		require.Nil(t, out.KeyCount)
		require.True(t, out.IsTruncated)
		require.Equal(t, "key-00", out.NextMarker)
	})

	t.Run("marker pagination", func(t *testing.T) {
		var got []string
		marker := ""
		pages := 0
		for {
			out := bucketDecodeList(t, do(t, h, testReq{method: "GET", target: "/lab?max-keys=10&marker=" + url.QueryEscape(marker)}).Body.String())
			pages++
			require.Equal(t, marker, *out.Marker)
			for _, c := range out.Contents {
				got = append(got, c.Key)
			}
			if !out.IsTruncated {
				require.Empty(t, out.NextMarker)
				break
			}
			marker = out.NextMarker
		}
		require.Equal(t, 3, pages)
		require.Equal(t, keys, got)
	})

	t.Run("delimiter", func(t *testing.T) {
		h := bucketSeed(t, "a/1", "b", "c/2")
		out := bucketDecodeList(t, do(t, h, testReq{method: "GET", target: "/lab?delimiter=/"}).Body.String())
		require.Len(t, out.Contents, 1)
		require.Equal(t, []CommonPrefix{{"a/"}, {"c/"}}, out.CommonPrefixes)
		require.False(t, out.IsTruncated)
	})

	t.Run("max-keys zero", func(t *testing.T) {
		out := bucketDecodeList(t, do(t, h, testReq{method: "GET", target: "/lab?max-keys=0"}).Body.String())
		require.Empty(t, out.Contents)
		require.False(t, out.IsTruncated)
	})
}

func TestBucketSDKEndToEnd(t *testing.T) {
	h, b := newTestHandler(t)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	client := s3.New(s3.Options{
		Region:       "us-east-1",
		BaseEndpoint: aws.String(srv.URL),
		UsePathStyle: true,
		Credentials:  aws.AnonymousCredentials{},
		HTTPClient:   srv.Client(),
	})
	ctx := t.Context()

	_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("sdk-bucket")})
	require.NoError(t, err)
	_, err = client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("another")})
	require.NoError(t, err)

	lb, err := client.ListBuckets(ctx, &s3.ListBucketsInput{})
	require.NoError(t, err)
	require.Len(t, lb.Buckets, 2)
	require.Equal(t, "another", aws.ToString(lb.Buckets[0].Name))

	_, err = client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String("sdk-bucket")})
	require.NoError(t, err)

	_, err = client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("sdk-bucket")})
	require.ErrorContains(t, err, "BucketAlreadyOwnedByYou")

	var want []string
	for i := range 25 {
		key := fmt.Sprintf("dir/file %02d+é.txt", i)
		want = append(want, key)
		mustPut(t, b, "sdk-bucket", key, "x")
	}

	pager := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{
		Bucket:       aws.String("sdk-bucket"),
		Prefix:       aws.String("dir/"),
		MaxKeys:      aws.Int32(10),
		EncodingType: s3types.EncodingTypeUrl,
	})
	var got []string
	pages := 0
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		pages++
		for _, o := range page.Contents {
			k, err := url.QueryUnescape(aws.ToString(o.Key))
			require.NoError(t, err)
			got = append(got, k)
		}
	}
	require.Equal(t, 3, pages)
	require.Equal(t, want, got)

	loc, err := client.GetBucketLocation(ctx, &s3.GetBucketLocationInput{Bucket: aws.String("sdk-bucket")})
	require.NoError(t, err)
	require.Empty(t, string(loc.LocationConstraint))

	_, err = client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String("missing")})
	var re *awshttp.ResponseError
	require.True(t, errors.As(err, &re))
	require.Equal(t, http.StatusNotFound, re.HTTPStatusCode())
}
