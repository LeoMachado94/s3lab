// Package reqlog records S3 requests in a bounded ring buffer and fans them
// out to live subscribers. Owner: task T1.D.
package reqlog

import (
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/LeoMachado94/s3lab/internal/s3op"
)

// Entry is one recorded request. JSON shape is part of the admin API contract
// (docs/admin-api.md).
type Entry struct {
	ID         uint64    `json:"id"`
	RequestID  string    `json:"requestId"`
	Time       time.Time `json:"time"`
	Method     string    `json:"method"`
	Path       string    `json:"path"`
	Query      string    `json:"query,omitempty"`
	Op         string    `json:"op"`
	Bucket     string    `json:"bucket,omitempty"`
	Key        string    `json:"key,omitempty"`
	Status     int       `json:"status"`
	ErrorCode  string    `json:"errorCode,omitempty"`
	DurationMs float64   `json:"durationMs"`
	BytesIn    int64     `json:"bytesIn"`
	BytesOut   int64     `json:"bytesOut"`
	AccessKey  string    `json:"accessKey,omitempty"`
	Auth       string    `json:"auth,omitempty"`
	FaultID    string    `json:"faultId,omitempty"`
	RemoteAddr string    `json:"remoteAddr,omitempty"`
	UserAgent  string    `json:"userAgent,omitempty"`
}

// Filter selects entries; zero values match everything.
type Filter struct {
	Bucket string
	Op     string
	// KeyPrefix matches entries whose key starts with it.
	KeyPrefix string
	Status    int
	// ErrorsOnly keeps entries with Status >= 400.
	ErrorsOnly bool
	// SinceID keeps entries with ID > SinceID.
	SinceID uint64
	// Limit keeps the newest N matches (<= 0 means all).
	Limit int
}

const defaultCapacity = 1000

// Store is a thread-safe ring buffer with pub/sub.
type Store struct {
	mu      sync.Mutex
	buf     []Entry
	head    int
	size    int
	nextID  uint64
	subs    map[uint64]*subscriber
	nextSub uint64
	dropped uint64
}

type subscriber struct {
	ch     chan Entry
	closed bool
}

// NewStore keeps at most capacity entries (oldest evicted first).
func NewStore(capacity int) *Store {
	if capacity <= 0 {
		capacity = defaultCapacity
	}
	return &Store{buf: make([]Entry, capacity), subs: map[uint64]*subscriber{}}
}

// Add assigns a monotonically increasing ID, stores and publishes e.
func (s *Store) Add(e Entry) Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	e.ID = s.nextID
	idx := (s.head + s.size) % len(s.buf)
	if s.size == len(s.buf) {
		idx = s.head
		s.head = (s.head + 1) % len(s.buf)
	} else {
		s.size++
	}
	s.buf[idx] = e
	for _, sub := range s.subs {
		select {
		case sub.ch <- e:
		default:
			s.dropped++
		}
	}
	return e
}

// Dropped reports how many entries were dropped for slow subscribers.
func (s *Store) Dropped() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped
}

func (f Filter) match(e Entry) bool {
	switch {
	case f.Bucket != "" && e.Bucket != f.Bucket:
		return false
	case f.Op != "" && e.Op != f.Op:
		return false
	case f.KeyPrefix != "" && !strings.HasPrefix(e.Key, f.KeyPrefix):
		return false
	case f.Status != 0 && e.Status != f.Status:
		return false
	case f.ErrorsOnly && e.Status < 400:
		return false
	case e.ID <= f.SinceID:
		return false
	}
	return true
}

// Match reports whether e passes the filter (Limit is ignored).
func (f Filter) Match(e Entry) bool { return f.match(e) }

// List returns matching entries ordered oldest to newest.
func (s *Store) List(f Filter) []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Entry{}
	for i := 0; i < s.size; i++ {
		e := s.buf[(s.head+i)%len(s.buf)]
		if f.match(e) {
			out = append(out, e)
		}
	}
	if f.Limit > 0 && len(out) > f.Limit {
		out = append([]Entry(nil), out[len(out)-f.Limit:]...)
	}
	return out
}

// Clear drops all entries; IDs keep increasing.
func (s *Store) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.buf)
	s.head, s.size = 0, 0
}

// Subscribe returns a channel receiving new entries. Slow subscribers drop
// entries instead of blocking Add. cancel closes the channel.
func (s *Store) Subscribe(buffer int) (ch <-chan Entry, cancel func()) {
	if buffer < 0 {
		buffer = 0
	}
	sub := &subscriber{ch: make(chan Entry, buffer)}
	s.mu.Lock()
	s.nextSub++
	id := s.nextSub
	s.subs[id] = sub
	s.mu.Unlock()
	return sub.ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if sub.closed {
			return
		}
		sub.closed = true
		delete(s.subs, id)
		close(sub.ch)
	}
}

type recorder struct {
	http.ResponseWriter
	status int
	bytes  int64
	wrote  bool
}

func (r *recorder) WriteHeader(code int) {
	if !r.wrote {
		r.status = code
		if code >= 200 || code == http.StatusSwitchingProtocols {
			r.wrote = true
		}
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(p []byte) (int, error) {
	r.wrote = true
	n, err := r.ResponseWriter.Write(p)
	r.bytes += int64(n)
	return n, err
}

func (r *recorder) Flush() {
	r.wrote = true
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

type countingBody struct {
	io.ReadCloser
	n int64
}

func (c *countingBody) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	c.n += int64(n)
	return n, err
}

// Middleware records every request, reading op/bucket/key/error/fault from
// s3op.Info set by upstream and downstream layers.
func Middleware(s *Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &recorder{ResponseWriter: w, status: http.StatusOK}
			body := &countingBody{ReadCloser: http.NoBody}
			if r.Body != nil {
				body.ReadCloser = r.Body
			}
			r.Body = body
			defer func() {
				e := Entry{
					Time:       start,
					Method:     r.Method,
					Path:       r.URL.Path,
					Query:      r.URL.RawQuery,
					Status:     rec.status,
					DurationMs: math.Round(float64(time.Since(start))/float64(time.Millisecond)*100) / 100,
					BytesIn:    body.n,
					BytesOut:   rec.bytes,
					RemoteAddr: r.RemoteAddr,
					UserAgent:  r.UserAgent(),
				}
				if info := s3op.FromContext(r.Context()); info != nil {
					e.RequestID = info.RequestID
					e.Op = string(info.Op)
					e.Bucket = info.Bucket
					e.Key = info.Key
					e.AccessKey = info.AccessKey
					e.Auth = string(info.Auth)
					e.ErrorCode = info.ErrorCode
					e.FaultID = info.FaultID
				}
				s.Add(e)
			}()
			next.ServeHTTP(rec, r)
		})
	}
}
