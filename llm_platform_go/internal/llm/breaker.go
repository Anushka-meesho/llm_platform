package llm

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Circuit breaker per provider (design doc §3.3): after `threshold` consecutive
// infrastructure failures the circuit opens and calls fail fast; after
// `cooldown` one probe request is allowed (half-open); a successful probe
// closes the circuit, a failed one re-opens it.

type breakerState int

const (
	stateClosed breakerState = iota
	stateOpen
	stateHalfOpen
)

func (s breakerState) String() string {
	switch s {
	case stateOpen:
		return "open"
	case stateHalfOpen:
		return "half-open"
	default:
		return "closed"
	}
}

type breakerEntry struct {
	state    breakerState
	failures int       // consecutive infra failures while closed
	openedAt time.Time // when the circuit opened
	probing  bool      // half-open: a probe is already in flight
}

// BreakerSet tracks one circuit per key (provider name).
type BreakerSet struct {
	mu        sync.Mutex
	entries   map[string]*breakerEntry
	threshold int
	cooldown  time.Duration
	now       func() time.Time // injectable for tests
	probeOnly bool             // open circuits recover only via out-of-band probes
}

// NewBreakerSet returns a set with production defaults: trip after 3
// consecutive infra failures, allow a probe after 30s.
func NewBreakerSet() *BreakerSet {
	return &BreakerSet{
		entries:   map[string]*breakerEntry{},
		threshold: 3,
		cooldown:  30 * time.Second,
		now:       time.Now,
	}
}

// NewBreakerSetForTest allows tests to control thresholds and the clock.
func NewBreakerSetForTest(threshold int, cooldown time.Duration, now func() time.Time) *BreakerSet {
	return &BreakerSet{
		entries:   map[string]*breakerEntry{},
		threshold: threshold,
		cooldown:  cooldown,
		now:       now,
	}
}

func (b *BreakerSet) get(key string) *breakerEntry {
	e, ok := b.entries[key]
	if !ok {
		e = &breakerEntry{}
		b.entries[key] = e
	}
	return e
}

// Allow reports whether a call to this provider may proceed, transitioning
// open → half-open after the cooldown (admitting exactly one probe).
func (b *BreakerSet) Allow(key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	e := b.get(key)

	switch e.state {
	case stateClosed:
		return true
	case stateOpen:
		if b.probeOnly {
			// The background recovery prober owns recovery — production
			// requests fail fast to the next model in the chain, always.
			return false
		}
		if b.now().Sub(e.openedAt) >= b.cooldown {
			e.state = stateHalfOpen
			e.probing = true
			return true
		}
		return false
	case stateHalfOpen:
		if e.probing {
			return false // one probe at a time
		}
		e.probing = true
		return true
	}
	return true
}

// RecordSuccess resets the circuit (a half-open probe success closes it).
func (b *BreakerSet) RecordSuccess(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e := b.get(key)
	e.state = stateClosed
	e.failures = 0
	e.probing = false
}

// RecordFailure counts an infra failure; trips the circuit at the threshold,
// and re-opens immediately on a failed half-open probe.
func (b *BreakerSet) RecordFailure(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e := b.get(key)

	switch e.state {
	case stateHalfOpen:
		e.state = stateOpen
		e.openedAt = b.now()
		e.probing = false
	case stateClosed:
		e.failures++
		if e.failures >= b.threshold {
			e.state = stateOpen
			e.openedAt = b.now()
		}
	}
}

// State returns the circuit state for observability ("closed"/"open"/"half-open").
func (b *BreakerSet) State(key string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.get(key).state.String()
}

// SetProbeOnly switches recovery ownership to the background prober: open
// circuits never half-open for production traffic; only RecordSuccess (from a
// successful out-of-band probe) closes them.
func (b *BreakerSet) SetProbeOnly(v bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probeOnly = v
}

// Unhealthy returns the keys whose circuit is currently not closed — the
// recovery prober's worklist.
func (b *BreakerSet) Unhealthy() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for k, e := range b.entries {
		if e.state != stateClosed {
			out = append(out, k)
		}
	}
	return out
}

// defaultBreakers is the process-wide breaker set used by CallModel.
var defaultBreakers = NewBreakerSet()

// ResetBreakers clears all circuit state (tests).
func ResetBreakers() {
	defaultBreakers.mu.Lock()
	defer defaultBreakers.mu.Unlock()
	defaultBreakers.entries = map[string]*breakerEntry{}
	defaultBreakers.probeOnly = false
}

// isInfraFailure reports whether an error indicates provider infrastructure
// trouble (worth tripping the breaker / falling back) as opposed to a request
// configuration problem (4xx other than 429) or caller cancellation.
func isInfraFailure(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false // caller went away — not the provider's fault
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.HTTPStatusCode == 429 || apiErr.HTTPStatusCode >= 500
	}
	// Network errors, timeouts, deadline exceeded, malformed responses.
	return true
}
