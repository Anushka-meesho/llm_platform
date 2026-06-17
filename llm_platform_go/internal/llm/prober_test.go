package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// flakyProvider is a fake OpenAI-compatible server whose health is togglable,
// counting how many requests reach it.
func flakyProvider(t *testing.T) (*httptest.Server, *atomic.Bool, *atomic.Int64) {
	t.Helper()
	var healthy atomic.Bool
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if !healthy.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"down"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "ok"}}},
			"usage":   map[string]any{"prompt_tokens": 1, "completion_tokens": 1},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, &healthy, &calls
}

// tripBreaker forces a provider's circuit open via the production default set.
func tripBreaker(provider string) {
	for i := 0; i < 3; i++ {
		defaultBreakers.RecordFailure(provider)
	}
}

func TestProbeOnlyNeverHalfOpensForProduction(t *testing.T) {
	clock := time.Now()
	b := NewBreakerSetForTest(3, 30*time.Second, func() time.Time { return clock })
	b.SetProbeOnly(true)

	for i := 0; i < 3; i++ {
		b.RecordFailure("openai")
	}
	if b.State("openai") != "open" {
		t.Fatalf("state: got %s, want open", b.State("openai"))
	}

	// Far past the cooldown: classic breakers would half-open and admit a
	// production probe; probe-only must keep failing fast.
	clock = clock.Add(10 * time.Minute)
	if b.Allow("openai") {
		t.Fatal("probe-only breaker must not admit production traffic while open")
	}

	// Only an out-of-band probe success reopens the path.
	b.RecordSuccess("openai")
	if !b.Allow("openai") {
		t.Fatal("circuit must close after a successful probe")
	}
}

func TestProberClosesCircuitWhenProviderRecovers(t *testing.T) {
	ResetBreakers()
	t.Cleanup(ResetBreakers)
	defaultBreakers.SetProbeOnly(true)

	srv, healthy, _ := flakyProvider(t)
	clients := &Clients{Meesho: NewOpenAICompatProvider(srv.URL, "test-key")}

	tripBreaker("openai")
	if got := defaultBreakers.State("openai"); got != "open" {
		t.Fatalf("state: got %s, want open", got)
	}

	// Provider still down: probe must not close the circuit.
	probeUnhealthy(context.Background(), clients)
	if got := defaultBreakers.State("openai"); got != "open" {
		t.Fatalf("state after failed probe: got %s, want open", got)
	}

	// Provider recovers: the next probe closes the circuit.
	healthy.Store(true)
	probeUnhealthy(context.Background(), clients)
	if got := defaultBreakers.State("openai"); got != "closed" {
		t.Fatalf("state after successful probe: got %s, want closed", got)
	}
}

// The full algorithm: while the primary is down, production requests fail
// fast to the fallback without touching the primary; once the prober sees
// the primary recover, the next request is served by it again.
func TestTrafficReturnsToPrimaryAfterProbeRecovery(t *testing.T) {
	ResetBreakers()
	t.Cleanup(ResetBreakers)
	defaultBreakers.SetProbeOnly(true)

	primarySrv, primaryHealthy, primaryCalls := flakyProvider(t)
	fallbackSrv, fallbackHealthy, _ := flakyProvider(t)
	fallbackHealthy.Store(true)

	clients := &Clients{
		Meesho: NewOpenAICompatProvider(primarySrv.URL, "k"), // gpt-4o-mini via bifrost
		Groq:   NewOpenAICompatProvider(fallbackSrv.URL, "k"), // llama-groq
	}
	chain := []string{"gpt-4o-mini", "llama-groq"}
	messages := []ChatMessage{{Role: "user", Content: "hi"}}

	tripBreaker("openai")
	callsBefore := primaryCalls.Load()

	// Down period: fallback serves, primary is never called.
	res := CallWithFallback(context.Background(), clients, chain, messages, 0, 16)
	if !res.Success || res.Model != "llama-groq" || !res.FallbackUsed {
		t.Fatalf("during outage: got model=%s success=%v fallback=%v, want llama-groq via fallback",
			res.Model, res.Success, res.FallbackUsed)
	}
	if primaryCalls.Load() != callsBefore {
		t.Fatal("production request must not touch a provider with an open circuit")
	}

	// Primary recovers; prober notices; traffic returns to it.
	primaryHealthy.Store(true)
	probeUnhealthy(context.Background(), clients)

	res = CallWithFallback(context.Background(), clients, chain, messages, 0, 16)
	if !res.Success || res.Model != "gpt-4o-mini" || res.FallbackUsed {
		t.Fatalf("after recovery: got model=%s fallback=%v, want primary gpt-4o-mini",
			res.Model, res.FallbackUsed)
	}
}
