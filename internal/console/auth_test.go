package console

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newAuthEnv(t *testing.T) *env {
	t.Helper()
	return newEnv(t, func(d *Deps) {
		d.Credentials = &Credentials{AccessKey: "ak", SecretKey: "sk"}
		d.authDelay = time.Nanosecond
	})
}

func sessionFrom(t *testing.T, r resp) string {
	t.Helper()
	for _, c := range r.Result().Cookies() {
		if c.Name == sessionCookie {
			require.True(t, c.HttpOnly)
			require.Equal(t, http.SameSiteStrictMode, c.SameSite)
			return c.Name + "=" + c.Value
		}
	}
	t.Fatal("no session cookie")
	return ""
}

func TestAuthRequired(t *testing.T) {
	e := newAuthEnv(t)

	r := e.do(http.MethodGet, "/_s3lab/buckets", "")
	require.Equal(t, http.StatusUnauthorized, r.Code)
	require.Equal(t, "Unauthorized", r.errCode())
	require.Empty(t, r.Header().Get("WWW-Authenticate"), "no browser basic-auth popup")

	require.Equal(t, http.StatusOK, e.do(http.MethodGet, "/_s3lab/health", "").Code)
	require.Equal(t, http.StatusOK, e.do(http.MethodGet, "/", "").Code)
	require.Equal(t, http.StatusUnauthorized, e.do(http.MethodPost, "/_s3lab/reset", "").Code)
	require.Equal(t, http.StatusUnauthorized, e.do(http.MethodGet, "/_s3lab/requests/stream", "").Code)
}

func TestAuthBasic(t *testing.T) {
	e := newAuthEnv(t)
	basic := func(ak, sk string) string {
		req, _ := http.NewRequest(http.MethodGet, "/", nil)
		req.SetBasicAuth(ak, sk)
		return req.Header.Get("Authorization")
	}

	r := e.do(http.MethodGet, "/_s3lab/info", "", "Authorization", basic("ak", "sk"))
	require.Equal(t, http.StatusOK, r.Code)
	require.Equal(t, true, r.json()["auth"])

	require.Equal(t, http.StatusUnauthorized, e.do(http.MethodGet, "/_s3lab/info", "", "Authorization", basic("ak", "nope")).Code)
	require.Equal(t, http.StatusUnauthorized, e.do(http.MethodGet, "/_s3lab/info", "", "Authorization", basic("", "")).Code)
}

func TestAuthLoginSession(t *testing.T) {
	e := newAuthEnv(t)

	r := e.do(http.MethodPost, "/_s3lab/login", `{"accessKey":"ak","secretKey":"bad"}`)
	require.Equal(t, http.StatusUnauthorized, r.Code)
	require.Empty(t, r.Result().Cookies())
	require.Equal(t, http.StatusBadRequest, e.do(http.MethodPost, "/_s3lab/login", `{`).Code)

	r = e.do(http.MethodPost, "/_s3lab/login", `{"accessKey":"ak","secretKey":"sk"}`)
	require.Equal(t, http.StatusNoContent, r.Code)
	cookie := sessionFrom(t, r)

	require.Equal(t, http.StatusOK, e.do(http.MethodGet, "/_s3lab/buckets", "", "Cookie", cookie).Code)

	name, val, _ := strings.Cut(cookie, "=")
	exp, sig, _ := strings.Cut(val, ".")
	forged := name + "=" + exp + "0." + sig
	require.Equal(t, http.StatusUnauthorized, e.do(http.MethodGet, "/_s3lab/buckets", "", "Cookie", forged).Code)
	require.Equal(t, http.StatusUnauthorized, e.do(http.MethodGet, "/_s3lab/buckets", "", "Cookie", name+"=garbage").Code)

	r = e.do(http.MethodPost, "/_s3lab/logout", "")
	require.Equal(t, http.StatusNoContent, r.Code)
	require.Equal(t, -1, r.Result().Cookies()[0].MaxAge)
}

func TestAuthCrossOriginLogin(t *testing.T) {
	e := newAuthEnv(t)
	r := e.do(http.MethodPost, "/_s3lab/login", `{"accessKey":"ak","secretKey":"sk"}`, "Origin", "http://evil.example")
	require.Equal(t, http.StatusForbidden, r.Code)
}

func TestSessionExpiry(t *testing.T) {
	a := newAuthenticator(Credentials{AccessKey: "ak", SecretKey: "sk"})
	now := time.Unix(1_000_000, 0)
	a.now = func() time.Time { return now }
	v, _ := a.newSession()
	require.True(t, a.validSession(v))
	now = now.Add(sessionTTL)
	require.False(t, a.validSession(v))
	require.False(t, newAuthenticator(a.creds).validSession(v), "other process key")
}

func TestAuthDisabled(t *testing.T) {
	e := newEnv(t)
	r := e.do(http.MethodGet, "/_s3lab/info", "")
	require.Equal(t, http.StatusOK, r.Code)
	require.Equal(t, false, r.json()["auth"])
}
