package auth

import (
	"cmp"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	algorithm   = "AWS4-HMAC-SHA256"
	isoFormat   = "20060102T150405Z"
	shortFormat = "20060102"
	terminator  = "aws4_request"
	maxExpires  = 604800

	unsignedPayload       = "UNSIGNED-PAYLOAD"
	streamUnsignedTrailer = "STREAMING-UNSIGNED-PAYLOAD-TRAILER"
	streamSigned          = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD"
	streamSignedTrailer   = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER"

	emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

type credScope struct {
	accessKey string
	date      string
	region    string
	service   string
}

func (c credScope) scope() string {
	return c.date + "/" + c.region + "/" + c.service + "/" + terminator
}

type sigRequest struct {
	cred          credScope
	signedHeaders []string
	signature     string
	presigned     bool
	expires       time.Duration
	malformed     *Error
}

func parseCredential(s string) (credScope, bool) {
	p := strings.Split(s, "/")
	if len(p) != 5 || p[4] != terminator || p[0] == "" || len(p[1]) != 8 || p[2] == "" || p[3] != "s3" {
		return credScope{}, false
	}
	return credScope{accessKey: p[0], date: p[1], region: p[2], service: p[3]}, true
}

func parseSignedHeaders(s string) ([]string, bool) {
	if s == "" {
		return nil, false
	}
	names := strings.Split(strings.ToLower(s), ";")
	if !slices.Contains(names, "host") || slices.Contains(names, "") {
		return nil, false
	}
	return names, true
}

func parseAuthorization(authz string) (*sigRequest, error) {
	if strings.HasPrefix(authz, "AWS ") {
		return nil, ErrUnsupportedSignature
	}
	rest, ok := strings.CutPrefix(authz, algorithm)
	if !ok {
		return nil, ErrUnsupportedSignature
	}
	if rest == "" || (rest[0] != ' ' && rest[0] != '\t') {
		return nil, ErrHeaderMalformed
	}
	fields := map[string]string{}
	for part := range strings.SplitSeq(rest, ",") {
		k, v, found := strings.Cut(strings.TrimSpace(part), "=")
		if !found {
			return nil, ErrHeaderMalformed
		}
		fields[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	cred, ok := parseCredential(fields["Credential"])
	if !ok {
		return nil, ErrHeaderMalformed
	}
	signed, ok := parseSignedHeaders(fields["SignedHeaders"])
	sig := strings.ToLower(fields["Signature"])
	if !ok || sig == "" {
		return nil, ErrHeaderMalformed
	}
	return &sigRequest{cred: cred, signedHeaders: signed, signature: sig, malformed: ErrHeaderMalformed}, nil
}

func parsePresigned(q url.Values) (*sigRequest, error) {
	if q.Get("X-Amz-Algorithm") != algorithm {
		return nil, ErrQueryMalformed
	}
	cred, ok := parseCredential(q.Get("X-Amz-Credential"))
	if !ok {
		return nil, ErrQueryMalformed
	}
	signed, ok := parseSignedHeaders(q.Get("X-Amz-SignedHeaders"))
	sig := strings.ToLower(q.Get("X-Amz-Signature"))
	if !ok || sig == "" || q.Get("X-Amz-Date") == "" {
		return nil, ErrQueryMalformed
	}
	secs, err := strconv.ParseInt(q.Get("X-Amz-Expires"), 10, 64)
	if err != nil || secs < 1 || secs > maxExpires {
		return nil, ErrQueryMalformed
	}
	return &sigRequest{
		cred: cred, signedHeaders: signed, signature: sig, presigned: true,
		expires: time.Duration(secs) * time.Second, malformed: ErrQueryMalformed,
	}, nil
}

// requestTime returns the signing time and enforces scope date, skew and expiry.
func (v *Verifier) requestTime(r *http.Request, sr *sigRequest) (time.Time, error) {
	var t time.Time
	var err error
	switch {
	case sr.presigned:
		t, err = time.Parse(isoFormat, r.URL.Query().Get("X-Amz-Date"))
	case r.Header.Get("X-Amz-Date") != "":
		t, err = time.Parse(isoFormat, r.Header.Get("X-Amz-Date"))
	case r.Header.Get("Date") != "":
		t, err = http.ParseTime(r.Header.Get("Date"))
	default:
		return time.Time{}, ErrMissingDate
	}
	if err != nil {
		return time.Time{}, sr.malformed
	}
	t = t.UTC()
	if t.Format(shortFormat) != sr.cred.date {
		return time.Time{}, sr.malformed
	}
	now := v.now()
	if sr.presigned {
		if now.After(t.Add(sr.expires)) {
			return time.Time{}, ErrExpiredPresign
		}
		if t.After(now.Add(v.maxSkew)) {
			return time.Time{}, ErrRequestTimeTooSkewed
		}
		return t, nil
	}
	if d := now.Sub(t); d > v.maxSkew || -d > v.maxSkew {
		return time.Time{}, ErrRequestTimeTooSkewed
	}
	return t, nil
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

func signingKey(secret string, c credScope) []byte {
	k := hmacSHA256([]byte("AWS4"+secret), c.date)
	k = hmacSHA256(k, c.region)
	k = hmacSHA256(k, c.service)
	return hmacSHA256(k, terminator)
}

func canonicalRequest(r *http.Request, sr *sigRequest, payloadHash string) string {
	var b strings.Builder
	b.WriteString(r.Method)
	b.WriteByte('\n')
	b.WriteString(canonicalURI(r.URL))
	b.WriteByte('\n')
	b.WriteString(canonicalQuery(r.URL.RawQuery, sr.presigned))
	b.WriteByte('\n')
	for _, name := range sr.signedHeaders {
		b.WriteString(name)
		b.WriteByte(':')
		b.WriteString(headerValue(r, name))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	b.WriteString(strings.Join(sr.signedHeaders, ";"))
	b.WriteByte('\n')
	b.WriteString(payloadHash)
	return b.String()
}

func headerValue(r *http.Request, name string) string {
	switch name {
	case "host":
		return strings.Join(strings.Fields(r.Host), " ")
	case "content-length":
		// Go keeps the header only when it was on the wire.
		if r.Header.Get("Content-Length") == "" && r.ContentLength > 0 {
			return strconv.FormatInt(r.ContentLength, 10)
		}
	}
	vals := slices.Clone(r.Header.Values(name))
	for i, v := range vals {
		vals[i] = strings.Join(strings.Fields(v), " ")
	}
	return strings.Join(vals, ",")
}

func canonicalURI(u *url.URL) string {
	p := u.EscapedPath()
	if p == "" {
		return "/"
	}
	segs := strings.Split(p, "/")
	for i, seg := range segs {
		dec, err := url.PathUnescape(seg)
		if err != nil {
			dec = seg
		}
		segs[i] = awsEncode(dec, false)
	}
	return strings.Join(segs, "/")
}

func canonicalQuery(raw string, skipSignature bool) string {
	type pair struct{ k, v string }
	var pairs []pair
	for part := range strings.SplitSeq(raw, "&") {
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		k, v = unescapeQuery(k), unescapeQuery(v)
		if skipSignature && k == "X-Amz-Signature" {
			continue
		}
		pairs = append(pairs, pair{awsEncode(k, false), awsEncode(v, false)})
	}
	slices.SortFunc(pairs, func(a, b pair) int {
		return cmp.Or(cmp.Compare(a.k, b.k), cmp.Compare(a.v, b.v))
	})
	out := make([]string, len(pairs))
	for i, p := range pairs {
		out[i] = p.k + "=" + p.v
	}
	return strings.Join(out, "&")
}

func unescapeQuery(s string) string {
	if u, err := url.QueryUnescape(s); err == nil {
		return u
	}
	return s
}

// awsEncode applies the SigV4 URI encoding: unreserved characters stay, all
// other bytes become uppercase %XX.
func awsEncode(s string, keepSlash bool) string {
	const upper = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s))
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~', c == '/' && keepSlash:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(upper[c>>4])
			b.WriteByte(upper[c&15])
		}
	}
	return b.String()
}

func hexSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
