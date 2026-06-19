package ratelimit

import (
	"sync"
	"testing"
	"time"
)

// clock is a manually-advanced time source for deterministic window tests.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// withClock builds a limiter wired to a manual clock starting at a fixed instant.
func withClock(cfg Config) (*Limiter, *clock) {
	l := New(cfg)
	c := &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	l.now = c.now
	return l, c
}

func TestDisabledAllowsEverything(t *testing.T) {
	l := New(Config{Enabled: false, MaxRequests: 1, MaxTokens: 1, MaxInputTokens: 1})
	for i := 0; i < 5; i++ {
		if _, d := l.Reserve("t", 1_000_000); !d.Allowed {
			t.Fatalf("disabled limiter must allow everything, got %+v", d)
		}
	}
}

func TestNilLimiterIsDisabled(t *testing.T) {
	var l *Limiter
	if l.Enabled() {
		t.Fatal("nil limiter must report disabled")
	}
	// Reconcile on a zero reservation + nil limiter must not panic.
	l.Reconcile(Reservation{}, 100)
}

func TestInputTooLarge(t *testing.T) {
	l, _ := withClock(Config{Enabled: true, Window: time.Minute, MaxInputTokens: 100})
	res, d := l.Reserve("t", 101)
	if d.Allowed {
		t.Fatal("input over the per-request cap must be rejected")
	}
	if d.Code != InputTooLarge {
		t.Errorf("code: got %q, want %q", d.Code, InputTooLarge)
	}
	if res.Active() {
		t.Error("a rejected request must not hold a reservation")
	}
	// Exactly at the cap is allowed.
	if _, d := l.Reserve("t", 100); !d.Allowed {
		t.Errorf("input at the cap should be allowed, got %+v", d)
	}
}

func TestRequestRateLimit(t *testing.T) {
	l, _ := withClock(Config{Enabled: true, Window: time.Minute, MaxRequests: 2})
	if _, d := l.Reserve("t", 1); !d.Allowed {
		t.Fatal("request 1 should pass")
	}
	if _, d := l.Reserve("t", 1); !d.Allowed {
		t.Fatal("request 2 should pass")
	}
	_, d := l.Reserve("t", 1)
	if d.Allowed {
		t.Fatal("request 3 should be rejected by the request-rate cap")
	}
	if d.Code != RequestRate {
		t.Errorf("code: got %q, want %q", d.Code, RequestRate)
	}
	if d.RetryAfter <= 0 || d.RetryAfter > time.Minute {
		t.Errorf("retry-after should be within the window, got %s", d.RetryAfter)
	}
}

func TestTokenBudgetReserveUpfront(t *testing.T) {
	l, _ := withClock(Config{Enabled: true, Window: time.Minute, MaxTokens: 100})
	if _, d := l.Reserve("t", 60); !d.Allowed {
		t.Fatal("60/100 should pass")
	}
	// 60 + 60 = 120 > 100 — rejected before running, even though nothing has
	// actually been consumed yet (reserve-upfront).
	_, d := l.Reserve("t", 60)
	if d.Allowed {
		t.Fatal("a request whose estimate would exceed the budget must be rejected upfront")
	}
	if d.Code != TokenBudget {
		t.Errorf("code: got %q, want %q", d.Code, TokenBudget)
	}
}

func TestReconcileToActualBelowEstimate(t *testing.T) {
	l, _ := withClock(Config{Enabled: true, Window: time.Minute, MaxTokens: 100})
	res, d := l.Reserve("t", 80)
	if !d.Allowed {
		t.Fatal("80/100 should pass")
	}
	// Actually used only 10 — reconcile frees the rest, so a 70-token request fits.
	l.Reconcile(res, 10)
	if _, d := l.Reserve("t", 70); !d.Allowed {
		t.Fatalf("after reconciling down to 10 used, 70 more should fit, got %+v", d)
	}
}

func TestReconcileToActualAboveEstimate(t *testing.T) {
	// Models can consume more than estimated (incl. failed/fallback attempts).
	// Reconcile records the overflow so later requests in the window are gated.
	l, _ := withClock(Config{Enabled: true, Window: time.Minute, MaxTokens: 100})
	res, _ := l.Reserve("t", 30)
	l.Reconcile(res, 95) // real usage blew past the estimate
	_, d := l.Reserve("t", 20)
	if d.Allowed {
		t.Fatal("95 used + 20 estimate = 115 > 100 must be rejected")
	}
	if d.Code != TokenBudget {
		t.Errorf("code: got %q, want %q", d.Code, TokenBudget)
	}
}

func TestWindowRollResetsCounters(t *testing.T) {
	l, c := withClock(Config{Enabled: true, Window: time.Minute, MaxRequests: 1, MaxTokens: 100})
	if _, d := l.Reserve("t", 100); !d.Allowed {
		t.Fatal("first request should pass")
	}
	if _, d := l.Reserve("t", 1); d.Allowed {
		t.Fatal("second request in the same window should be rejected")
	}
	c.advance(time.Minute) // window elapses
	if _, d := l.Reserve("t", 100); !d.Allowed {
		t.Fatalf("after the window rolls, counters reset and the request should pass, got %+v", d)
	}
}

func TestReconcileAfterWindowRollIsNoop(t *testing.T) {
	l, c := withClock(Config{Enabled: true, Window: time.Minute, MaxTokens: 100})
	res, _ := l.Reserve("t", 50) // window A
	c.advance(time.Minute)       // roll to window B
	// Reserving in window B resets to 0 then reserves 40.
	if _, d := l.Reserve("t", 40); !d.Allowed {
		t.Fatal("fresh window should accept 40")
	}
	// Reconciling the window-A reservation must NOT touch window B.
	l.Reconcile(res, 90)
	if _, d := l.Reserve("t", 60); !d.Allowed {
		t.Fatalf("window B should still have 60 free (40 used), got %+v", d)
	}
}

func TestPerTaskIsolation(t *testing.T) {
	l, _ := withClock(Config{Enabled: true, Window: time.Minute, MaxRequests: 1})
	if _, d := l.Reserve("task-a", 1); !d.Allowed {
		t.Fatal("task-a first request should pass")
	}
	if _, d := l.Reserve("task-a", 1); d.Allowed {
		t.Fatal("task-a second request should be rejected")
	}
	// task-b has its own window — unaffected by task-a being exhausted.
	if _, d := l.Reserve("task-b", 1); !d.Allowed {
		t.Fatal("task-b must have an independent budget")
	}
}

func TestEstimate(t *testing.T) {
	l := New(Config{CharsPerToken: 4, TokensPerImage: 1000})
	// 8 chars / 4 = 2 tokens, + 2 images * 1000.
	if got := l.Estimate("abcdefgh", 2); got != 2002 {
		t.Errorf("estimate: got %d, want 2002", got)
	}
	// Rounding up: 5 chars / 4 = 2 (ceil).
	if got := l.Estimate("abcde", 0); got != 2 {
		t.Errorf("estimate ceil: got %d, want 2", got)
	}
}

func TestConcurrentReserveIsConsistent(t *testing.T) {
	// The window must never accept more than MaxRequests even under concurrency.
	l, _ := withClock(Config{Enabled: true, Window: time.Minute, MaxRequests: 50})
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, d := l.Reserve("t", 1); d.Allowed {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allowed != 50 {
		t.Fatalf("exactly MaxRequests should be admitted, got %d", allowed)
	}
}
