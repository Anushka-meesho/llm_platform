package tests

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"llm_platform_go/internal/api"
	appdb "llm_platform_go/internal/db"
	"llm_platform_go/internal/llm"
	"llm_platform_go/internal/ratelimit"
	"llm_platform_go/internal/tasks"
	"llm_platform_go/internal/users"
)

// newLimiterServer wires a predict-capable test server whose gpt-4o-mini (+ an
// optional fallback) route through `clients`, with the given rate limiter active
// and the supplied task registered. Mirrors newTestServerWithCache but injects a
// Limiter — the dependency this suite exercises.
func newLimiterServer(t *testing.T, clients *llm.Clients, limiter *ratelimit.Limiter, taskJSON string) *httptest.Server {
	t.Helper()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	database.SetMaxOpenConns(1)
	if err := appdb.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	_ = llm.LoadPricingFromMap(map[string]llm.Rate{
		"gpt-4o-mini":      {InputPer1M: 0.15, OutputPer1M: 0.60},
		"gemini-2.5-flash": {InputPer1M: 0.15, OutputPer1M: 0.60},
	})
	taskStore := tasks.NewStore(database)
	if err := tasks.SeedPlayground(taskStore); err != nil {
		t.Fatalf("seed playground: %v", err)
	}

	router := api.NewRouter(api.RouterDeps{
		DB:      database,
		Clients: clients,
		Users:   users.NewDemoStore(),
		Tasks:   taskStore,
		Limiter: limiter,
		Auth: api.AuthConfig{
			Secret:      []byte(testSecret),
			CookieName:  "llm_platform_token",
			Issuer:      testIssuer,
			TokenExpiry: time.Hour,
		},
	})
	srv := httptest.NewServer(router)
	t.Cleanup(func() {
		srv.Close()
		database.Close()
	})

	resp, err := http.DefaultClient.Do(authReq(t, http.MethodPost, srv.URL+"/v1/tasks", taskJSON))
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create task status: got %d, want 201", resp.StatusCode)
	}
	return srv
}

const sentimentTaskJSON = `{
	"id": "sentiment",
	"name": "Sentiment",
	"model": "gpt-4o-mini",
	"prompt_template": "Classify sentiment: {{.text}}",
	"input_schema": {"type":"object","required":["text"],"properties":{"text":{"type":"string"}}},
	"output_schema": {"type":"object","required":["label"],"properties":{"label":{"type":"string"}}}
}`

func predictReq(t *testing.T, srv *httptest.Server, text string) *http.Response {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"inputs": map[string]string{"text": text}})
	resp, err := http.DefaultClient.Do(authReq(t, http.MethodPost, srv.URL+"/v1/tasks/sentiment/predict", string(body)))
	if err != nil {
		t.Fatalf("predict: %v", err)
	}
	return resp
}

func decodeErrCode(t *testing.T, resp *http.Response) string {
	t.Helper()
	var body struct {
		Code      string `json:"code"`
		ErrorCode string `json:"error_code"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	if body.Code != "" {
		return body.Code
	}
	return body.ErrorCode
}

// TestRateLimitInputTooLarge: a request whose estimated input exceeds the
// per-request cap is rejected (413) before the model is ever called.
func TestRateLimitInputTooLarge(t *testing.T) {
	fake, calls := countingModelServer(t, `{"label":"positive"}`, http.StatusOK)
	clients := &llm.Clients{Meesho: llm.NewOpenAICompatProvider(fake.URL, "k")}
	limiter := ratelimit.New(ratelimit.Config{
		Enabled: true, Window: time.Minute, MaxInputTokens: 5, CharsPerToken: 4,
	})
	srv := newLimiterServer(t, clients, limiter, sentimentTaskJSON)

	resp := predictReq(t, srv, "this input is clearly far longer than five tokens worth of text")
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status: got %d, want 413", resp.StatusCode)
	}
	if code := decodeErrCode(t, resp); code != "input_too_large" {
		t.Errorf("code: got %q, want input_too_large", code)
	}
	if calls.Load() != 0 {
		t.Errorf("an over-cap input must be rejected before any model call, got %d calls", calls.Load())
	}
}

// TestRateLimitRequestRate: the per-window request count caps accepted requests;
// the next one gets 429 with a Retry-After hint.
func TestRateLimitRequestRate(t *testing.T) {
	fake, _ := countingModelServer(t, `{"label":"positive"}`, http.StatusOK)
	clients := &llm.Clients{Meesho: llm.NewOpenAICompatProvider(fake.URL, "k")}
	limiter := ratelimit.New(ratelimit.Config{
		Enabled: true, Window: time.Minute, MaxRequests: 1, CharsPerToken: 4,
	})
	srv := newLimiterServer(t, clients, limiter, sentimentTaskJSON)

	if resp := predictReq(t, srv, "I love it"); resp.StatusCode != http.StatusOK {
		t.Fatalf("first request: got %d, want 200", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
	resp := predictReq(t, srv, "I love it")
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second request: got %d, want 429", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Error("a 429 from the request-rate cap should carry Retry-After")
	}
	if code := decodeErrCode(t, resp); code != "request_rate_exceeded" {
		t.Errorf("code: got %q, want request_rate_exceeded", code)
	}
}

// TestRateLimitCountsFailedTokens is the core requirement: tokens consumed by a
// request are counted even when it ultimately FAILS. Here both the primary and
// the fallback return schema-invalid output — the request fails (502), but each
// attempt still consumed tokens (42+10 each = 104 total). With the window budget
// just under that, the very next request is rejected on the token budget,
// proving the failed attempts' tokens were counted.
func TestRateLimitCountsFailedTokens(t *testing.T) {
	// Both models route through one server returning schema-invalid JSON.
	fake, calls := countingModelServer(t, `{"wrong_key":"x"}`, http.StatusOK)
	clients := &llm.Clients{Meesho: llm.NewOpenAICompatProvider(fake.URL, "k")}
	limiter := ratelimit.New(ratelimit.Config{
		Enabled: true, Window: time.Minute,
		MaxTokens:     80, // < the 104 tokens the failed chain consumes
		CharsPerToken: 4,
	})
	taskJSON := `{
		"id": "sentiment",
		"name": "Sentiment",
		"model": "gpt-4o-mini",
		"fallback_models": ["gemini-2.5-flash"],
		"prompt_template": "Classify sentiment: {{.text}}",
		"input_schema": {"type":"object","required":["text"],"properties":{"text":{"type":"string"}}},
		"output_schema": {"type":"object","required":["label"],"properties":{"label":{"type":"string"}}}
	}`
	srv := newLimiterServer(t, clients, limiter, taskJSON)

	// First request: accepted (small estimate), runs both models, both fail
	// validation → 502, but consumes ~104 tokens.
	first := predictReq(t, srv, "meh")
	if first.StatusCode != http.StatusBadGateway {
		t.Fatalf("first request should fail validation on every model (502), got %d", first.StatusCode)
	}
	first.Body.Close()
	if calls.Load() != 2 {
		t.Fatalf("both primary and fallback should have been called, got %d", calls.Load())
	}

	// Second request: rejected on the token budget because the FAILED request's
	// tokens were counted (104 used ≥ 80 budget).
	second := predictReq(t, srv, "meh")
	if second.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second request should be rejected on the token budget (failed tokens counted), got %d", second.StatusCode)
	}
	if code := decodeErrCode(t, second); code != "token_budget_exhausted" {
		t.Errorf("code: got %q, want token_budget_exhausted", code)
	}
	// The model must NOT have been called for the rejected request.
	if calls.Load() != 2 {
		t.Errorf("rejected request must not reach the model; calls=%d (want 2)", calls.Load())
	}
}

// TestRateLimitDisabledByDefaultInTests: with no limiter wired, predicts are
// never gated (existing behavior is preserved for the rest of the suite).
func TestRateLimitNilLimiterAllows(t *testing.T) {
	fake, _ := countingModelServer(t, `{"label":"positive"}`, http.StatusOK)
	clients := &llm.Clients{Meesho: llm.NewOpenAICompatProvider(fake.URL, "k")}
	srv := newLimiterServer(t, clients, nil, sentimentTaskJSON) // nil limiter
	for i := 0; i < 3; i++ {
		resp := predictReq(t, srv, "I love it")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d with no limiter: got %d, want 200", i, resp.StatusCode)
		}
		resp.Body.Close()
	}
}
