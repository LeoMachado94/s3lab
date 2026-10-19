// Package faults injects errors and latency into S3 requests for testing
// client retry/timeout behaviour. Owner: task T1.D.
package faults

import (
	"errors"
	"math/rand/v2"
	"net/http"

	"github.com/LeoMachado94/s3lab/internal/storage"
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

var ErrInvalidRule = errors.New("invalid fault rule")

type Engine struct{}

type Option func(*Engine)

// WithRand injects the random source used for Probability (tests).
func WithRand(r *rand.Rand) Option { return func(e *Engine) {} }

func NewEngine(opts ...Option) *Engine { return &Engine{} }

// Add validates r (wrapping ErrInvalidRule), fills defaults (Target http,
// Code from status, generated ID if empty) and stores it.
func (e *Engine) Add(r Rule) (Rule, error) { return r, errors.New("faults: not implemented") }

func (e *Engine) List() []Rule          { return nil }
func (e *Engine) Remove(id string) bool { return false }
func (e *Engine) Clear()                {}

// Match returns the first rule (insertion order) of the given target that
// matches and passes its probability, incrementing Hits and honouring Count.
func (e *Engine) Match(target Target, op, bucket, key string) (Rule, bool) { return Rule{}, false }

// LoadJSON parses a JSON array of rules (S3LAB_FAULTS) and adds them.
func (e *Engine) LoadJSON(data []byte) error { return errors.New("faults: not implemented") }

// Middleware applies http rules: waits DelayMs (honouring request context),
// sets s3op.Info.FaultID, then writes the error via writeErr or continues.
func Middleware(e *Engine, writeErr func(http.ResponseWriter, *http.Request, error)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler { return next }
}

// WrapBackend decorates b so storage rules can fail or delay backend calls.
func WrapBackend(b storage.Backend, e *Engine) storage.Backend { return b }
