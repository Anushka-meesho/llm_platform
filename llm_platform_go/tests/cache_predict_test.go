package tests

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"llm_platform_go/internal/cache"
	appdb "llm_platform_go/internal/db"
	"llm_platform_go/internal/llm"
)

// countingModelServer is a fake OpenAI-compatible endpoint that counts how
// many completions it served — the ground truth for "did the cache prevent a
// provider call".
func countingModelServer(t *testing.T, content string, status int) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": content}},
			},
			"usage": map[string]any{"prompt_tokens": 42, "completion_tokens": 10},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// newCacheServer wires a server with a memory prediction cache and one
// cache-enabled task backed by the counting fake model.
func newCacheServer(t *testing.T, modelOutput string, modelStatus int) (*httptest.Server, *sql.DB, *atomic.Int64) {
	t.Helper()
	fake, calls := countingModelServer(t, modelOutput, modelStatus)
	clients := &llm.Clients{OpenAI: llm.NewOpenAICompatProvider(fake.URL, "test-key")}
	srv, database := newTestServerWithCache(t, clients, cache.NewMemory())

	taskJSON := `{
		"id": "cached-sentiment",
		"name": "Cached Sentiment",
		"model": "gpt-4o-mini",
		"cache_enabled": true,
		"prompt_template": "Classify sentiment: {{.text}}",
		"input_schema": {"type":"object","required":["text"],"properties":{"text":{"type":"string"}}},
		"output_schema": {"type":"object","required":["label"],"properties":{"label":{"type":"string"}}}
	}`
	resp, err := http.DefaultClient.Do(authReq(t, http.MethodPost, srv.URL+"/v1/tasks", taskJSON))
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create task status: got %d, want 201", resp.StatusCode)
	}
	return srv, database, calls
}

type cachePredictOut struct {
	TaskRunID   string          `json:"task_run_id"`
	Model       string          `json:"model"`
	Provider    string          `json:"provider"`
	Output      json.RawMessage `json:"output"`
	OutputValid *bool           `json:"output_valid"`
	Cached      bool            `json:"cached"`
	Usage       struct {
		TotalTokens int     `json:"total_tokens"`
		CostUSD     float64 `json:"cost_usd"`
	} `json:"usage"`
}

func doPredict(t *testing.T, srv *httptest.Server, body string) (int, cachePredictOut) {
	t.Helper()
	resp, err := http.DefaultClient.Do(authReq(t, http.MethodPost,
		srv.URL+"/v1/tasks/cached-sentiment/predict", body))
	if err != nil {
		t.Fatalf("predict: %v", err)
	}
	defer resp.Body.Close()
	var out cachePredictOut
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode predict response: %v", err)
	}
	return resp.StatusCode, out
}

func TestPredictCacheHit(t *testing.T) {
	srv, database, calls := newCacheServer(t, `{"label":"positive"}`, http.StatusOK)
	body := `{"inputs":{"text":"I love it"}}`

	// First call: miss — provider is called, normal cost.
	status, first := doPredict(t, srv, body)
	if status != http.StatusOK {
		t.Fatalf("first predict: got %d, want 200", status)
	}
	if first.Cached {
		t.Fatal("first call must not be served from cache")
	}
	if calls.Load() != 1 {
		t.Fatalf("provider calls after first predict: got %d, want 1", calls.Load())
	}
	spendAfterFirst, err := appdb.TaskSpendToday(database, "cached-sentiment")
	if err != nil {
		t.Fatalf("spend query: %v", err)
	}

	// Second identical call: hit — no provider call, same output, zero cost.
	status, second := doPredict(t, srv, body)
	if status != http.StatusOK {
		t.Fatalf("second predict: got %d, want 200", status)
	}
	if !second.Cached {
		t.Fatal("identical second call must be served from cache")
	}
	if calls.Load() != 1 {
		t.Fatalf("provider calls after cached predict: got %d, want still 1", calls.Load())
	}
	if string(second.Output) != string(first.Output) {
		t.Fatalf("cached output mismatch: %s vs %s", second.Output, first.Output)
	}
	if second.OutputValid == nil || !*second.OutputValid {
		t.Fatal("cached hit must preserve output_valid=true")
	}
	if second.Usage.CostUSD != 0 || second.Usage.TotalTokens != 0 {
		t.Fatalf("cached hit must report zero usage, got %+v", second.Usage)
	}

	// Money path: a cache hit adds no spend (budget gate must not see it).
	spendAfterHit, err := appdb.TaskSpendToday(database, "cached-sentiment")
	if err != nil {
		t.Fatalf("spend query: %v", err)
	}
	if spendAfterHit != spendAfterFirst {
		t.Fatalf("cache hit changed task spend: %v -> %v", spendAfterFirst, spendAfterHit)
	}

	// Observability: the hit is a run row flagged cache_hit=1 with zero cost.
	var cacheHit int
	var cost float64
	err = database.QueryRow(
		`SELECT cache_hit, cost_usd FROM runs WHERE run_id = ?`, second.TaskRunID).
		Scan(&cacheHit, &cost)
	if err != nil {
		t.Fatalf("run row for cached hit: %v", err)
	}
	if cacheHit != 1 || cost != 0 {
		t.Fatalf("cached run row: cache_hit=%d cost=%v, want 1 and 0", cacheHit, cost)
	}
}

func TestPredictCacheMissOnDifferentInputs(t *testing.T) {
	srv, _, calls := newCacheServer(t, `{"label":"positive"}`, http.StatusOK)

	doPredict(t, srv, `{"inputs":{"text":"I love it"}}`)
	doPredict(t, srv, `{"inputs":{"text":"I hate it"}}`)
	if calls.Load() != 2 {
		t.Fatalf("different inputs must both reach the provider: got %d calls, want 2", calls.Load())
	}
}

func TestPredictCacheInvalidatedByDeploy(t *testing.T) {
	srv, _, calls := newCacheServer(t, `{"label":"positive"}`, http.StatusOK)
	body := `{"inputs":{"text":"I love it"}}`

	doPredict(t, srv, body)
	if calls.Load() != 1 {
		t.Fatalf("first predict: got %d provider calls, want 1", calls.Load())
	}

	// Save a draft with the SAME template and deploy it. The rendered prompt
	// is byte-identical, but the deployed version changed — the key must too.
	draft := `{"prompt_template":"Classify sentiment: {{.text}}","system_prompt":"","note":"same content, new version"}`
	resp, err := http.DefaultClient.Do(authReq(t, http.MethodPost,
		srv.URL+"/v1/tasks/cached-sentiment/versions", draft))
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("save draft: err=%v status=%v", err, resp.StatusCode)
	}
	var saved struct {
		Version int `json:"version"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&saved)
	resp.Body.Close()

	deploy := fmt.Sprintf(`{"version":%d}`, saved.Version)
	resp, err = http.DefaultClient.Do(authReq(t, http.MethodPost,
		srv.URL+"/v1/tasks/cached-sentiment/deploy", deploy))
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("deploy: err=%v status=%v", err, resp.StatusCode)
	}
	resp.Body.Close()

	if _, out := doPredict(t, srv, body); out.Cached {
		t.Fatal("a deploy must invalidate cached predictions")
	}
	if calls.Load() != 2 {
		t.Fatalf("post-deploy predict must reach the provider: got %d calls, want 2", calls.Load())
	}
}

func TestPredictCacheRequiresOptIn(t *testing.T) {
	fake, calls := countingModelServer(t, `{"label":"positive"}`, http.StatusOK)
	clients := &llm.Clients{OpenAI: llm.NewOpenAICompatProvider(fake.URL, "test-key")}
	srv, _ := newTestServerWithCache(t, clients, cache.NewMemory())

	// Task without cache_enabled — caching must never engage.
	taskJSON := `{
		"id": "uncached-task",
		"name": "Uncached",
		"model": "gpt-4o-mini",
		"prompt_template": "Echo: {{.text}}",
		"input_schema": {"type":"object","required":["text"],"properties":{"text":{"type":"string"}}}
	}`
	resp, err := http.DefaultClient.Do(authReq(t, http.MethodPost, srv.URL+"/v1/tasks", taskJSON))
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("create task: err=%v status=%v", err, resp.StatusCode)
	}

	for i := 0; i < 2; i++ {
		r, err := http.DefaultClient.Do(authReq(t, http.MethodPost,
			srv.URL+"/v1/tasks/uncached-task/predict", `{"inputs":{"text":"same"}}`))
		if err != nil || r.StatusCode != http.StatusOK {
			t.Fatalf("predict %d: err=%v status=%v", i, err, r.StatusCode)
		}
		r.Body.Close()
	}
	if calls.Load() != 2 {
		t.Fatalf("cache-disabled task must always call the provider: got %d, want 2", calls.Load())
	}
}

func TestStudioTestBypassesCache(t *testing.T) {
	srv, _, calls := newCacheServer(t, `{"label":"positive"}`, http.StatusOK)

	// Warm the cache through the production endpoint.
	doPredict(t, srv, `{"inputs":{"text":"I love it"}}`)
	if calls.Load() != 1 {
		t.Fatalf("warm-up: got %d provider calls, want 1", calls.Load())
	}

	// The Studio test panel must always run fresh — same inputs, two calls,
	// and neither is served from (or stored into) the cache.
	for i := 0; i < 2; i++ {
		resp, err := http.DefaultClient.Do(authReq(t, http.MethodPost,
			srv.URL+"/v1/tasks/cached-sentiment/test", `{"inputs":{"text":"I love it"}}`))
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("test call %d: err=%v status=%v", i, err, resp.StatusCode)
		}
		resp.Body.Close()
	}
	if calls.Load() != 3 {
		t.Fatalf("test calls must bypass the cache: got %d provider calls, want 3", calls.Load())
	}
}

func TestPredictFailureNotCached(t *testing.T) {
	// 400 from the provider = non-retryable config error → prediction fails.
	srv, _, calls := newCacheServer(t, "", http.StatusBadRequest)
	body := `{"inputs":{"text":"I love it"}}`

	for i := 0; i < 2; i++ {
		resp, err := http.DefaultClient.Do(authReq(t, http.MethodPost,
			srv.URL+"/v1/tasks/cached-sentiment/predict", body))
		if err != nil {
			t.Fatalf("predict %d: %v", i, err)
		}
		if resp.StatusCode != http.StatusBadGateway {
			t.Fatalf("predict %d status: got %d, want 502", i, resp.StatusCode)
		}
		resp.Body.Close()
	}
	if calls.Load() != 2 {
		t.Fatalf("failures must never be cached: got %d provider calls, want 2", calls.Load())
	}
}

func TestPredictInvalidOutputNotCached(t *testing.T) {
	// Model output violates the output schema → output_valid=false → no cache.
	srv, _, calls := newCacheServer(t, `{"wrong_field": true}`, http.StatusOK)
	body := `{"inputs":{"text":"I love it"}}`

	for i := 0; i < 2; i++ {
		status, out := doPredict(t, srv, body)
		if status != http.StatusOK {
			t.Fatalf("predict %d status: got %d, want 200 (invalid output is flagged, not failed)", i, status)
		}
		if out.Cached {
			t.Fatalf("predict %d: schema-invalid output must not be served from cache", i)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("schema-invalid outputs must not be cached: got %d provider calls, want 2", calls.Load())
	}
}
