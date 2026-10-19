package reqlog

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/LeoMachado94/s3lab/internal/s3op"
)

func ids(es []Entry) []uint64 {
	out := make([]uint64, len(es))
	for i, e := range es {
		out[i] = e.ID
	}
	return out
}

func equalIDs(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestStoreRingAndIDs(t *testing.T) {
	s := NewStore(3)
	for i := 0; i < 5; i++ {
		e := s.Add(Entry{})
		if e.ID != uint64(i+1) {
			t.Fatalf("id = %d, want %d", e.ID, i+1)
		}
	}
	if got := ids(s.List(Filter{})); !equalIDs(got, []uint64{3, 4, 5}) {
		t.Fatalf("got %v", got)
	}
	s.Clear()
	if got := s.List(Filter{}); len(got) != 0 {
		t.Fatalf("after clear: %v", got)
	}
	if e := s.Add(Entry{}); e.ID != 6 {
		t.Fatalf("id after clear = %d", e.ID)
	}
	if got := ids(s.List(Filter{})); !equalIDs(got, []uint64{6}) {
		t.Fatalf("got %v", got)
	}
}

func TestNewStoreDefaultCapacity(t *testing.T) {
	for _, c := range []int{0, -5} {
		s := NewStore(c)
		for i := 0; i < 1005; i++ {
			s.Add(Entry{})
		}
		if n := len(s.List(Filter{})); n != 1000 {
			t.Fatalf("cap %d: len = %d", c, n)
		}
	}
}

func TestListFilters(t *testing.T) {
	s := NewStore(10)
	s.Add(Entry{Bucket: "a", Op: "PutObject", Key: "x/1", Status: 200})
	s.Add(Entry{Bucket: "a", Op: "GetObject", Key: "x/2", Status: 404})
	s.Add(Entry{Bucket: "b", Op: "PutObject", Key: "y/1", Status: 503})
	s.Add(Entry{Bucket: "b", Op: "GetObject", Key: "x/3", Status: 200})

	tests := []struct {
		name string
		f    Filter
		want []uint64
	}{
		{"all", Filter{}, []uint64{1, 2, 3, 4}},
		{"bucket", Filter{Bucket: "b"}, []uint64{3, 4}},
		{"op", Filter{Op: "PutObject"}, []uint64{1, 3}},
		{"prefix", Filter{KeyPrefix: "x/"}, []uint64{1, 2, 4}},
		{"status", Filter{Status: 404}, []uint64{2}},
		{"errors", Filter{ErrorsOnly: true}, []uint64{2, 3}},
		{"since", Filter{SinceID: 2}, []uint64{3, 4}},
		{"limit", Filter{Limit: 2}, []uint64{3, 4}},
		{"limit larger", Filter{Limit: 10}, []uint64{1, 2, 3, 4}},
		{"combined", Filter{Bucket: "a", ErrorsOnly: true, Limit: 1}, []uint64{2}},
		{"none", Filter{Bucket: "zzz"}, []uint64{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ids(s.List(tt.f)); !equalIDs(got, tt.want) {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
	if !(Filter{Bucket: "a"}).Match(Entry{ID: 1, Bucket: "a"}) {
		t.Fatal("Match should accept")
	}
}

func TestSubscribe(t *testing.T) {
	s := NewStore(10)
	ch, cancel := s.Subscribe(2)
	for i := 0; i < 4; i++ {
		s.Add(Entry{})
	}
	if len(ch) != 2 {
		t.Fatalf("buffered = %d", len(ch))
	}
	if s.Dropped() != 2 {
		t.Fatalf("dropped = %d", s.Dropped())
	}
	if e := <-ch; e.ID != 1 {
		t.Fatalf("first = %d", e.ID)
	}
	cancel()
	cancel()
	for range ch {
	}
	s.Add(Entry{})

	ch2, cancel2 := s.Subscribe(-1)
	defer cancel2()
	s.Add(Entry{})
	select {
	case <-ch2:
		t.Fatal("unbuffered subscriber should drop")
	default:
	}
}

func TestSubscribeConcurrent(t *testing.T) {
	s := NewStore(50)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					s.Add(Entry{})
				}
			}
		}()
	}
	var subs sync.WaitGroup
	for i := 0; i < 20; i++ {
		subs.Add(1)
		go func() {
			defer subs.Done()
			for j := 0; j < 50; j++ {
				ch, cancel := s.Subscribe(1)
				<-ch
				go cancel()
				cancel()
			}
		}()
	}
	subs.Wait()
	close(stop)
	wg.Wait()
}

func serve(t *testing.T, s *Store, h http.HandlerFunc, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	Middleware(s)(h).ServeHTTP(rr, req)
	return rr
}

func TestMiddleware(t *testing.T) {
	s := NewStore(10)
	info := &s3op.Info{RequestID: "RID", Op: s3op.PutObject, Bucket: "b", Key: "k"}
	req := httptest.NewRequest(http.MethodPut, "/b/k?x=1", strings.NewReader("hello"))
	req.Header.Set("User-Agent", "ua")
	req = req.WithContext(s3op.WithInfo(req.Context(), info))
	serve(t, s, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		info.AccessKey, info.Auth, info.ErrorCode, info.FaultID = "ak", s3op.AuthHeader, "Boom", "f1"
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("abc"))
		w.(http.Flusher).Flush()
	}, req)
	got := s.List(Filter{})
	if len(got) != 1 {
		t.Fatalf("entries = %d", len(got))
	}
	e := got[0]
	if e.Status != 418 || e.BytesIn != 5 || e.BytesOut != 3 || e.RequestID != "RID" ||
		e.Op != "PutObject" || e.Bucket != "b" || e.Key != "k" || e.AccessKey != "ak" ||
		e.Auth != "header" || e.ErrorCode != "Boom" || e.FaultID != "f1" || e.Method != "PUT" ||
		e.Path != "/b/k" || e.Query != "x=1" || e.UserAgent != "ua" || e.RemoteAddr == "" || e.Time.IsZero() {
		t.Fatalf("bad entry: %+v", e)
	}
}

func TestMiddlewareDefaultsAndNoInfo(t *testing.T) {
	s := NewStore(10)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Body = nil
	serve(t, s, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("x"))
		w.WriteHeader(http.StatusInternalServerError)
	}, req)
	e := s.List(Filter{})[0]
	if e.Status != 200 || e.Op != "" || e.BytesOut != 1 || e.BytesIn != 0 {
		t.Fatalf("bad entry: %+v", e)
	}
}

func TestMiddlewareInformationalStatus(t *testing.T) {
	s := NewStore(10)
	serve(t, s, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusContinue)
		w.WriteHeader(http.StatusNoContent)
	}, httptest.NewRequest(http.MethodGet, "/", nil))
	if st := s.List(Filter{})[0].Status; st != 204 {
		t.Fatalf("status = %d", st)
	}
}

func TestMiddlewareUnwrapAndNonFlusher(t *testing.T) {
	rec := &recorder{ResponseWriter: struct{ http.ResponseWriter }{httptest.NewRecorder()}}
	rec.Flush()
	if rec.Unwrap() == nil {
		t.Fatal("unwrap nil")
	}
}

func TestMiddlewarePanicStillRecorded(t *testing.T) {
	s := NewStore(10)
	defer func() {
		if recover() == nil {
			t.Fatal("expected re-panic")
		}
		if n := len(s.List(Filter{})); n != 1 {
			t.Fatalf("entries = %d", n)
		}
	}()
	serve(t, s, func(w http.ResponseWriter, r *http.Request) { panic("boom") },
		httptest.NewRequest(http.MethodGet, "/", nil))
}

func TestRedactQuery(t *testing.T) {
	cases := map[string]string{
		"":    "",
		"x=1": "x=1",
		"a=1&X-Amz-Signature=abc%2B&X-Amz-Credential=ak%2F2024&b": "a=1&X-Amz-Signature=REDACTED&X-Amz-Credential=ak%2F2024&b",
		"x-amz-security-token=tok&x-amz-signature=s":              "x-amz-security-token=REDACTED&x-amz-signature=REDACTED",
	}
	for in, want := range cases {
		if got := redactQuery(in); got != want {
			t.Errorf("redactQuery(%q) = %q, want %q", in, got, want)
		}
	}
}
