// Package faults injects errors and latency into S3 requests for testing
// client retry/timeout behaviour. Owner: task T1.D.
package faults

import (
	"context"
	crand "crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"path"
	"sync"
	"time"

	"github.com/LeoMachado94/s3lab/internal/s3op"
)

type Target string

const (
	// TargetHTTP matches at the HTTP layer, before auth.
	TargetHTTP Target = "http"
	// TargetStorage matches inside the storage backend (WrapBackend).
	TargetStorage Target = "storage"
)

// Rule JSON shape is part of the admin API contract (docs/admin-api.md).
type Rule struct {
	ID     string `json:"id"`
	Target Target `json:"target,omitempty"`
	// Op is an s3op.Op name for http rules or a storage.Backend method name
	// for storage rules. Empty matches any.
	Op     string `json:"op,omitempty"`
	Bucket string `json:"bucket,omitempty"`
	// Key is a path.Match glob; empty matches any.
	Key string `json:"key,omitempty"`
	// Status/Code/Message define the injected S3 error. Status 0 means
	// delay-only.
	Status  int    `json:"status,omitempty"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	DelayMs int    `json:"delayMs,omitempty"`
	// Probability in (0,1]; nil means 1.
	Probability *float64 `json:"probability,omitempty"`
	// Count limits how many times the rule fires; 0 means unlimited.
	Count int `json:"count,omitempty"`
	// Hits is read-only: how many times the rule fired.
	Hits int `json:"hits"`
}

// InjectedError is returned by WrapBackend (implements s3err.Coder).
type InjectedError struct {
	RuleID  string
	Code    string
	Message string
	Status  int
}

func (e *InjectedError) Error() string   { return e.Code + ": " + e.Message }
func (e *InjectedError) S3Code() string  { return e.Code }
func (e *InjectedError) HTTPStatus() int { return e.Status }

var (
	ErrInvalidRule   = errors.New("invalid fault rule")
	ErrDuplicateRule = errors.New("duplicate fault rule id")
)

const (
	maxDelayMs     = 300000
	defaultMessage = "Injected fault."
)

type Engine struct {
	mu    sync.Mutex
	rules []Rule
	rng   *rand.Rand
}

type Option func(*Engine)

// WithRand injects the random source used for Probability (tests).
func WithRand(r *rand.Rand) Option { return func(e *Engine) { e.rng = r } }

func NewEngine(opts ...Option) *Engine {
	e := &Engine{}
	for _, o := range opts {
		o(e)
	}
	if e.rng == nil {
		var b [16]byte
		_, _ = crand.Read(b[:])
		e.rng = rand.New( //nolint:gosec // fault probability needs no CSPRNG
			rand.NewPCG(binary.LittleEndian.Uint64(b[:8]), binary.LittleEndian.Uint64(b[8:])))
	}
	return e
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidRule, fmt.Sprintf(format, args...))
}

func defaultCode(status int) string {
	switch status {
	case http.StatusServiceUnavailable:
		return "SlowDown"
	case http.StatusForbidden:
		return "AccessDenied"
	case http.StatusNotFound:
		return "NoSuchKey"
	case http.StatusBadRequest:
		return "InvalidRequest"
	default:
		return "InternalError"
	}
}

func validate(r *Rule) error {
	switch r.Target {
	case "":
		r.Target = TargetHTTP
	case TargetHTTP, TargetStorage:
	default:
		return invalid("target must be %q or %q", TargetHTTP, TargetStorage)
	}
	if r.Status != 0 && (r.Status < 400 || r.Status > 599) {
		return invalid("status must be 0 or 400-599")
	}
	if r.DelayMs < 0 || r.DelayMs > maxDelayMs {
		return invalid("delayMs must be between 0 and %d", maxDelayMs)
	}
	if p := r.Probability; p != nil && (!(*p > 0) || *p > 1) {
		return invalid("probability must be in (0,1]")
	}
	if r.Count < 0 {
		return invalid("count must be >= 0")
	}
	if _, err := path.Match(r.Key, ""); err != nil {
		return invalid("key glob: %v", err)
	}
	if r.Status == 0 && r.DelayMs == 0 {
		return invalid("status or delayMs is required")
	}
	if r.Status > 0 {
		if r.Code == "" {
			r.Code = defaultCode(r.Status)
		}
		if r.Message == "" {
			r.Message = defaultMessage
		}
	}
	return nil
}

func newID() string {
	var b [4]byte
	_, _ = crand.Read(b[:])
	return "f-" + hex.EncodeToString(b[:])
}

func cloneRule(r Rule) Rule {
	if r.Probability != nil {
		p := *r.Probability
		r.Probability = &p
	}
	return r
}

// Add validates r (wrapping ErrInvalidRule), fills defaults (Target http,
// Code from status, generated ID if empty) and stores it.
func (e *Engine) Add(r Rule) (Rule, error) {
	r = cloneRule(r)
	if err := validate(&r); err != nil {
		return Rule{}, err
	}
	r.Hits = 0
	e.mu.Lock()
	defer e.mu.Unlock()
	if r.ID == "" {
		for {
			r.ID = newID()
			if e.indexLocked(r.ID) < 0 {
				break
			}
		}
	} else if e.indexLocked(r.ID) >= 0 {
		return Rule{}, fmt.Errorf("%w: %q", ErrDuplicateRule, r.ID)
	}
	e.rules = append(e.rules, r)
	return cloneRule(r), nil
}

func (e *Engine) indexLocked(id string) int {
	for i := range e.rules {
		if e.rules[i].ID == id {
			return i
		}
	}
	return -1
}

func (e *Engine) List() []Rule {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Rule, len(e.rules))
	for i, r := range e.rules {
		out[i] = cloneRule(r)
	}
	return out
}

func (e *Engine) Remove(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	i := e.indexLocked(id)
	if i < 0 {
		return false
	}
	e.rules = append(e.rules[:i], e.rules[i+1:]...)
	return true
}

func (e *Engine) Clear() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rules = nil
}

func (r *Rule) matches(target Target, op, bucket, key string) bool {
	if r.Target != target {
		return false
	}
	if r.Op != "" && r.Op != op {
		return false
	}
	if r.Bucket != "" && r.Bucket != bucket {
		return false
	}
	if r.Key != "" {
		if ok, err := path.Match(r.Key, key); err != nil || !ok {
			return false
		}
	}
	return r.Count <= 0 || r.Hits < r.Count
}

// Match returns the first rule (insertion order) of the given target that
// matches and passes its probability, incrementing Hits and honouring Count.
func (e *Engine) Match(target Target, op, bucket, key string) (Rule, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range e.rules {
		r := &e.rules[i]
		if !r.matches(target, op, bucket, key) {
			continue
		}
		if r.Probability != nil && *r.Probability < 1 && e.rng.Float64() >= *r.Probability {
			continue
		}
		r.Hits++
		return cloneRule(*r), true
	}
	return Rule{}, false
}

// LoadJSON parses a JSON array of rules (S3LAB_FAULTS) and adds them.
func (e *Engine) LoadJSON(data []byte) error {
	var rules []Rule
	if err := json.Unmarshal(data, &rules); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidRule, err)
	}
	for i, r := range rules {
		if _, err := e.Add(r); err != nil {
			return fmt.Errorf("rule %d: %w", i, err)
		}
	}
	return nil
}

func (r Rule) injected() *InjectedError {
	return &InjectedError{RuleID: r.ID, Code: r.Code, Message: r.Message, Status: r.Status}
}

// wait sleeps for ms; false means ctx ended first.
func wait(ctx context.Context, ms int) bool {
	if ms <= 0 {
		return true
	}
	t := time.NewTimer(time.Duration(ms) * time.Millisecond)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// Middleware applies http rules: waits DelayMs (honouring request context),
// sets s3op.Info.FaultID, then writes the error via writeErr or continues.
func Middleware(e *Engine, writeErr func(http.ResponseWriter, *http.Request, error)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			info := s3op.FromContext(r.Context())
			if info == nil {
				next.ServeHTTP(w, r)
				return
			}
			rule, ok := e.Match(TargetHTTP, string(info.Op), info.Bucket, info.Key)
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			if !wait(r.Context(), rule.DelayMs) {
				return
			}
			info.FaultID = rule.ID
			if rule.Status > 0 {
				writeErr(w, r, rule.injected())
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
