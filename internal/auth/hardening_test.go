package auth

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnsignedAmzHeaders(t *testing.T) {
	t.Run("added after signing", func(t *testing.T) {
		r := signSpec{path: "/b/k"}.sign(t)
		r.Header.Set("X-Amz-Meta-Foo", "bar")
		_, err := newVerifier().Verify(r)
		requireErr(t, err, ErrHeadersNotSigned)
	})
	t.Run("signed amz headers pass", func(t *testing.T) {
		r := signSpec{path: "/b/k", headers: map[string]string{"X-Amz-Meta-Foo": "bar", "X-Amz-Trailer": "x-amz-checksum-crc32"}}.sign(t)
		_, err := newVerifier().Verify(r)
		require.NoError(t, err)
	})
	t.Run("presigned ignores extra headers", func(t *testing.T) {
		r := presign(t, "/b/k", "X-Amz-Expires=900", testNow, testSecret)
		r.Header.Set("X-Amz-Meta-Foo", "bar")
		_, err := newVerifier().Verify(r)
		require.NoError(t, err)
	})
	t.Run("non amz extra header ok", func(t *testing.T) {
		r := signSpec{path: "/b/k"}.sign(t)
		r.Header.Set("User-Agent", "x")
		_, err := newVerifier().Verify(r)
		require.NoError(t, err)
	})
}

func TestCanonicalURIEscapedSlash(t *testing.T) {
	for _, raw := range []string{"/bucket/a%2Fb", "/bucket/a%2Fb/c%20d"} {
		t.Run(raw, func(t *testing.T) {
			u, err := url.Parse("http://" + testHost + raw)
			require.NoError(t, err)
			req, err := http.NewRequest(http.MethodGet, u.String(), http.NoBody)
			require.NoError(t, err)
			h := sha("")
			req.Header.Set("X-Amz-Content-Sha256", h)
			require.NoError(t, v4.NewSigner().SignHTTP(context.Background(), sdkCreds(testSecret), req, h, "s3", testRegion, testNow, sdkOpts))
			_, err = newVerifier().Verify(serverRequest(req, ""))
			require.NoError(t, err)
		})
	}
	u, err := url.Parse("http://h/b/a%2Fb/c%20d//e")
	require.NoError(t, err)
	assert.Equal(t, "/b/a%2Fb/c%20d//e", canonicalURI(u))
	u, err = url.Parse("http://h/b/%41%7e/a%2fb")
	require.NoError(t, err)
	assert.Equal(t, "/b/A~/a%2Fb", canonicalURI(u))
	bad := &url.URL{Path: "/b/%zz", RawPath: "/b/%zz"}
	assert.Equal(t, "/b/%25zz", canonicalURI(bad))
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

func TestChunkReaderPropagatesErrors(t *testing.T) {
	sentinel := &http.MaxBytesError{Limit: 5}
	cases := map[string]io.Reader{
		"line":  errReader{sentinel},
		"data":  io.MultiReader(strings.NewReader("5\r\nab"), errReader{sentinel}),
		"crlf":  io.MultiReader(strings.NewReader("2\r\nab"), errReader{sentinel}),
		"trail": io.MultiReader(strings.NewReader("0\r\n"), errReader{sentinel}),
	}
	for name, rd := range cases {
		t.Run(name, func(t *testing.T) {
			c := newChunkReader(io.NopCloser(rd), chunkOpts{declared: -1})
			_, err := io.ReadAll(c)
			var mbe *http.MaxBytesError
			require.ErrorAs(t, err, &mbe)
			assert.Same(t, sentinel, mbe)
		})
	}
}

func TestChunkReaderEagerDeclaredLength(t *testing.T) {
	c := newChunkReader(io.NopCloser(strings.NewReader("3\r\nabc\r\n3\r\ndef\r\n0\r\n\r\n")), chunkOpts{declared: 2})
	_, err := io.ReadAll(c)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decoded length")
	_, err = c.Read(make([]byte, 1))
	assert.Contains(t, err.Error(), "decoded length")
}
