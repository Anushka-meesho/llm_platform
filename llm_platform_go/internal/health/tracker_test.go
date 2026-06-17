package health

import (
	"testing"
	"time"

	"llm_platform_go/internal/types"
)

const task = "attribute-extraction"
const model = "gemini-2.5-flash"

// newTestTracker returns a tracker with a controllable clock and an event sink
// that appends to *events.
func newTestTracker(t *testing.T, cfg Config, events *[]types.HealthEvent) (*Tracker, *time.Time) {
	t.Helper()
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tr := NewTracker(cfg, func(e types.HealthEvent) { *events = append(*events, e) })
	tr.now = func() time.Time { return clock }
	return tr, &clock
}

func TestTripsAfterThreshold(t *testing.T) {
	var events []types.HealthEvent
	tr, _ := newTestTracker(t, Config{Enabled: true, Threshold: 3, BaseCooldown: time.Second, MaxCooldown: time.Minute, Factor: 2}, &events)

	if ok, _ := tr.Allow(task, model); !ok {
		t.Fatal("fresh model should be allowed")
	}
	tr.RecordFailure(task, model, "gemini", "boom")
	tr.RecordFailure(task, model, "gemini", "boom")
	if ok, st := tr.Allow(task, model); !ok || st != StateHealthy {
		t.Fatalf("below threshold should stay allowed/healthy, got ok=%v st=%s", ok, st)
	}
	tr.RecordFailure(task, model, "gemini", "boom") // 3rd → trip
	if ok, st := tr.Allow(task, model); ok || st != StateUnhealthy {
		t.Fatalf("at threshold should be blocked/unhealthy, got ok=%v st=%s", ok, st)
	}

	// One "tripped" event must have been emitted.
	var tripped int
	for _, e := range events {
		if e.Event == "tripped" {
			tripped++
		}
	}
	if tripped != 1 {
		t.Errorf("want exactly 1 tripped event, got %d (%+v)", tripped, events)
	}
}

func TestProbeRecovers(t *testing.T) {
	var events []types.HealthEvent
	tr, clock := newTestTracker(t, Config{Enabled: true, Threshold: 1, BaseCooldown: 10 * time.Second, MaxCooldown: time.Minute, Factor: 2}, &events)

	tr.RecordFailure(task, model, "gemini", "boom") // threshold 1 → trip immediately
	if ok, _ := tr.Allow(task, model); ok {
		t.Fatal("should be blocked right after trip")
	}
	// Advance past the cooldown → a probing trial is allowed.
	*clock = clock.Add(11 * time.Second)
	if ok, st := tr.Allow(task, model); !ok || st != StateProbing {
		t.Fatalf("after cooldown want probing/allowed, got ok=%v st=%s", ok, st)
	}
	tr.RecordSuccess(task, model, "gemini") // probe succeeds → healthy
	if ok, st := tr.Allow(task, model); !ok || st != StateHealthy {
		t.Fatalf("after successful probe want healthy/allowed, got ok=%v st=%s", ok, st)
	}
	snaps := tr.Snapshot()
	if len(snaps) != 1 || snaps[0].ConsecutiveFailures != 0 || snaps[0].Trips != 0 {
		t.Errorf("recovery should reset counters: %+v", snaps)
	}
}

func TestBackoffGrowsOnReTrip(t *testing.T) {
	var events []types.HealthEvent
	tr, clock := newTestTracker(t, Config{Enabled: true, Threshold: 1, BaseCooldown: time.Second, MaxCooldown: time.Hour, Factor: 2}, &events)

	tr.RecordFailure(task, model, "gemini", "boom") // trip 1 → cooldown 1s
	if cd := tr.Snapshot()[0].CooldownMs; cd != 1000 {
		t.Fatalf("first cooldown want 1000ms, got %d", cd)
	}
	*clock = clock.Add(2 * time.Second)
	if ok, _ := tr.Allow(task, model); !ok { // → probing
		t.Fatal("should allow a probe after cooldown")
	}
	tr.RecordFailure(task, model, "gemini", "boom again") // probe fails → re-trip, cooldown 2s
	if cd := tr.Snapshot()[0].CooldownMs; cd != 2000 {
		t.Errorf("second cooldown want 2000ms (doubled), got %d", cd)
	}
}

func TestBackoffCappedAtMax(t *testing.T) {
	var events []types.HealthEvent
	tr, clock := newTestTracker(t, Config{Enabled: true, Threshold: 1, BaseCooldown: time.Second, MaxCooldown: 3 * time.Second, Factor: 2}, &events)
	// 1s, 2s, then capped at 3s (would be 4s).
	for i := 0; i < 3; i++ {
		tr.RecordFailure(task, model, "gemini", "boom")
		*clock = clock.Add(10 * time.Second)
		tr.Allow(task, model) // → probing so the next failure re-trips
	}
	if cd := tr.Snapshot()[0].CooldownMs; cd != 3000 {
		t.Errorf("cooldown should cap at 3000ms, got %d", cd)
	}
}

func TestManualReset(t *testing.T) {
	var events []types.HealthEvent
	tr, _ := newTestTracker(t, Config{Enabled: true, Threshold: 1, BaseCooldown: time.Minute, MaxCooldown: time.Hour, Factor: 2}, &events)

	tr.RecordFailure(task, model, "gemini", "boom") // trip
	if ok, _ := tr.Allow(task, model); ok {
		t.Fatal("should be blocked after trip")
	}
	if !tr.Reset(task, model, "admin@demo.local") {
		t.Fatal("reset of a tracked pair should return true")
	}
	if ok, st := tr.Allow(task, model); !ok || st != StateHealthy {
		t.Fatalf("after reset want healthy/allowed, got ok=%v st=%s", ok, st)
	}
	if tr.Reset("nope", "nope", "admin") {
		t.Error("reset of an unknown pair should return false")
	}
	var resets int
	for _, e := range events {
		if e.Event == "manual_reset" {
			resets++
		}
	}
	if resets != 1 {
		t.Errorf("want 1 manual_reset event, got %d", resets)
	}
}

func TestDisabledAlwaysAllows(t *testing.T) {
	var events []types.HealthEvent
	tr, _ := newTestTracker(t, Config{Enabled: false, Threshold: 1}, &events)
	for i := 0; i < 5; i++ {
		tr.RecordFailure(task, model, "gemini", "boom")
	}
	if ok, _ := tr.Allow(task, model); !ok {
		t.Error("disabled breaker must always allow")
	}
	if len(tr.Snapshot()) != 0 || len(events) != 0 {
		t.Error("disabled breaker must not track state or emit events")
	}
}

func TestNilTrackerSafe(t *testing.T) {
	var tr *Tracker
	if tr.Enabled() {
		t.Error("nil tracker should report disabled")
	}
	if got := tr.Snapshot(); got == nil || len(got) != 0 {
		t.Error("nil tracker snapshot should be empty, non-nil")
	}
}
