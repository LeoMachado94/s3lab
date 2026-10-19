// Package reqlog records S3 requests in a bounded ring buffer and fans them
// out to live subscribers. Owner: task T1.D.
package reqlog

import (
	"net/http"
	"time"
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

// Store is a thread-safe ring buffer with pub/sub.
type Store struct{}

// NewStore keeps at most capacity entries (oldest evicted first).
func NewStore(capacity int) *Store { return &Store{} }

// Add assigns a monotonically increasing ID, stores and publishes e.
func (s *Store) Add(e Entry) Entry { return e }

// List returns matching entries ordered oldest to newest.
func (s *Store) List(f Filter) []Entry { return nil }

// Clear drops all entries; IDs keep increasing.
func (s *Store) Clear() {}

// Subscribe returns a channel receiving new entries. Slow subscribers drop
// entries instead of blocking Add. cancel closes the channel.
func (s *Store) Subscribe(buffer int) (ch <-chan Entry, cancel func()) {
	c := make(chan Entry)
	close(c)
	return c, func() {}
}

// Middleware records every request, reading op/bucket/key/error/fault from
// s3op.Info set by upstream and downstream layers.
func Middleware(s *Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler { return next }
}
