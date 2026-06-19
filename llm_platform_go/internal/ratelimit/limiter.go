// Package ratelimit provides a per-task request- and token-rate limiter for the
// prediction gateway. Each task gets its own rolling window with two coarse
// gates plus a per-request input cap:
//
//   - per-request input cap: reject a single request whose estimated input
//     tokens exceed MaxInputTokens (deterministic — retrying won't help);
//   - request-rate cap: at most MaxRequests accepted per task per Window;
//   - token budget: at most MaxTokens consumed per task per Window, enforced
//     "reserve upfront" — a request reserves its estimated input tokens before
//     running and the caller later Reconciles the reservation to the tokens
//     actually consumed (input + output across every attempt, including failed
//     and fallback ones).
//
// Tasks are independent: each task's window has its own lock, so different tasks
// are never serialized against each other.
package ratelimit

import (
	"fmt"
	"sync"
	"time"
)

// Config controls the limiter. A zero MaxRequests / MaxTokens / MaxInputTokens
// disables that particular gate (treated as unlimited). Enabled=false disables
// the limiter entirely.
type Config struct {
	Enabled        bool
	Window         time.Duration // rolling window length per task
	MaxRequests    int           // accepted requests per task per window (0 = unlimited)
	MaxTokens      int           // tokens consumed per task per window (0 = unlimited)
	MaxInputTokens int           // estimated input tokens allowed for one request (0 = unlimited)
	CharsPerToken  int           // estimation divisor (default 4); ~chars per token
	TokensPerImage int           // estimation: flat token cost added per attached image
}

// Rejection codes (also surfaced to clients as error codes).
const (
	InputTooLarge = "input_too_large"
	RequestRate   = "request_rate_exceeded"
	TokenBudget   = "token_budget_exhausted"
)

// Decision is the result of Reserve. When Allowed is false, Code says why and
// RetryAfter hints how long until the window refills (zero for InputTooLarge,
// which won't pass on retry).
type Decision struct {
	Allowed    bool
	Code       string
	Message    string
	RetryAfter time.Duration
}

// Reservation links a Reserve to its Reconcile so the estimated tokens can be
// settled to the real consumption. The zero value is an inert no-op (used when
// the limiter is disabled or the request was rejected), so Reconcile is always
// safe to call.
type Reservation struct {
	taskID      string
	windowStart time.Time
	estTokens   int
	active      bool
}

// Active reports whether this reservation reserved capacity (and therefore needs
// reconciling). False for the zero value (limiter disabled or request rejected).
func (r Reservation) Active() bool { return r.active }

type window struct {
	mu       sync.Mutex
	start    time.Time
	requests int
	tokens   int
}

// Limiter is safe for concurrent use. A nil *Limiter behaves as "disabled".
type Limiter struct {
	cfg   Config
	now   func() time.Time // injectable clock (tests)
	mu    sync.RWMutex     // guards tasks map lookup/creation only
	tasks map[string]*window
}

// New builds a Limiter. CharsPerToken defaults to 4 when unset.
func New(cfg Config) *Limiter {
	if cfg.CharsPerToken <= 0 {
		cfg.CharsPerToken = 4
	}
	return &Limiter{cfg: cfg, now: time.Now, tasks: make(map[string]*window)}
}

// Enabled reports whether gating is active (nil-safe).
func (l *Limiter) Enabled() bool { return l != nil && l.cfg.Enabled }

// Window returns the configured window length (for logging/diagnostics).
func (l *Limiter) Window() time.Duration {
	if l == nil {
		return 0
	}
	return l.cfg.Window
}

// Estimate returns the rough input-token cost of a request: text length over
// CharsPerToken (rounded up), plus a flat per-image cost. Deliberately a cheap
// over-estimate — the limiter would rather gate slightly early than under-count.
func (l *Limiter) Estimate(text string, images int) int {
	if l == nil {
		return 0
	}
	cpt := l.cfg.CharsPerToken
	if cpt <= 0 {
		cpt = 4
	}
	est := (len(text) + cpt - 1) / cpt
	if images > 0 {
		est += images * l.cfg.TokensPerImage
	}
	return est
}

func (l *Limiter) windowFor(taskID string) *window {
	l.mu.RLock()
	w := l.tasks[taskID]
	l.mu.RUnlock()
	if w != nil {
		return w
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if w = l.tasks[taskID]; w == nil {
		w = &window{start: l.now()}
		l.tasks[taskID] = w
	}
	return w
}

// Reserve runs the three gates for one request against the task's current
// window. On success it reserves a request slot and the estimated tokens and
// returns an active Reservation; the caller MUST later call Reconcile (even when
// the request fails) to settle the estimate to actual usage.
func (l *Limiter) Reserve(taskID string, estInputTokens int) (Reservation, Decision) {
	if !l.Enabled() {
		return Reservation{}, Decision{Allowed: true}
	}
	if l.cfg.MaxInputTokens > 0 && estInputTokens > l.cfg.MaxInputTokens {
		return Reservation{}, Decision{
			Allowed: false,
			Code:    InputTooLarge,
			Message: fmt.Sprintf(
				"input too large: ~%d estimated tokens exceeds the per-request limit of %d",
				estInputTokens, l.cfg.MaxInputTokens),
		}
	}

	w := l.windowFor(taskID)
	w.mu.Lock()
	defer w.mu.Unlock()

	now := l.now()
	if now.Sub(w.start) >= l.cfg.Window {
		w.start = now
		w.requests = 0
		w.tokens = 0
	}
	retry := l.cfg.Window - now.Sub(w.start)

	if l.cfg.MaxRequests > 0 && w.requests+1 > l.cfg.MaxRequests {
		return Reservation{}, Decision{
			Allowed: false, Code: RequestRate, RetryAfter: retry,
			Message: fmt.Sprintf("request rate limit exceeded: max %d requests per %s for this task",
				l.cfg.MaxRequests, l.cfg.Window),
		}
	}
	if l.cfg.MaxTokens > 0 && w.tokens+estInputTokens > l.cfg.MaxTokens {
		return Reservation{}, Decision{
			Allowed: false, Code: TokenBudget, RetryAfter: retry,
			Message: fmt.Sprintf("token budget exhausted: max %d tokens per %s for this task",
				l.cfg.MaxTokens, l.cfg.Window),
		}
	}

	w.requests++
	w.tokens += estInputTokens
	return Reservation{taskID: taskID, windowStart: w.start, estTokens: estInputTokens, active: true},
		Decision{Allowed: true}
}

// Reconcile settles a reservation against the tokens actually consumed. If the
// window has rolled since the reservation was taken, the reservation expired
// with it and is dropped. The request count is never rolled back — a request
// that ran counts even if it failed.
func (l *Limiter) Reconcile(r Reservation, actualTokens int) {
	if !r.active || !l.Enabled() {
		return
	}
	l.mu.RLock()
	w := l.tasks[r.taskID]
	l.mu.RUnlock()
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.start.Equal(r.windowStart) {
		return // window rolled over; the reservation no longer applies
	}
	w.tokens += actualTokens - r.estTokens
	if w.tokens < 0 {
		w.tokens = 0
	}
}
