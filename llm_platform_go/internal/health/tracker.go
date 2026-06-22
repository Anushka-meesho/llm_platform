// Package health implements a per-(task, model) circuit breaker that drives the
// task fallback chain. Unlike the provider-wide breaker in internal/llm
// (keyed on "openai"/"gemini"/…), this one is keyed on a specific task's use of
// a specific model, so a model that misbehaves for one task is routed around
// only for that task.
//
// Model of operation:
//   - Every failed call (provider error OR schema-invalid output) increments a
//     consecutive-failure counter for that (task, model).
//   - After `Threshold` consecutive failures the model is tripped UNHEALTHY for
//     a cooldown window; while unhealthy it is skipped entirely (no call made).
//   - When the window elapses, one PROBING trial call is allowed. If it
//     succeeds the model is healthy again; if it fails the model re-trips with
//     a longer cooldown (exponential backoff, capped at MaxCooldown).
//   - An admin can force any model back to healthy at any time (Reset).
//
// Live state is in-process (like the provider breaker); transitions are emitted
// to a sink for durable persistence/observation.
package health

import (
	"sort"
	"sync"
	"time"

	"llm_platform_go/internal/types"
)

// Circuit states.
const (
	StateHealthy   = "healthy"
	StateUnhealthy = "unhealthy"
	StateProbing   = "probing"
)

// Config tunes the breaker. Zero/negative fields fall back to sane defaults.
type Config struct {
	Enabled      bool
	Threshold    int           // consecutive failures before tripping
	BaseCooldown time.Duration // first unhealthy window
	MaxCooldown  time.Duration // cap for the backed-off window
	Factor       int           // cooldown multiplier per re-trip
}

type entry struct {
	provider       string
	consecutive    int
	state          string
	openUntil      time.Time
	trips          int
	cooldown       time.Duration
	totalFailures  int
	totalSuccesses int
	lastReason     string
	lastError      string
	lastChange     time.Time
}

type key struct{ task, model string }

// Tracker holds the live per-(task, model) circuit state.
type Tracker struct {
	mu      sync.Mutex
	cfg     Config
	entries map[key]*entry
	now     func() time.Time           // injectable for tests
	sink    func(types.HealthEvent)    // durable event persistence; may be nil
}

// NewTracker builds a tracker. sink (nullable) receives every state-transition
// event for persistence — it is called OUTSIDE the lock, so it may block.
func NewTracker(cfg Config, sink func(types.HealthEvent)) *Tracker {
	if cfg.Threshold <= 0 {
		cfg.Threshold = 3
	}
	if cfg.BaseCooldown <= 0 {
		cfg.BaseCooldown = 30 * time.Second
	}
	if cfg.MaxCooldown <= 0 {
		cfg.MaxCooldown = 30 * time.Minute
	}
	if cfg.Factor <= 1 {
		cfg.Factor = 2
	}
	return &Tracker{cfg: cfg, entries: map[key]*entry{}, now: time.Now, sink: sink}
}

// Enabled reports whether the breaker is active.
func (t *Tracker) Enabled() bool { return t != nil && t.cfg.Enabled }

// Allow reports whether a live call to (task, model) may proceed now, and the
// state observed. A healthy model is always allowed; an unhealthy one is
// blocked until its cooldown elapses, after which a single probing trial is
// permitted (best-effort under concurrency).
func (t *Tracker) Allow(task, model string) (bool, string) {
	if !t.Enabled() {
		return true, StateHealthy
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.entries[key{task, model}]
	if e == nil || e.state == StateHealthy {
		return true, StateHealthy
	}
	if e.state == StateProbing {
		return true, StateProbing
	}
	// Unhealthy: allow a trial once the cooldown window has elapsed.
	if !t.now().Before(e.openUntil) {
		e.state = StateProbing
		e.lastChange = t.now()
		return true, StateProbing
	}
	return false, StateUnhealthy
}

// RecordSuccess marks a successful, usable response for (task, model): it clears
// the consecutive-failure counter and, if the model was unhealthy/probing,
// recovers it to healthy.
func (t *Tracker) RecordSuccess(task, model, provider string) {
	if !t.Enabled() {
		return
	}
	t.mu.Lock()
	e := t.ensure(task, model)
	e.provider = provider
	e.totalSuccesses++
	recovered := e.state != StateHealthy
	e.consecutive = 0
	e.state = StateHealthy
	e.openUntil = time.Time{}
	e.cooldown = 0
	e.trips = 0
	var ev *types.HealthEvent
	if recovered {
		e.lastChange = t.now()
		ev = t.eventLocked(task, model, provider, "recovered", "model responded successfully", e)
	}
	t.mu.Unlock()
	t.emit(ev)
}

// RecordFailure marks a failed/unusable response for (task, model) — a provider
// error or a schema-invalid output. It trips the model unhealthy once the
// failures cross the threshold, and re-trips with a longer cooldown if a
// probing trial fails.
func (t *Tracker) RecordFailure(task, model, provider, reason string) {
	if !t.Enabled() {
		return
	}
	t.mu.Lock()
	e := t.ensure(task, model)
	e.provider = provider
	e.totalFailures++
	e.consecutive++
	e.lastReason = reason
	e.lastError = reason

	var ev *types.HealthEvent
	switch {
	case e.state == StateProbing:
		// Trial failed → straight back to unhealthy with a longer window.
		t.trip(e)
		ev = t.eventLocked(task, model, provider, "tripped", reason, e)
	case e.consecutive >= t.cfg.Threshold:
		t.trip(e)
		ev = t.eventLocked(task, model, provider, "tripped", reason, e)
	default:
		ev = t.eventLocked(task, model, provider, "failure", reason, e)
	}
	t.mu.Unlock()
	t.emit(ev)
}

// Reset forces (task, model) back to healthy (admin override). Returns false if
// the pair was never seen. `by` is recorded on the emitted event.
func (t *Tracker) Reset(task, model, by string) bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	e := t.entries[key{task, model}]
	if e == nil {
		t.mu.Unlock()
		return false
	}
	provider := e.provider
	e.consecutive = 0
	e.state = StateHealthy
	e.openUntil = time.Time{}
	e.cooldown = 0
	e.trips = 0
	e.lastReason = "manual reset by " + by
	e.lastChange = t.now()
	ev := t.eventLocked(task, model, provider, "manual_reset", e.lastReason, e)
	t.mu.Unlock()
	t.emit(ev)
	return true
}

// Snapshot returns the live state of every tracked (task, model), sorted.
func (t *Tracker) Snapshot() []types.ModelHealthStatus {
	out := []types.ModelHealthStatus{}
	if t == nil {
		return out
	}
	t.mu.Lock()
	now := t.now()
	for k, e := range t.entries {
		st := types.ModelHealthStatus{
			TaskID:              k.task,
			Model:               k.model,
			Provider:            e.provider,
			State:               e.state,
			ConsecutiveFailures: e.consecutive,
			TotalFailures:       e.totalFailures,
			TotalSuccesses:      e.totalSuccesses,
			Trips:               e.trips,
			CooldownMs:          int(e.cooldown / time.Millisecond),
			LastReason:          e.lastReason,
			LastError:           e.lastError,
			LastChange:          e.lastChange,
		}
		if e.state == StateUnhealthy && !e.openUntil.IsZero() {
			if secs := int(e.openUntil.Sub(now).Seconds()); secs > 0 {
				st.OpenForSeconds = secs
			}
		}
		out = append(out, st)
	}
	t.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].TaskID != out[j].TaskID {
			return out[i].TaskID < out[j].TaskID
		}
		return out[i].Model < out[j].Model
	})
	return out
}

// trip moves an entry to unhealthy with the next (backed-off) cooldown. Caller
// holds the lock.
func (t *Tracker) trip(e *entry) {
	e.trips++
	e.cooldown = t.backoff(e.trips)
	e.state = StateUnhealthy
	e.openUntil = t.now().Add(e.cooldown)
	e.lastChange = t.now()
}

// backoff returns the cooldown for the nth trip: Base * Factor^(n-1), capped.
func (t *Tracker) backoff(trips int) time.Duration {
	d := t.cfg.BaseCooldown
	for i := 1; i < trips; i++ {
		d *= time.Duration(t.cfg.Factor)
		if d >= t.cfg.MaxCooldown {
			return t.cfg.MaxCooldown
		}
	}
	if d > t.cfg.MaxCooldown {
		return t.cfg.MaxCooldown
	}
	return d
}

func (t *Tracker) ensure(task, model string) *entry {
	k := key{task, model}
	e := t.entries[k]
	if e == nil {
		e = &entry{state: StateHealthy, lastChange: t.now()}
		t.entries[k] = e
	}
	return e
}

func (t *Tracker) eventLocked(task, model, provider, kind, reason string, e *entry) *types.HealthEvent {
	return &types.HealthEvent{
		TaskID:              task,
		Model:               model,
		Provider:            provider,
		Event:               kind,
		Reason:              reason,
		ConsecutiveFailures: e.consecutive,
		CooldownMs:          int(e.cooldown / time.Millisecond),
		State:               e.state,
		CreatedAt:           t.now(),
	}
}

func (t *Tracker) emit(ev *types.HealthEvent) {
	if ev != nil && t.sink != nil {
		t.sink(*ev)
	}
}
