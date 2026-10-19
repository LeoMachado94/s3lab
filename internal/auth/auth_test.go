package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/smithy-go/encoding/httpbinding"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeoMachado94/s3lab/internal/s3op"
)

const (
	testAK     = "AKIDEXAMPLE"
	testSecret = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
	testRegion = "us-east-1"
	testHost   = "s3.local"
)

var testNow = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

func newVerifier(opts ...Option) *Verifier {
	opts = append([]Option{WithClock(func() time.Time { return testNow })}, opts...)
	return NewVerifier(StaticCredentials{testAK: testSecret}, testRegion, opts...)
}

func sdkCreds(secret string) aws.Credentials {
	return aws.Credentials{AccessKeyID: testAK, SecretAccessKey: secret}
}

func sdkOpts(o *v4.SignerOptions) { o.DisableURIPathEscaping = true }

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func target(path, query string) string {
	p := httpbinding.EscapePath(path, false)
	if query != "" {
		p += "?" + query
	}
	return p
}

type signSpec struct {
	name    string
	method  string
	path    string
	query   string
	body    string
	hash    string
	headers map[string]string
	region  string
	secret  string
	at      time.Time
}

func (s signSpec) sign(t *testing.T) *http.Request {
	t.Helper()
	if s.method == "" {
		s.method = http.MethodGet
	}
	if s.hash == "" {
		s.hash = sha(s.body)
	}
	if s.region == "" {
		s.region = testRegion
	}
	if s.secret == "" {
		s.secret = testSecret
	}
	if s.at.IsZero() {
		s.at = testNow
	}
	var rd io.Reader = http.NoBody
	if s.body != "" {
		rd = strings.NewReader(s.body)
	}
	req, err := http.NewRequest(s.method, "http://"+testHost+target(s.path, s.query), rd)
	require.NoError(t, err)
	req.Header.Set("X-Amz-Content-Sha256", s.hash)
	for k, v := range s.headers {
		req.Header.Set(k, v)
	}
	require.NoError(t, v4.NewSigner().SignHTTP(context.Background(), sdkCreds(s.secret), req, s.hash, "s3", s.region, s.at, sdkOpts))
	return serverRequest(req, s.body)
}

// serverRequest mimics what net/http hands the handler.
func serverRequest(src *http.Request, body string) *http.Request {
	r := httptest.NewRequest(src.Method, src.URL.String(), strings.NewReader(body))
	if body == "" {
		r.Body = http.NoBody
	}
	r.Header = src.Header.Clone()
	r.Host = testHost
	return r
}

func presign(t *testing.T, path, query string, at time.Time, secret string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+testHost+target(path, query), nil)
	require.NoError(t, err)
	signed, _, err := v4.NewSigner().PresignHTTP(context.Background(), sdkCreds(secret), req, unsignedPayload, "s3", testRegion, at, sdkOpts)
	require.NoError(t, err)
	r := httptest.NewRequest(http.MethodGet, signed, nil)
	r.Host = testHost
	return r
}

func requireErr(t *testing.T, err error, want *Error) {
	t.Helper()
	require.Error(t, err)
	assert.Same(t, want, err, "got %v want %v", err, want)
}

var signedCases = []signSpec{
	{name: "get", path: "/bucket/key.txt"},
	{name: "root", path: "/"},
	{
		name: "put with spaces", method: http.MethodPut, path: "/bucket/my file.txt", body: "hello world",
		headers: map[string]string{"X-Amz-Meta-Foo": "  a   b ", "Content-Type": "text/plain"},
	},
	{name: "unicode", path: "/bucket/dir/héllo ✓.txt"},
	{name: "plus equals", path: "/bucket/a+b=c&d,e;f.txt"},
	{name: "double slash", path: "/bucket//x//y"},
	{name: "delete", method: http.MethodDelete, path: "/bucket/key"},
	{name: "query specials", path: "/bucket", query: "list-type=2&prefix=a%20b%2Bc%2Fd&continuation-token=%C3%A9%3D&delimiter=%2F&empty="},
	{name: "query repeated and sorted", path: "/bucket", query: "z=1&a=2&a=1&uploads"},
	{name: "other region", path: "/bucket/k", region: "eu-west-3"},
}

func TestVerifyHeaderSigned(t *testing.T) {
	for _, tc := range signedCases {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.sign(t)
			res, err := newVerifier().Verify(r)
			require.NoError(t, err)
			assert.Equal(t, Result{AccessKey: testAK, Method: s3op.AuthHeader}, res)
			if tc.body != "" {
				got, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				assert.Equal(t, tc.body, string(got))
			}
		})
	}
}

func TestVerifyHeaderTamper(t *testing.T) {
	base := signSpec{
		method: http.MethodPut, path: "/bucket/my file.txt", query: "a=1", body: "hi",
		headers: map[string]string{"X-Amz-Meta-Foo": "bar"},
	}

	tests := []struct {
		name   string
		mutate func(r *http.Request)
		want   *Error
	}{
		{"path", func(r *http.Request) { r.URL.Path = "/bucket/other.txt"; r.URL.RawPath = "" }, ErrSignatureDoesNotMatch},
		{"query", func(r *http.Request) { r.URL.RawQuery = "a=2" }, ErrSignatureDoesNotMatch},
		{"query added", func(r *http.Request) { r.URL.RawQuery += "&b=1" }, ErrSignatureDoesNotMatch},
		{"method", func(r *http.Request) { r.Method = http.MethodPost }, ErrSignatureDoesNotMatch},
		{"signed header", func(r *http.Request) { r.Header.Set("X-Amz-Meta-Foo", "baz") }, ErrSignatureDoesNotMatch},
		{"host", func(r *http.Request) { r.Host = "evil.local" }, ErrSignatureDoesNotMatch},
		{"date within skew", func(r *http.Request) {
			r.Header.Set("X-Amz-Date", testNow.Add(time.Minute).Format(isoFormat))
		}, ErrSignatureDoesNotMatch},
		{"date header fallback", func(r *http.Request) {
			r.Header.Del("X-Amz-Date")
			r.Header.Set("Date", testNow.Format(http.TimeFormat))
		}, ErrSignatureDoesNotMatch},
		{"scope date mismatch", func(r *http.Request) {
			r.Header.Set("X-Amz-Date", testNow.Add(24*time.Hour).Format(isoFormat))
		}, ErrHeaderMalformed},
		{"bad date", func(r *http.Request) { r.Header.Set("X-Amz-Date", "garbage") }, ErrHeaderMalformed},
		{"missing date", func(r *http.Request) { r.Header.Del("X-Amz-Date") }, ErrMissingDate},
		{"skewed", func(r *http.Request) {
			r.Header.Set("X-Amz-Date", testNow.Add(-time.Hour).Format(isoFormat))
		}, ErrRequestTimeTooSkewed},
		{"future skew", func(r *http.Request) {
			r.Header.Set("X-Amz-Date", testNow.Add(time.Hour).Format(isoFormat))
		}, ErrRequestTimeTooSkewed},
		{"payload header missing", func(r *http.Request) { r.Header.Del("X-Amz-Content-Sha256") }, ErrHeaderMalformed},
		{"payload header changed", func(r *http.Request) {
			r.Header.Set("X-Amz-Content-Sha256", sha("other"))
		}, ErrSignatureDoesNotMatch},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := base.sign(t)
			tc.mutate(r)
			_, err := newVerifier().Verify(r)
			requireErr(t, err, tc.want)
		})
	}

	t.Run("wrong secret", func(t *testing.T) {
		r := signSpec{path: "/b/k", secret: "nope"}.sign(t)
		_, err := newVerifier().Verify(r)
		requireErr(t, err, ErrSignatureDoesNotMatch)
	})
	t.Run("custom skew", func(t *testing.T) {
		r := signSpec{path: "/b/k", at: testNow.Add(-time.Hour)}.sign(t)
		_, err := newVerifier(WithMaxSkew(2 * time.Hour)).Verify(r)
		require.NoError(t, err)
	})
}

func TestVerifyAuthorizationParsing(t *testing.T) {
	good := signSpec{path: "/b/k"}.sign(t)
	authz := good.Header.Get("Authorization")

	t.Run("compact and spaced", func(t *testing.T) {
		for _, h := range []string{
			strings.ReplaceAll(authz, ", ", ","),
			strings.ReplaceAll(authz, ", ", " ,  "),
		} {
			r := good.Clone(context.Background())
			r.Header.Set("Authorization", h)
			_, err := newVerifier().Verify(r)
			require.NoError(t, err, h)
		}
	})

	cred := "Credential=" + testAK + "/20261019/us-east-1/s3/aws4_request"
	sh := "SignedHeaders=host;x-amz-date"
	sig := "Signature=abcd"
	tests := []struct {
		name string
		h    string
		want *Error
	}{
		{"sigv2", "AWS " + testAK + ":abc", ErrUnsupportedSignature},
		{"other scheme", "Bearer abc", ErrUnsupportedSignature},
		{"no params", "AWS4-HMAC-SHA256", ErrHeaderMalformed},
		{"glued", "AWS4-HMAC-SHA256X " + cred, ErrHeaderMalformed},
		{"no equals", "AWS4-HMAC-SHA256 foo", ErrHeaderMalformed},
		{"missing credential", "AWS4-HMAC-SHA256 " + sh + "," + sig, ErrHeaderMalformed},
		{"missing signed headers", "AWS4-HMAC-SHA256 " + cred + "," + sig, ErrHeaderMalformed},
		{"missing signature", "AWS4-HMAC-SHA256 " + cred + "," + sh, ErrHeaderMalformed},
		{"host not signed", "AWS4-HMAC-SHA256 " + cred + ",SignedHeaders=x-amz-date," + sig, ErrHeaderMalformed},
		{"empty signed header", "AWS4-HMAC-SHA256 " + cred + ",SignedHeaders=host;;x-amz-date," + sig, ErrHeaderMalformed},
		{"bad scope parts", "AWS4-HMAC-SHA256 Credential=" + testAK + "/20261019/us-east-1/s3," + sh + "," + sig, ErrHeaderMalformed},
		{"wrong service", "AWS4-HMAC-SHA256 Credential=" + testAK + "/20261019/us-east-1/ec2/aws4_request," + sh + "," + sig, ErrHeaderMalformed},
		{"wrong terminator", "AWS4-HMAC-SHA256 Credential=" + testAK + "/20261019/us-east-1/s3/aws4_x," + sh + "," + sig, ErrHeaderMalformed},
		{"unknown key", "AWS4-HMAC-SHA256 Credential=NOPE/20261019/us-east-1/s3/aws4_request," + sh + "," + sig, ErrInvalidAccessKeyID},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := good.Clone(context.Background())
			r.Header.Set("Authorization", tc.h)
			_, err := newVerifier().Verify(r)
			requireErr(t, err, tc.want)
		})
	}
}

func TestVerifyAnonymous(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "http://"+testHost+"/b/k", nil)

	res, err := newVerifier(WithAnonymous(true)).Verify(r)
	require.NoError(t, err)
	assert.Equal(t, Result{Method: s3op.AuthNone}, res)

	_, err = newVerifier().Verify(r)
	requireErr(t, err, ErrAccessDenied)
}

func TestVerifyBothAuthMechanisms(t *testing.T) {
	r := presign(t, "/b/k", "X-Amz-Expires=60", testNow, testSecret)
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 x=y")
	_, err := newVerifier().Verify(r)
	requireErr(t, err, ErrQueryMalformed)
}

func TestVerifyPresigned(t *testing.T) {
	keys := []struct{ name, path, query string }{
		{"plain", "/bucket/key.txt", ""},
		{"spaces", "/bucket/my file.txt", ""},
		{"unicode", "/bucket/héllo ✓.txt", ""},
		{"specials", "/bucket/a+b=c&d.txt", ""},
		{"double slash", "/bucket//x", ""},
		{"query", "/bucket", "prefix=a%20b%2Bc&response-content-disposition=attachment%3B%20filename%3D%22x%22"},
	}
	for _, tc := range keys {
		t.Run(tc.name, func(t *testing.T) {
			q := "X-Amz-Expires=900"
			if tc.query != "" {
				q += "&" + tc.query
			}
			r := presign(t, tc.path, q, testNow, testSecret)
			res, err := newVerifier().Verify(r)
			require.NoError(t, err)
			assert.Equal(t, Result{AccessKey: testAK, Method: s3op.AuthPresigned}, res)
		})
	}

	mk := func() *http.Request { return presign(t, "/bucket/k", "X-Amz-Expires=900", testNow, testSecret) }
	tests := []struct {
		name   string
		clock  time.Time
		mutate func(r *http.Request)
		want   *Error
	}{
		{"expiry boundary ok", testNow.Add(900 * time.Second), nil, nil},
		{"expired", testNow.Add(901 * time.Second), nil, ErrExpiredPresign},
		{"not yet valid", testNow.Add(-time.Hour), nil, ErrRequestTimeTooSkewed},
		{"tampered path", testNow, func(r *http.Request) { r.URL.Path = "/bucket/x"; r.URL.RawPath = "" }, ErrSignatureDoesNotMatch},
		{"tampered query", testNow, func(r *http.Request) { r.URL.RawQuery += "&x=1" }, ErrSignatureDoesNotMatch},
		{"tampered expires", testNow, func(r *http.Request) { setQuery(r, "X-Amz-Expires", "901") }, ErrSignatureDoesNotMatch},
		{"expires zero", testNow, func(r *http.Request) { setQuery(r, "X-Amz-Expires", "0") }, ErrQueryMalformed},
		{"expires too big", testNow, func(r *http.Request) { setQuery(r, "X-Amz-Expires", "604801") }, ErrQueryMalformed},
		{"expires nan", testNow, func(r *http.Request) { setQuery(r, "X-Amz-Expires", "x") }, ErrQueryMalformed},
		{"bad algorithm", testNow, func(r *http.Request) { setQuery(r, "X-Amz-Algorithm", "AWS4-HMAC-SHA1") }, ErrQueryMalformed},
		{"bad credential", testNow, func(r *http.Request) { setQuery(r, "X-Amz-Credential", "x") }, ErrQueryMalformed},
		{"missing signature", testNow, func(r *http.Request) { delQuery(r, "X-Amz-Signature") }, ErrQueryMalformed},
		{"missing signed headers", testNow, func(r *http.Request) { delQuery(r, "X-Amz-SignedHeaders") }, ErrQueryMalformed},
		{"missing date", testNow, func(r *http.Request) { delQuery(r, "X-Amz-Date") }, ErrQueryMalformed},
		{"bad date", testNow, func(r *http.Request) { setQuery(r, "X-Amz-Date", "nope") }, ErrQueryMalformed},
		{"scope date mismatch", testNow, func(r *http.Request) {
			setQuery(r, "X-Amz-Date", testNow.Add(24*time.Hour).Format(isoFormat))
		}, ErrQueryMalformed},
		{"unknown key", testNow, func(r *http.Request) {
			setQuery(r, "X-Amz-Credential", "NOPE/20261019/us-east-1/s3/aws4_request")
		}, ErrInvalidAccessKeyID},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := mk()
			if tc.mutate != nil {
				tc.mutate(r)
			}
			clock := tc.clock
			v := NewVerifier(StaticCredentials{testAK: testSecret}, testRegion, WithClock(func() time.Time { return clock }))
			_, err := v.Verify(r)
			if tc.want == nil {
				require.NoError(t, err)
				return
			}
			requireErr(t, err, tc.want)
		})
	}

	t.Run("wrong secret", func(t *testing.T) {
		r := presign(t, "/bucket/k", "X-Amz-Expires=900", testNow, "nope")
		_, err := newVerifier().Verify(r)
		requireErr(t, err, ErrSignatureDoesNotMatch)
	})
}

func setQuery(r *http.Request, k, v string) {
	q := r.URL.Query()
	q.Set(k, v)
	r.URL.RawQuery = q.Encode()
}

func delQuery(r *http.Request, k string) {
	q := r.URL.Query()
	q.Del(k)
	r.URL.RawQuery = q.Encode()
}

func TestPayloadHash(t *testing.T) {
	t.Run("matching hash", func(t *testing.T) {
		r := signSpec{method: http.MethodPut, path: "/b/k", body: "hello"}.sign(t)
		_, err := newVerifier().Verify(r)
		require.NoError(t, err)
		got, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		assert.Equal(t, "hello", string(got))
		require.NoError(t, r.Body.Close())
	})

	t.Run("mismatch at EOF", func(t *testing.T) {
		r := signSpec{method: http.MethodPut, path: "/b/k", body: "HELLO", hash: sha("hello")}.sign(t)
		r.Header.Set("Content-Length", "5")
		r.ContentLength = -1
		_, err := newVerifier().Verify(r)
		require.NoError(t, err)
		_, err = io.ReadAll(r.Body)
		assert.Same(t, ErrContentSHA256Mismatch, err)
		_, err = r.Body.Read(make([]byte, 1))
		assert.Same(t, ErrContentSHA256Mismatch, err)
	})

	t.Run("mismatch with known length without EOF", func(t *testing.T) {
		r := signSpec{method: http.MethodPut, path: "/b/k", body: "HELLO", hash: sha("hello")}.sign(t)
		_, err := newVerifier().Verify(r)
		require.NoError(t, err)
		n, err := r.Body.Read(make([]byte, 5))
		assert.Equal(t, 5, n)
		assert.Same(t, ErrContentSHA256Mismatch, err)
	})

	t.Run("one byte reads", func(t *testing.T) {
		r := signSpec{method: http.MethodPut, path: "/b/k", body: "HELLO", hash: sha("hello")}.sign(t)
		r.Header.Set("Content-Length", "5")
		r.ContentLength = -1
		_, err := newVerifier().Verify(r)
		require.NoError(t, err)
		_, err = io.ReadAll(iotest.OneByteReader(r.Body))
		assert.Same(t, ErrContentSHA256Mismatch, err)
	})

	t.Run("empty body wrong hash", func(t *testing.T) {
		r := signSpec{path: "/b/k", hash: sha("x")}.sign(t)
		_, err := newVerifier().Verify(r)
		requireErr(t, err, ErrContentSHA256Mismatch)
	})

	t.Run("empty body right hash", func(t *testing.T) {
		r := signSpec{path: "/b/k"}.sign(t)
		_, err := newVerifier().Verify(r)
		require.NoError(t, err)
	})

	t.Run("unsigned payload", func(t *testing.T) {
		r := signSpec{method: http.MethodPut, path: "/b/k", body: "anything", hash: unsignedPayload}.sign(t)
		_, err := newVerifier().Verify(r)
		require.NoError(t, err)
		got, _ := io.ReadAll(r.Body)
		assert.Equal(t, "anything", string(got))
	})

	t.Run("unknown payload value", func(t *testing.T) {
		r := signSpec{path: "/b/k", hash: "FOO"}.sign(t)
		_, err := newVerifier().Verify(r)
		requireErr(t, err, ErrHeaderMalformed)
	})

	t.Run("signed content-length", func(t *testing.T) {
		r := signSpec{method: http.MethodPut, path: "/b/k", body: "hello"}.sign(t)
		assert.Contains(t, r.Header.Get("Authorization"), "content-length")
		_, err := newVerifier().Verify(r)
		require.NoError(t, err)
	})
}

// --- aws-chunked ---

func chunkSig(key []byte, amzDate, scope, prev string, data string) string {
	sts := "AWS4-HMAC-SHA256-PAYLOAD\n" + amzDate + "\n" + scope + "\n" + prev + "\n" + sha("") + "\n" + sha(data)
	m := hmac.New(sha256.New, key)
	m.Write([]byte(sts))
	return hex.EncodeToString(m.Sum(nil))
}

func testSigningKey() []byte {
	k := []byte("AWS4" + testSecret)
	for _, s := range []string{testNow.Format(shortFormat), testRegion, "s3", "aws4_request"} {
		m := hmac.New(sha256.New, k)
		m.Write([]byte(s))
		k = m.Sum(nil)
	}
	return k
}

func seedFrom(r *http.Request) string {
	a := r.Header.Get("Authorization")
	return a[strings.LastIndex(a, "Signature=")+len("Signature="):]
}

func signedChunks(seed string, chunks []string, trailer string) string {
	scope := testNow.Format(shortFormat) + "/" + testRegion + "/s3/aws4_request"
	date := testNow.Format(isoFormat)
	key := testSigningKey()
	var b strings.Builder
	prev := seed
	for _, c := range append(chunks, "") {
		sig := chunkSig(key, date, scope, prev, c)
		b.WriteString(strconv.FormatInt(int64(len(c)), 16) + ";chunk-signature=" + sig + "\r\n")
		if c != "" {
			b.WriteString(c + "\r\n")
		}
		prev = sig
	}
	b.WriteString(trailer + "\r\n")
	return b.String()
}

func streamingRequest(t *testing.T, hash string, decoded string, build func(seed string) string) *http.Request {
	t.Helper()
	spec := signSpec{method: http.MethodPut, path: "/b/k", hash: hash, headers: map[string]string{
		"Content-Encoding": "aws-chunked",
	}}
	if decoded != "" {
		spec.headers["X-Amz-Decoded-Content-Length"] = decoded
	}
	r := spec.sign(t)
	body := build(seedFrom(r))
	r.Body = io.NopCloser(strings.NewReader(body))
	r.ContentLength = int64(len(body))
	return r
}

func TestStreamingSigned(t *testing.T) {
	chunks := []string{strings.Repeat("a", 100), "bcd", strings.Repeat("e", 1000)}
	payload := strings.Join(chunks, "")
	decoded := strconv.Itoa(len(payload))

	t.Run("valid", func(t *testing.T) {
		r := streamingRequest(t, streamSigned, decoded, func(seed string) string { return signedChunks(seed, chunks, "") })
		res, err := newVerifier().Verify(r)
		require.NoError(t, err)
		assert.Equal(t, s3op.AuthHeader, res.Method)
		assert.EqualValues(t, len(payload), r.ContentLength)
		got, err := io.ReadAll(iotest.OneByteReader(r.Body))
		require.NoError(t, err)
		assert.Equal(t, payload, string(got))
		assert.Equal(t, "aws-chunked", r.Header.Get("Content-Encoding"))
	})

	t.Run("valid empty", func(t *testing.T) {
		r := streamingRequest(t, streamSigned, "0", func(seed string) string { return signedChunks(seed, nil, "") })
		_, err := newVerifier().Verify(r)
		require.NoError(t, err)
		got, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("no decoded length", func(t *testing.T) {
		r := streamingRequest(t, streamSigned, "", func(seed string) string { return signedChunks(seed, chunks, "") })
		_, err := newVerifier().Verify(r)
		require.NoError(t, err)
		assert.EqualValues(t, -1, r.ContentLength)
		got, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		assert.Equal(t, payload, string(got))
	})

	t.Run("tampered data", func(t *testing.T) {
		r := streamingRequest(t, streamSigned, decoded, func(seed string) string {
			s := signedChunks(seed, chunks, "")
			return strings.Replace(s, "bcd", "bcX", 1)
		})
		_, err := newVerifier().Verify(r)
		require.NoError(t, err)
		_, err = io.ReadAll(r.Body)
		assert.Same(t, ErrSignatureDoesNotMatch, err)
	})

	t.Run("reordered chunks", func(t *testing.T) {
		r := streamingRequest(t, streamSigned, decoded, func(seed string) string {
			return signedChunks(seed, chunks, "")
		})
		raw, _ := io.ReadAll(r.Body)
		s := string(raw)
		i := strings.Index(s, "3;chunk-signature=")
		j := strings.Index(s, "3e8;chunk-signature=")
		a, b := s[:i], s[i:j]
		first := strings.SplitN(a, "\r\n", 3)
		swapped := b + first[0] + "\r\n" + first[1] + "\r\n" + s[j:]
		r.Body = io.NopCloser(strings.NewReader(swapped))
		_, err := newVerifier().Verify(r)
		require.NoError(t, err)
		_, err = io.ReadAll(r.Body)
		assert.Same(t, ErrSignatureDoesNotMatch, err)
	})

	t.Run("wrong seed", func(t *testing.T) {
		r := streamingRequest(t, streamSigned, decoded, func(string) string {
			return signedChunks(strings.Repeat("0", 64), chunks, "")
		})
		_, err := newVerifier().Verify(r)
		require.NoError(t, err)
		_, err = io.ReadAll(r.Body)
		assert.Same(t, ErrSignatureDoesNotMatch, err)
	})

	t.Run("truncated", func(t *testing.T) {
		r := streamingRequest(t, streamSigned, decoded, func(seed string) string {
			s := signedChunks(seed, chunks, "")
			return s[:len(s)/2]
		})
		_, err := newVerifier().Verify(r)
		require.NoError(t, err)
		_, err = io.ReadAll(r.Body)
		assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
	})

	t.Run("decoded length mismatch", func(t *testing.T) {
		r := streamingRequest(t, streamSigned, "5", func(seed string) string { return signedChunks(seed, chunks, "") })
		_, err := newVerifier().Verify(r)
		require.NoError(t, err)
		_, err = io.ReadAll(r.Body)
		require.Error(t, err)
	})

	t.Run("bad decoded length header", func(t *testing.T) {
		r := streamingRequest(t, streamSigned, "abc", func(seed string) string { return signedChunks(seed, chunks, "") })
		_, err := newVerifier().Verify(r)
		requireErr(t, err, ErrHeaderMalformed)
	})

	t.Run("signed trailer variant", func(t *testing.T) {
		trailer := "x-amz-checksum-crc32:AAAAAA==\r\nx-amz-trailer-signature:" + strings.Repeat("0", 64) + "\r\n"
		r := streamingRequest(t, streamSignedTrailer, decoded, func(seed string) string {
			return signedChunks(seed, chunks, trailer)
		})
		_, err := newVerifier().Verify(r)
		require.NoError(t, err)
		got, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		assert.Equal(t, payload, string(got))
	})
}

func TestStreamingUnsignedTrailer(t *testing.T) {
	body := "5\r\nhello\r\n6\r\n world\r\n0\r\nx-amz-checksum-crc32:AAAAAA==\r\n\r\n"
	r := streamingRequest(t, streamUnsignedTrailer, "11", func(string) string { return body })
	r.Header.Set("X-Amz-Trailer", "x-amz-checksum-crc32")
	_, err := newVerifier().Verify(r)
	require.NoError(t, err)
	assert.EqualValues(t, 11, r.ContentLength)
	got, err := io.ReadAll(r.Body)
	require.NoError(t, err)
	assert.Equal(t, "hello world", string(got))
	require.NoError(t, r.Body.Close())

	t.Run("no trailer and unknown length", func(t *testing.T) {
		r := streamingRequest(t, streamUnsignedTrailer, "", func(string) string { return "3\r\nabc\r\n0\r\n\r\n" })
		_, err := newVerifier().Verify(r)
		require.NoError(t, err)
		got, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		assert.Equal(t, "abc", string(got))
	})
}

func TestChunkReaderFraming(t *testing.T) {
	big := strings.Repeat("x-amz-checksum-crc32:"+strings.Repeat("A", 200)+"\r\n", 100)
	tests := []struct {
		name    string
		in      string
		signed  bool
		declare int64
		wantErr string
		wantOut string
	}{
		{"ok", "3\r\nabc\r\n0\r\n\r\n", false, -1, "", "abc"},
		{"ok declared", "3\r\nabc\r\n0\r\n\r\n", false, 3, "", "abc"},
		{"chunk extension ignored", "3;foo=bar\r\nabc\r\n0\r\n\r\n", false, -1, "", "abc"},
		{"bad hex", "G\r\nabc\r\n", false, -1, "invalid chunk size", ""},
		{"empty size", "\r\n", false, -1, "invalid chunk size", ""},
		{"size line too long", strings.Repeat("1", 17) + "\r\n", false, -1, "invalid chunk size", ""},
		{"size too big", "5000000\r\nxxxx", false, -1, "chunk too large", ""},
		{"header too long", "3;" + strings.Repeat("a", 600) + "\r\nabc", false, -1, "header too long", ""},
		{"line way too long", strings.Repeat("a", 9000), false, -1, "line too long", ""},
		{"missing crlf after data", "3\r\nabcXX0\r\n\r\n", false, -1, "missing CRLF after", ""},
		{"bare LF", "3\nabc", false, -1, "missing CRLF", ""},
		{"truncated data", "5\r\nhel", false, -1, "unexpected EOF", "hel"},
		{"truncated crlf", "3\r\nabc", false, -1, "unexpected EOF", ""},
		{"empty", "", false, -1, "unexpected EOF", ""},
		{"no terminator", "0\r\n", false, -1, "unexpected EOF", ""},
		{"trailer too large", "0\r\n" + big + "\r\n", false, -1, "trailer too large", ""},
		{"declared mismatch", "3\r\nabc\r\n0\r\n\r\n", false, 5, "decoded length", "abc"},
		{"signed without signature", "3\r\nabc\r\n", true, -1, "missing chunk signature", ""},
		{"signed with empty signature", "3;chunk-signature=\r\nabc\r\n", true, -1, "missing chunk signature", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newChunkReader(io.NopCloser(strings.NewReader(tc.in)), chunkOpts{signed: tc.signed, declared: tc.declare})
			out, err := io.ReadAll(c)
			assert.Equal(t, tc.wantOut, string(out))
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			_, again := c.Read(make([]byte, 1))
			assert.Error(t, again)
		})
	}

	t.Run("zero length read", func(t *testing.T) {
		c := newChunkReader(io.NopCloser(strings.NewReader("0\r\n\r\n")), chunkOpts{declared: -1})
		n, err := c.Read(nil)
		assert.Zero(t, n)
		assert.NoError(t, err)
	})

	t.Run("max chunk size accepted", func(t *testing.T) {
		c := newChunkReader(io.NopCloser(strings.NewReader("4000000\r\nab")), chunkOpts{declared: -1})
		_, err := io.ReadAll(c)
		assert.True(t, errors.Is(err, io.ErrUnexpectedEOF))
	})
}

func TestMiddleware(t *testing.T) {
	var gotInfo *s3op.Info
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	onErr := func(w http.ResponseWriter, _ *http.Request, err error) {
		var ae *Error
		require.True(t, errors.As(err, &ae))
		w.WriteHeader(ae.HTTPStatus())
	}
	h := Middleware(newVerifier(), onErr)(next)

	run := func(r *http.Request, op s3op.Op) (*httptest.ResponseRecorder, *s3op.Info) {
		gotInfo = &s3op.Info{Op: op}
		r = r.WithContext(s3op.WithInfo(r.Context(), gotInfo))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w, gotInfo
	}

	w, info := run(signSpec{path: "/b/k"}.sign(t), s3op.GetObject)
	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Equal(t, testAK, info.AccessKey)
	assert.Equal(t, s3op.AuthHeader, info.Auth)

	w, info = run(httptest.NewRequest(http.MethodGet, "http://"+testHost+"/b/k", nil), s3op.GetObject)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, s3op.AuthFailed, info.Auth)

	w, _ = run(httptest.NewRequest(http.MethodOptions, "http://"+testHost+"/b/k", nil), s3op.CORSPreflight)
	assert.Equal(t, http.StatusNoContent, w.Code)
}

func TestErrorInterface(t *testing.T) {
	assert.Equal(t, "AccessDenied: Access Denied", ErrAccessDenied.Error())
	assert.Equal(t, "SignatureDoesNotMatch", ErrSignatureDoesNotMatch.S3Code())
	assert.Equal(t, http.StatusForbidden, ErrSignatureDoesNotMatch.HTTPStatus())
}

func TestAWSEncode(t *testing.T) {
	assert.Equal(t, "a%20b/%C3%A9%2B%3D~-_.", awsEncode("a b/é+=~-_.", true))
	assert.Equal(t, "a%2Fb", awsEncode("a/b", false))
}
