package console

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	sessionCookie = "s3lab_session"
	sessionTTL    = 12 * time.Hour
	failureDelay  = time.Second
)

// Credentials protect the console; the UI logs in with them and API clients
// send them as HTTP Basic auth.
type Credentials struct {
	AccessKey string
	SecretKey string
}

type authenticator struct {
	creds Credentials
	// key signs session cookies; random per process, so restarts log everyone out.
	key   []byte
	now   func() time.Time
	delay time.Duration
}

func newAuthenticator(c Credentials) *authenticator {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(err)
	}
	return &authenticator{creds: c, key: key, now: time.Now, delay: failureDelay}
}

func (a *authenticator) match(accessKey, secretKey string) bool {
	ak := subtle.ConstantTimeCompare([]byte(accessKey), []byte(a.creds.AccessKey))
	sk := subtle.ConstantTimeCompare([]byte(secretKey), []byte(a.creds.SecretKey))
	return ak&sk == 1
}

func (a *authenticator) sign(exp int64) string {
	m := hmac.New(sha256.New, a.key)
	m.Write([]byte(strconv.FormatInt(exp, 10)))
	return hex.EncodeToString(m.Sum(nil))
}

func (a *authenticator) newSession() (string, time.Time) {
	exp := a.now().Add(sessionTTL)
	return strconv.FormatInt(exp.Unix(), 10) + "." + a.sign(exp.Unix()), exp
}

func (a *authenticator) validSession(v string) bool {
	expStr, sig, ok := strings.Cut(v, ".")
	if !ok {
		return false
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || a.now().Unix() >= exp {
		return false
	}
	return hmac.Equal([]byte(sig), []byte(a.sign(exp)))
}

func (a *authenticator) authorized(r *http.Request) bool {
	if ak, sk, ok := r.BasicAuth(); ok {
		return a.match(ak, sk)
	}
	c, err := r.Cookie(sessionCookie)
	return err == nil && a.validSession(c.Value)
}

func (a *authenticator) fail(w http.ResponseWriter, message string) {
	time.Sleep(a.delay)
	writeError(w, http.StatusUnauthorized, "Unauthorized", message)
}

// middleware guards the admin API; the static UI shell and health stay public
// so the login screen and the container healthcheck work.
func (a *authenticator) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		public := !strings.HasPrefix(p, "/_s3lab/") || p == "/_s3lab/health" || p == "/_s3lab/login" || p == "/_s3lab/logout"
		if public || a.authorized(r) {
			next.ServeHTTP(w, r)
			return
		}
		a.fail(w, "login required: use the console login or HTTP Basic auth with the S3 access key and secret key")
	})
}

func (a *authenticator) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AccessKey string `json:"accessKey"`
		SecretKey string `json:"secretKey"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "BadRequest", "invalid JSON body")
		return
	}
	if !a.match(body.AccessKey, body.SecretKey) {
		a.fail(w, "invalid access key or secret key")
		return
	}
	v, exp := a.newSession()
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: v, Path: "/", Expires: exp,
		HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

func logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode,
	})
	w.WriteHeader(http.StatusNoContent)
}
