package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBreakerStateMachine(t *testing.T) {
	clock := time.Unix(1000, 0)
	now := func() time.Time { return clock }
	b := NewBreakerSetForTest(3, 30*time.Second, now)

	const key = "prov"

	// Closed: allows; failures below threshold keep it closed.
	if !b.Allow(key) {
		t.Fatal("closed circuit should allow")
	}
	b.RecordFailure(key)
	b.RecordFailure(key)
	if b.State(key) != "closed" {
		t.Fatalf("state after 2 failures: %s, want closed", b.State(key))
	}

	// Third consecutive failure trips it.
	b.RecordFailure(key)
	if b.State(key) != "open" {
		t.Fatalf("state after 3 failures: %s, want open", b.State(key))
	}
	if b.Allow(key) {
		t.Fatal("open circuit should reject")
	}

	// Cooldown elapses → half-open admits exactly one probe.
	clock = clock.Add(31 * time.Second)
	if !b.Allow(key) {
		t.Fatal("after cooldown the first probe should be allowed")
	}
	if b.State(key) != "half-open" {
		t.Fatalf("state: %s, want half-open", b.State(key))
	}
	if b.Allow(key) {
		t.Fatal("second concurrent probe should be rejected")
	}

	// Failed probe re-opens.
	b.RecordFailure(key)
	if b.State(key) != "open" {
		t.Fatalf("state after failed probe: %s, want open", b.State(key))
	}

	// Next cooldown → probe succeeds → closed.
	clock = clock.Add(31 * time.Second)
	if !b.Allow(key) {
		t.Fatal("probe after second cooldown should be allowed")
	}
	b.RecordSuccess(key)
	if b.State(key) != "closed" {
		t.Fatalf("state after successful probe: %s, want closed", b.State(key))
	}

	// Success resets the consecutive-failure count.
	b.RecordFailure(key)
	b.RecordFailure(key)
	b.RecordSuccess(key)
	b.RecordFailure(key)
	b.RecordFailure(key)
	if b.State(key) != "closed" {
		t.Fatalf("interleaved successes must reset the count, state: %s", b.State(key))
	}
}

func TestIsInfraFailure(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{&APIError{HTTPStatusCode: 500, Message: "boom"}, true},
		{&APIError{HTTPStatusCode: 503, Message: "down"}, true},
		{&APIError{HTTPStatusCode: 429, Message: "slow down"}, true},
		{&APIError{HTTPStatusCode: 400, Message: "bad req"}, false},
		{&APIError{HTTPStatusCode: 401, Message: "bad key"}, false},
		{&APIError{HTTPStatusCode: 404, Message: "no model"}, false},
		{context.Canceled, false},
	}
	for _, c := range cases {
		if got := isInfraFailure(c.err); got != c.want {
			t.Errorf("isInfraFailure(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

// deadProvider points at a closed port — connection refused is an instant,
// non-retryable infra failure (fast tests, no retry sleeps).
func deadProvider() Provider {
	return NewOpenAICompatProvider("http://127.0.0.1:1", "k")
}

func okServer(t *testing.T, content string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"` + content + `"}}],
			"usage":{"prompt_tokens":5,"completion_tokens":2}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func badRequestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad request"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func authFailureServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCallWithFallback(t *testing.T) {
	ResetBreakers()
	t.Cleanup(ResetBreakers)
	_ = LoadPricingFromMap(map[string]Rate{})

	msgs := []ChatMessage{{Role: "user", Content: "hi"}}

	t.Run("primary infra failure falls back", func(t *testing.T) {
		ResetBreakers()
		ok := okServer(t, "served-by-fallback")
		clients := &Clients{
			OpenAI: deadProvider(),                          // primary gpt-4o-mini → dead
			Groq:   NewOpenAICompatProvider(ok.URL, "k"),    // fallback llama-groq → ok
		}
		r := CallWithFallback(context.Background(), clients,
			[]string{"gpt-4o-mini", "llama-groq"}, msgs, 0.1, 100)
		if !r.Success {
			t.Fatalf("expected success via fallback, got error: %v", *r.Error)
		}
		if r.Model != "llama-groq" || !r.FallbackUsed || !r.Degraded {
			t.Errorf("fallback attribution wrong: model=%s fallback=%v degraded=%v",
				r.Model, r.FallbackUsed, r.Degraded)
		}
	})

	t.Run("primary success does not fall back", func(t *testing.T) {
		ResetBreakers()
		ok := okServer(t, "primary")
		clients := &Clients{OpenAI: NewOpenAICompatProvider(ok.URL, "k"), Groq: deadProvider()}
		r := CallWithFallback(context.Background(), clients,
			[]string{"gpt-4o-mini", "llama-groq"}, msgs, 0.1, 100)
		if !r.Success || r.FallbackUsed || r.Degraded || r.Model != "gpt-4o-mini" {
			t.Errorf("unexpected: %+v", r)
		}
	})

	t.Run("primary auth failure (401) falls back", func(t *testing.T) {
		// The reported bug: a misconfigured primary (bad/missing API key → 401)
		// must advance to a working fallback provider.
		ResetBreakers()
		bad := authFailureServer(t)
		ok := okServer(t, "served-by-fallback")
		clients := &Clients{
			OpenAI: NewOpenAICompatProvider(bad.URL, ""),    // primary gpt-4o-mini → 401
			Gemini: NewOpenAICompatProvider(ok.URL, "k"),    // fallback gemini → ok
		}
		r := CallWithFallback(context.Background(), clients,
			[]string{"gpt-4o-mini", "gemini-flash"}, msgs, 0.1, 100)
		if !r.Success {
			t.Fatalf("expected success via fallback after 401, got error: %v", r.Error)
		}
		if r.Model != "gemini-flash" || !r.FallbackUsed || !r.Degraded {
			t.Errorf("401 should advance the chain: model=%s fallback=%v degraded=%v",
				r.Model, r.FallbackUsed, r.Degraded)
		}
	})

	t.Run("config error (400) does not fall back", func(t *testing.T) {
		ResetBreakers()
		bad := badRequestServer(t)
		ok := okServer(t, "should-not-be-reached")
		clients := &Clients{
			OpenAI: NewOpenAICompatProvider(bad.URL, "k"),
			Groq:   NewOpenAICompatProvider(ok.URL, "k"),
		}
		r := CallWithFallback(context.Background(), clients,
			[]string{"gpt-4o-mini", "llama-groq"}, msgs, 0.1, 100)
		if r.Success {
			t.Fatal("400 should be a definitive failure")
		}
		if r.Model != "gpt-4o-mini" || r.FallbackUsed {
			t.Errorf("400 must not advance the chain: %+v", r)
		}
	})

	t.Run("whole chain dead → degraded failure", func(t *testing.T) {
		ResetBreakers()
		clients := &Clients{OpenAI: deadProvider(), Groq: deadProvider()}
		r := CallWithFallback(context.Background(), clients,
			[]string{"gpt-4o-mini", "llama-groq"}, msgs, 0.1, 100)
		if r.Success || !r.Degraded {
			t.Errorf("expected degraded failure: %+v", r)
		}
	})

	t.Run("breaker opens after repeated failures and fails fast", func(t *testing.T) {
		ResetBreakers()
		clients := &Clients{OpenAI: deadProvider()}
		// 3 infra failures trip the openai circuit.
		for i := 0; i < 3; i++ {
			_ = CallModel(context.Background(), clients, "gpt-4o-mini", msgs, 0.1, 100)
		}
		if defaultBreakers.State("openai") != "open" {
			t.Fatalf("breaker state: %s, want open", defaultBreakers.State("openai"))
		}
		r := CallModel(context.Background(), clients, "gpt-4o-mini", msgs, 0.1, 100)
		if r.Success || r.Error == nil {
			t.Fatal("open circuit should fail")
		}
		if *r.Error != "provider circuit open — recent failures, retry shortly" {
			t.Errorf("error: %q", *r.Error)
		}
	})
}
