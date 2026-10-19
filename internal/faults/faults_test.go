package faults

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LeoMachado94/s3lab/internal/s3op"
	"github.com/LeoMachado94/s3lab/internal/storage"
)

func fp(f float64) *float64 { return &f }

func TestAddValidation(t *testing.T) {
	tests := []struct {
		name string
		rule Rule
		ok   bool
	}{
		{"minimal status", Rule{Status: 500}, true},
		{"delay only", Rule{DelayMs: 10}, true},
		{"storage target", Rule{Target: TargetStorage, Status: 500}, true},
		{"bad target", Rule{Target: "x", Status: 500}, false},
		{"status too low", Rule{Status: 200}, false},
		{"status too high", Rule{Status: 600}, false},
		{"delay negative", Rule{Status: 500, DelayMs: -1}, false},
		{"delay too big", Rule{DelayMs: 300001}, false},
		{"delay max", Rule{DelayMs: 300000}, true},
		{"prob zero", Rule{Status: 500, Probability: fp(0)}, false},
		{"prob over", Rule{Status: 500, Probability: fp(1.1)}, false},
		{"prob one", Rule{Status: 500, Probability: fp(1)}, true},
		{"count negative", Rule{Status: 500, Count: -1}, false},
		{"bad glob", Rule{Status: 500, Key: "[a"}, false},
		{"good glob", Rule{Status: 500, Key: "a/*.jpg"}, true},
		{"no effect", Rule{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewEngine().Add(tt.rule)
			if tt.ok != (err == nil) {
				t.Fatalf("err = %v", err)
			}
			if err != nil && !errors.Is(err, ErrInvalidRule) {
				t.Fatalf("not ErrInvalidRule: %v", err)
			}
		})
	}
}

func TestAddDefaults(t *testing.T) {
	tests := map[int]string{
		500: "InternalError", 503: "SlowDown", 403: "AccessDenied",
		404: "NoSuchKey", 400: "InvalidRequest", 502: "InternalError",
	}
	for status, code := range tests {
		r, err := NewEngine().Add(Rule{Status: status, Hits: 7})
		if err != nil {
			t.Fatal(err)
		}
		if r.Code != code || r.Target != TargetHTTP || r.Message == "" || r.Hits != 0 {
			t.Fatalf("status %d: %+v", status, r)
		}
		if !strings.HasPrefix(r.ID, "f-") || len(r.ID) != 10 {
			t.Fatalf("id = %q", r.ID)
		}
	}
	r, _ := NewEngine().Add(Rule{Status: 500, Code: "Custom", Message: "m"})
	if r.Code != "Custom" || r.Message != "m" {
		t.Fatalf("%+v", r)
	}
}

func TestDuplicateListRemoveClear(t *testing.T) {
	e := NewEngine()
	if _, err := e.Add(Rule{ID: "a", Status: 500, Probability: fp(0.5)}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Add(Rule{ID: "a", Status: 500}); !errors.Is(err, ErrDuplicateRule) {
		t.Fatalf("err = %v", err)
	}
	e.Add(Rule{ID: "b", Status: 500})
	l := e.List()
	if len(l) != 2 || l[0].ID != "a" || l[1].ID != "b" {
		t.Fatalf("%+v", l)
	}
	*l[0].Probability = 0.9
	if *e.List()[0].Probability != 0.5 {
		t.Fatal("List must return copies")
	}
	if !e.Remove("a") || e.Remove("a") {
		t.Fatal("remove semantics")
	}
	e.Clear()
	if len(e.List()) != 0 {
		t.Fatal("clear")
	}
}

func TestMatch(t *testing.T) {
	e := NewEngine()
	e.Add(Rule{ID: "store", Target: TargetStorage, Status: 500})
	e.Add(Rule{ID: "op", Op: "PutObject", Status: 500})
	e.Add(Rule{ID: "bucket", Bucket: "b", Status: 501})
	e.Add(Rule{ID: "glob", Key: "up/*.jpg", Status: 502})
	e.rules = append(e.rules, Rule{ID: "badglob", Target: TargetHTTP, Key: "[", Status: 500})
	e.Add(Rule{ID: "all", Status: 503})

	tests := []struct {
		name                string
		target              Target
		op, bucket, key, id string
	}{
		{"op first", TargetHTTP, "PutObject", "x", "k", "op"},
		{"bucket", TargetHTTP, "GetObject", "b", "k", "bucket"},
		{"glob", TargetHTTP, "GetObject", "x", "up/a.jpg", "glob"},
		{"glob nested no match", TargetHTTP, "GetObject", "x", "up/a/b.jpg", "all"},
		{"fallthrough", TargetHTTP, "GetObject", "x", "k", "all"},
		{"storage", TargetStorage, "Anything", "x", "k", "store"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, ok := e.Match(tt.target, tt.op, tt.bucket, tt.key)
			if !ok || r.ID != tt.id {
				t.Fatalf("got %q ok=%v", r.ID, ok)
			}
		})
	}
	if _, ok := NewEngine().Match(TargetHTTP, "", "", ""); ok {
		t.Fatal("empty engine matched")
	}
}

func TestMatchCount(t *testing.T) {
	e := NewEngine()
	e.Add(Rule{ID: "once", Status: 500, Count: 2})
	e.Add(Rule{ID: "after", Status: 503})
	var got []string
	for i := 0; i < 4; i++ {
		r, _ := e.Match(TargetHTTP, "", "", "")
		got = append(got, r.ID)
	}
	if strings.Join(got, ",") != "once,once,after,after" {
		t.Fatalf("got %v", got)
	}
	if h := e.List()[0].Hits; h != 2 {
		t.Fatalf("hits = %d", h)
	}
}

func TestMatchProbability(t *testing.T) {
	e := NewEngine(WithRand(rand.New(rand.NewPCG(1, 2))))
	e.Add(Rule{ID: "p", Status: 500, Probability: fp(0.5)})
	e.Add(Rule{ID: "fallback", Status: 503})
	ref := rand.New(rand.NewPCG(1, 2))
	for i := 0; i < 50; i++ {
		want := "fallback"
		if ref.Float64() < 0.5 {
			want = "p"
		}
		r, ok := e.Match(TargetHTTP, "", "", "")
		if !ok || r.ID != want {
			t.Fatalf("iter %d: got %q want %q", i, r.ID, want)
		}
	}
}

func TestLoadJSON(t *testing.T) {
	e := NewEngine()
	err := e.LoadJSON([]byte(`[{"id":"a","status":503,"op":"PutObject"},{"delayMs":5}]`))
	if err != nil || len(e.List()) != 2 {
		t.Fatalf("err=%v rules=%v", err, e.List())
	}
	err = NewEngine().LoadJSON([]byte(`[{"status":500},{"status":200}]`))
	if !errors.Is(err, ErrInvalidRule) || !strings.Contains(err.Error(), "rule 1") {
		t.Fatalf("err = %v", err)
	}
	if err := NewEngine().LoadJSON([]byte(`nope`)); !errors.Is(err, ErrInvalidRule) {
		t.Fatalf("err = %v", err)
	}
}

func TestInjectedError(t *testing.T) {
	e := &InjectedError{Code: "SlowDown", Message: "m", Status: 503}
	if e.Error() != "SlowDown: m" || e.S3Code() != "SlowDown" || e.HTTPStatus() != 503 {
		t.Fatalf("%+v", e)
	}
}

func run(e *Engine, info *s3op.Info, req *http.Request) (*httptest.ResponseRecorder, bool, error) {
	var got error
	called := false
	h := Middleware(e, func(w http.ResponseWriter, r *http.Request, err error) {
		got = err
		var ie *InjectedError
		_ = errors.As(err, &ie)
		w.WriteHeader(ie.Status)
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	if info != nil {
		req = req.WithContext(s3op.WithInfo(req.Context(), info))
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr, called, got
}

func TestMiddleware(t *testing.T) {
	e := NewEngine()
	e.Add(Rule{ID: "err", Op: "PutObject", Status: 503})
	e.Add(Rule{ID: "slow", Op: "GetObject", DelayMs: 20})

	t.Run("error", func(t *testing.T) {
		info := &s3op.Info{Op: s3op.PutObject}
		rr, called, err := run(e, info, httptest.NewRequest("PUT", "/b/k", nil))
		var ie *InjectedError
		if called || rr.Code != 503 || !errors.As(err, &ie) || ie.Code != "SlowDown" || info.FaultID != "err" {
			t.Fatalf("code=%d called=%v err=%v fault=%q", rr.Code, called, err, info.FaultID)
		}
	})
	t.Run("delay only continues", func(t *testing.T) {
		info := &s3op.Info{Op: s3op.GetObject}
		start := time.Now()
		rr, called, _ := run(e, info, httptest.NewRequest("GET", "/b/k", nil))
		if !called || rr.Code != 200 || info.FaultID != "slow" || time.Since(start) < 20*time.Millisecond {
			t.Fatalf("called=%v code=%d fault=%q", called, rr.Code, info.FaultID)
		}
	})
	t.Run("no match", func(t *testing.T) {
		_, called, _ := run(e, &s3op.Info{Op: s3op.HeadObject}, httptest.NewRequest("HEAD", "/b/k", nil))
		if !called {
			t.Fatal("should pass through")
		}
	})
	t.Run("no info", func(t *testing.T) {
		_, called, _ := run(e, nil, httptest.NewRequest("PUT", "/b/k", nil))
		if !called {
			t.Fatal("should pass through")
		}
	})
	t.Run("cancel during delay", func(t *testing.T) {
		e2 := NewEngine()
		e2.Add(Rule{ID: "long", DelayMs: 60000})
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		info := &s3op.Info{Op: s3op.GetObject}
		start := time.Now()
		_, called, err := run(e2, info, httptest.NewRequest("GET", "/b/k", nil).WithContext(ctx))
		if called || err != nil || info.FaultID != "long" || info.ErrorCode != "ClientCanceled" || time.Since(start) > 5*time.Second {
			t.Fatalf("called=%v err=%v fault=%q code=%q", called, err, info.FaultID, info.ErrorCode)
		}
	})
}

type fake struct{ storage.Backend }

func (fake) ListBuckets(context.Context) ([]storage.Bucket, error) {
	return []storage.Bucket{{Name: "ok"}}, nil
}
func (fake) CreateBucket(context.Context, string) error { return nil }
func (fake) DeleteBucket(context.Context, string) error { return nil }
func (fake) HeadBucket(context.Context, string) (storage.Bucket, error) {
	return storage.Bucket{Name: "ok"}, nil
}

func (fake) PutObject(context.Context, string, string, io.Reader, storage.PutOptions) (storage.ObjectInfo, error) {
	return storage.ObjectInfo{ETag: "ok"}, nil
}

func (fake) GetObject(context.Context, string, string, *storage.ByteRange) (io.ReadCloser, storage.ObjectInfo, error) {
	return io.NopCloser(strings.NewReader("")), storage.ObjectInfo{ETag: "ok"}, nil
}

func (fake) HeadObject(context.Context, string, string) (storage.ObjectInfo, error) {
	return storage.ObjectInfo{ETag: "ok"}, nil
}
func (fake) DeleteObject(context.Context, string, string) error { return nil }
func (fake) CopyObject(context.Context, storage.ObjectRef, storage.ObjectRef, storage.CopyOptions) (storage.ObjectInfo, error) {
	return storage.ObjectInfo{ETag: "ok"}, nil
}

func (fake) ListObjects(context.Context, string, storage.ListOptions) (storage.ListResult, error) {
	return storage.ListResult{NextStartAfter: "ok"}, nil
}

func (fake) CreateMultipartUpload(context.Context, string, string, storage.ObjectMeta) (string, error) {
	return "ok", nil
}

func (fake) UploadPart(context.Context, string, string, string, int, io.Reader, int64) (storage.PartInfo, error) {
	return storage.PartInfo{ETag: "ok"}, nil
}

func (fake) CompleteMultipartUpload(context.Context, string, string, string, []storage.CompletePart) (storage.ObjectInfo, error) {
	return storage.ObjectInfo{ETag: "ok"}, nil
}
func (fake) AbortMultipartUpload(context.Context, string, string, string) error { return nil }
func (fake) ListParts(context.Context, string, string, string, storage.ListPartsOptions) (storage.ListPartsResult, error) {
	return storage.ListPartsResult{NextPartNumberMarker: 1}, nil
}

func (fake) ListMultipartUploads(context.Context, string, storage.ListUploadsOptions) (storage.ListUploadsResult, error) {
	return storage.ListUploadsResult{IsTruncated: true}, nil
}
func (fake) Reset(context.Context, string) error { return nil }

func TestWrapBackend(t *testing.T) {
	ctx := context.Background()
	calls := map[string]func(b storage.Backend) error{
		"ListBuckets":  func(b storage.Backend) error { _, err := b.ListBuckets(ctx); return err },
		"CreateBucket": func(b storage.Backend) error { return b.CreateBucket(ctx, "b") },
		"DeleteBucket": func(b storage.Backend) error { return b.DeleteBucket(ctx, "b") },
		"HeadBucket":   func(b storage.Backend) error { _, err := b.HeadBucket(ctx, "b"); return err },
		"PutObject": func(b storage.Backend) error {
			_, err := b.PutObject(ctx, "b", "k", nil, storage.PutOptions{})
			return err
		},
		"GetObject":    func(b storage.Backend) error { _, _, err := b.GetObject(ctx, "b", "k", nil); return err },
		"HeadObject":   func(b storage.Backend) error { _, err := b.HeadObject(ctx, "b", "k"); return err },
		"DeleteObject": func(b storage.Backend) error { return b.DeleteObject(ctx, "b", "k") },
		"CopyObject": func(b storage.Backend) error {
			_, err := b.CopyObject(ctx, storage.ObjectRef{Bucket: "s", Key: "s"}, storage.ObjectRef{Bucket: "b", Key: "k"}, storage.CopyOptions{})
			return err
		},
		"ListObjects": func(b storage.Backend) error { _, err := b.ListObjects(ctx, "b", storage.ListOptions{}); return err },
		"CreateMultipartUpload": func(b storage.Backend) error {
			_, err := b.CreateMultipartUpload(ctx, "b", "k", storage.ObjectMeta{})
			return err
		},
		"UploadPart": func(b storage.Backend) error {
			_, err := b.UploadPart(ctx, "b", "k", "u", 1, nil, 0)
			return err
		},
		"CompleteMultipartUpload": func(b storage.Backend) error {
			_, err := b.CompleteMultipartUpload(ctx, "b", "k", "u", nil)
			return err
		},
		"AbortMultipartUpload": func(b storage.Backend) error { return b.AbortMultipartUpload(ctx, "b", "k", "u") },
		"ListParts": func(b storage.Backend) error {
			_, err := b.ListParts(ctx, "b", "k", "u", storage.ListPartsOptions{})
			return err
		},
		"ListMultipartUploads": func(b storage.Backend) error {
			_, err := b.ListMultipartUploads(ctx, "b", storage.ListUploadsOptions{})
			return err
		},
		"Reset": func(b storage.Backend) error { return b.Reset(ctx, "") },
	}
	if len(calls) != 17 {
		t.Fatalf("methods = %d", len(calls))
	}
	for method, call := range calls {
		t.Run(method, func(t *testing.T) {
			e := NewEngine()
			b := WrapBackend(fake{}, e)
			if err := call(b); err != nil {
				t.Fatalf("pass-through: %v", err)
			}
			e.Add(Rule{ID: "r", Target: TargetStorage, Op: method, Status: 503, Bucket: "b", Key: ""})
			if method == "ListBuckets" || method == "Reset" {
				e.Clear()
				e.Add(Rule{ID: "r", Target: TargetStorage, Op: method, Status: 503})
			}
			err := call(b)
			var ie *InjectedError
			if !errors.As(err, &ie) || ie.RuleID != "r" || ie.Status != 503 {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestWrapBackendDelayAndCancel(t *testing.T) {
	e := NewEngine()
	e.Add(Rule{Target: TargetStorage, Op: "ListBuckets", DelayMs: 10})
	b := WrapBackend(fake{}, e)
	if _, err := b.ListBuckets(context.Background()); err != nil {
		t.Fatalf("delay-only: %v", err)
	}
	e.Clear()
	e.Add(Rule{Target: TargetStorage, DelayMs: 60000})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := b.ListBuckets(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

func TestWrapBackendCopyUsesDestination(t *testing.T) {
	e := NewEngine()
	e.Add(Rule{Target: TargetStorage, Op: "CopyObject", Bucket: "dst", Key: "d", Status: 500})
	b := WrapBackend(fake{}, e)
	_, err := b.CopyObject(context.Background(), storage.ObjectRef{Bucket: "src", Key: "s"}, storage.ObjectRef{Bucket: "dst", Key: "d"}, storage.CopyOptions{})
	if err == nil {
		t.Fatal("expected injection on destination")
	}
}
