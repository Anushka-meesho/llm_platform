package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

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

// modelDispatchServer returns a server that inspects the "model" field in the
// request JSON and responds with the configured HTTP status per model ID.
// Use for tests where two models sharing the same client need different outcomes.
func modelDispatchServer(t *testing.T, modelStatus map[string]int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		code, ok := modelStatus[body.Model]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"unknown model"}}`))
			return
		}
		if code == http.StatusOK {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`))
			return
		}
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`{"error":{"message":"error"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCallWithFallback(t *testing.T) {
	_ = LoadPricingFromMap(map[string]Rate{})

	msgs := []ChatMessage{{Role: "user", Content: "hi"}}

	t.Run("primary infra failure falls back", func(t *testing.T) {
		ok := okServer(t, "served-by-fallback")
		clients := &Clients{
			Meesho: deadProvider(),                          // primary gpt-4o-mini → dead (via bifrost)
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
		ok := okServer(t, "primary")
		clients := &Clients{Meesho: NewOpenAICompatProvider(ok.URL, "k"), Groq: deadProvider()}
		r := CallWithFallback(context.Background(), clients,
			[]string{"gpt-4o-mini", "llama-groq"}, msgs, 0.1, 100)
		if !r.Success || r.FallbackUsed || r.Degraded || r.Model != "gpt-4o-mini" {
			t.Errorf("unexpected: %+v", r)
		}
	})

	t.Run("primary auth failure (401) falls back", func(t *testing.T) {
		// A misconfigured primary (bad/missing API key → 401) must advance to a
		// working fallback. Both models route through bifrost (meeshoC), so a
		// model-dispatch server returns different status codes per model ID.
		dispatch := modelDispatchServer(t, map[string]int{
			"openai/gpt-4o-mini":      http.StatusUnauthorized,
			"vertex/gemini-2.5-flash": http.StatusOK,
		})
		clients := &Clients{
			Meesho: NewOpenAICompatProvider(dispatch.URL, "k"),
		}
		r := CallWithFallback(context.Background(), clients,
			[]string{"gpt-4o-mini", "gemini-2.5-flash"}, msgs, 0.1, 100)
		if !r.Success {
			t.Fatalf("expected success via fallback after 401, got error: %v", r.Error)
		}
		if r.Model != "gemini-2.5-flash" || !r.FallbackUsed || !r.Degraded {
			t.Errorf("401 should advance the chain: model=%s fallback=%v degraded=%v",
				r.Model, r.FallbackUsed, r.Degraded)
		}
	})

	t.Run("config error (400) does not fall back", func(t *testing.T) {
		bad := badRequestServer(t)
		ok := okServer(t, "should-not-be-reached")
		clients := &Clients{
			Meesho: NewOpenAICompatProvider(bad.URL, "k"),
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
		clients := &Clients{Meesho: deadProvider(), Groq: deadProvider()}
		r := CallWithFallback(context.Background(), clients,
			[]string{"gpt-4o-mini", "llama-groq"}, msgs, 0.1, 100)
		if r.Success || !r.Degraded {
			t.Errorf("expected degraded failure: %+v", r)
		}
	})
}
