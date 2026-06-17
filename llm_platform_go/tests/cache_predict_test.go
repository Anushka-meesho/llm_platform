package tests

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"llm_platform_go/internal/cache"
	appdb "llm_platform_go/internal/db"
	"llm_platform_go/internal/llm"
)

// recordingCache is an in-memory Cache that also records the TTL of every Set,
// so tests can assert the cache-fill TTL without depending on wall-clock expiry.
type recordingCache struct {
	mu   sync.Mutex
	data map[string][]byte
	ttls []time.Duration
}

func newRecordingCache() *recordingCache { return &recordingCache{data: map[string][]byte{}} }

func (c *recordingCache) Get(_ context.Context, key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.data[key]
	return v, ok
}

func (c *recordingCache) Set(_ context.Context, key string, val []byte, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[key] = val
	c.ttls = append(c.ttls, ttl)
}

// hasTTL reports whether any Set used exactly the given TTL.
func (c *recordingCache) hasTTL(d time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, t := range c.ttls {
		if t == d {
			return true
		}
	}
	return false
}

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
	clients := &llm.Clients{Meesho: llm.NewOpenAICompatProvider(fake.URL, "test-key")}
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
	clients := &llm.Clients{Meesho: llm.NewOpenAICompatProvider(fake.URL, "test-key")}
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

// TestPredictCachesFallbackAnswer is the reported bug: when the primary model
// fails (here a 401) and a fallback serves, the answer must be cached under the
// serving model and replayed on the next identical request — no second provider
// call — while still reporting fallback_used.
func TestPredictCachesFallbackAnswer(t *testing.T) {
	primaryFail, primaryCalls := countingModelServer(t, "", http.StatusUnauthorized)
	fallbackOK, fallbackCalls := countingModelServer(t, `{"label":"positive"}`, http.StatusOK)
	clients := &llm.Clients{
		OpenAI: llm.NewOpenAICompatProvider(primaryFail.URL, ""),   // primary gpt-4o-mini → 401
		Groq:   llm.NewOpenAICompatProvider(fallbackOK.URL, "k"),   // fallback llama-groq → ok
	}
	srv, _ := newTestServerWithCache(t, clients, cache.NewMemory())

	taskJSON := `{
		"id": "cached-sentiment", "name": "Cached Sentiment",
		"model": "gpt-4o-mini", "fallback_models": ["llama-groq"],
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
		t.Fatalf("create task: got %d, want 201", resp.StatusCode)
	}

	body := `{"inputs":{"text":"I love it"}}`

	// First predict: primary 401 → fallback serves and is cached.
	_, first := doPredict(t, srv, body)
	if first.Cached {
		t.Fatal("first call must not be a cache hit")
	}
	if first.Model != "llama-groq" {
		t.Fatalf("expected fallback to serve, got model %q", first.Model)
	}
	if fallbackCalls.Load() != 1 {
		t.Fatalf("fallback provider calls after first predict: got %d, want 1", fallbackCalls.Load())
	}

	// Second identical predict: cache hit on the fallback's entry, no new call.
	_, second := doPredict(t, srv, body)
	if !second.Cached {
		t.Fatal("a fallback answer must be cached and replayed on the next identical request")
	}
	if second.Model != "llama-groq" {
		t.Fatalf("cache hit should report the serving (fallback) model, got %q", second.Model)
	}
	if fallbackCalls.Load() != 1 {
		t.Fatalf("cache hit must not call the provider again: got %d fallback calls", fallbackCalls.Load())
	}
	if second.Usage.CostUSD != 0 || second.Usage.TotalTokens != 0 {
		t.Fatalf("cache hit must be zero-cost, got %+v", second.Usage)
	}
	_ = primaryCalls // primary is exercised live each time (cheap 401); not asserted
}

// TestConcurrentPredictAndChainEdit hammers the routing config from both sides:
// many predictions (config reads via Store.Get) racing with chain edits (PUTs →
// Store.Update). It proves the editMu coordination is deadlock- and race-free
// (run with -race) and that an edit is consistent — every read sees a whole
// chain, never a half-applied one. The model server accepts both routing keys
// the edits toggle between, so a degraded outcome never fails a request.
func TestConcurrentPredictAndChainEdit(t *testing.T) {
	ok, _ := countingModelServer(t, `{"label":"positive"}`, http.StatusOK)
	clients := &llm.Clients{
		Meesho: llm.NewOpenAICompatProvider(ok.URL, "k"), // gpt-4o-mini and gemini-2.5-flash via bifrost
	}
	srv, _ := newTestServerWithCache(t, clients, cache.NewMemory())

	create := `{
		"id":"cached-sentiment","name":"Cached Sentiment",
		"model":"gpt-4o-mini","fallback_models":["gemini-2.5-flash"],"cache_enabled": true,
		"prompt_template":"Classify sentiment: {{.text}}",
		"input_schema":{"type":"object","required":["text"],"properties":{"text":{"type":"string"}}},
		"output_schema":{"type":"object","required":["label"],"properties":{"label":{"type":"string"}}}
	}`
	resp, _ := http.DefaultClient.Do(authReq(t, http.MethodPost, srv.URL+"/v1/tasks", create))
	resp.Body.Close()

	var wg sync.WaitGroup
	var bad atomic.Int64

	// Readers: vary inputs so they aren't all served from cache.
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			body := fmt.Sprintf(`{"inputs":{"text":"sample %d"}}`, n)
			status, _ := doPredict(t, srv, body)
			if status != http.StatusOK {
				bad.Add(1)
			}
		}(i)
	}
	// Writers: flip the fallback chain repeatedly.
	chains := []string{`{"fallback_models":["gemini-2.5-flash"]}`, `{"fallback_models":[]}`}
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			r, err := http.DefaultClient.Do(authReq(t, http.MethodPut,
				srv.URL+"/v1/tasks/cached-sentiment", chains[n%2]))
			if err != nil || r.StatusCode != http.StatusOK {
				bad.Add(1)
				return
			}
			r.Body.Close()
		}(i)
	}
	wg.Wait()

	if bad.Load() != 0 {
		t.Fatalf("concurrent predicts/edits produced %d non-OK responses", bad.Load())
	}
}

// TestCacheModelEntryTTL verifies that one clean predict writes exactly one
// entry — keyed on the serving model, at the task's TTL (DefaultTTL here). The
// chain is never cached.
func TestCacheModelEntryTTL(t *testing.T) {
	rec := newRecordingCache()
	ok, _ := countingModelServer(t, `{"label":"positive"}`, http.StatusOK)
	clients := &llm.Clients{Meesho: llm.NewOpenAICompatProvider(ok.URL, "k")}
	srv, _ := newTestServerWithCache(t, clients, rec)

	taskJSON := `{
		"id":"cached-sentiment","name":"Cached Sentiment",
		"model":"gpt-4o-mini","fallback_models":["llama-groq"],"cache_enabled": true,
		"prompt_template":"Classify sentiment: {{.text}}",
		"input_schema":{"type":"object","required":["text"],"properties":{"text":{"type":"string"}}},
		"output_schema":{"type":"object","required":["label"],"properties":{"label":{"type":"string"}}}
	}`
	resp, err := http.DefaultClient.Do(authReq(t, http.MethodPost, srv.URL+"/v1/tasks", taskJSON))
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create task: got %d, want 201", resp.StatusCode)
	}

	doPredict(t, srv, `{"inputs":{"text":"I love it"}}`)

	if !rec.hasTTL(cache.DefaultTTL) {
		t.Errorf("expected a per-model entry with DefaultTTL %v, got %v", cache.DefaultTTL, rec.ttls)
	}
	if len(rec.ttls) != 1 {
		t.Errorf("expected exactly one cache entry (per-model only), got %d: %v", len(rec.ttls), rec.ttls)
	}
}

// TestPerModelCacheSurvivesChainEdit verifies the per-model cache: an identical
// request replays the cached model answer, and editing the chain still hits the
// surviving model's entry (the chain itself is never part of the cache key).
func TestPerModelCacheSurvivesChainEdit(t *testing.T) {
	rec := newRecordingCache()
	ok, calls := countingModelServer(t, `{"label":"positive"}`, http.StatusOK)
	// Both gpt-4o-mini and gemini-2.5-flash route through bifrost (meeshoC).
	clients := &llm.Clients{
		Meesho: llm.NewOpenAICompatProvider(ok.URL, "k"),
	}
	srv, _ := newTestServerWithCache(t, clients, rec)

	create := `{
		"id":"cached-sentiment","name":"Cached Sentiment",
		"model":"gpt-4o-mini","fallback_models":["gemini-2.5-flash"],"cache_enabled": true,
		"prompt_template":"Classify sentiment: {{.text}}",
		"input_schema":{"type":"object","required":["text"],"properties":{"text":{"type":"string"}}},
		"output_schema":{"type":"object","required":["label"],"properties":{"label":{"type":"string"}}}
	}`
	resp, _ := http.DefaultClient.Do(authReq(t, http.MethodPost, srv.URL+"/v1/tasks", create))
	resp.Body.Close()
	body := `{"inputs":{"text":"I love it"}}`

	// First predict: primary serves, writes the per-model entry for gpt-4o-mini.
	doPredict(t, srv, body)
	if calls.Load() != 1 {
		t.Fatalf("first predict: got %d provider calls, want 1", calls.Load())
	}

	// Identical request → the walk reaches the primary, hits its per-model entry,
	// no provider call.
	_, second := doPredict(t, srv, body)
	if !second.Cached || calls.Load() != 1 {
		t.Fatalf("identical request must hit the per-model cache: cached=%v calls=%d", second.Cached, calls.Load())
	}

	// Edit the chain (drop the fallback). The primary's per-model entry is keyed
	// only on the model, not the chain — so it still hits.
	put := `{"fallback_models":[]}`
	r2, _ := http.DefaultClient.Do(authReq(t, http.MethodPut, srv.URL+"/v1/tasks/cached-sentiment", put))
	r2.Body.Close()
	_, third := doPredict(t, srv, body)
	if !third.Cached || calls.Load() != 1 {
		t.Fatalf("a chain edit must not drop the per-model cache: cached=%v calls=%d", third.Cached, calls.Load())
	}
}

// TestRecoveredPrimaryNotShadowedByFallbackCache is the routing-change bug: a
// fallback's long-lived per-model cache entry must NOT be served in place of a
// recovered primary. The per-model cache is consulted only as the walk reaches
// each model, so when the primary is healthy again it gets called live — the
// stale fallback answer is never reached.
func TestRecoveredPrimaryNotShadowedByFallbackCache(t *testing.T) {
	// Primary flips from down (401) to healthy via an atomic flag.
	var primaryHealthy atomic.Bool
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !primaryHealthy.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"down"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": `{"label":"primary-recovered"}`}}},
			"usage":   map[string]any{"prompt_tokens": 1, "completion_tokens": 1},
		})
	}))
	t.Cleanup(primary.Close)
	fallback, fallbackCalls := countingModelServer(t, `{"label":"fallback"}`, http.StatusOK)

	clients := &llm.Clients{
		Meesho: llm.NewOpenAICompatProvider(primary.URL, ""),   // gpt-4o-mini and gemini-2.5-flash via bifrost
		Groq:   llm.NewOpenAICompatProvider(fallback.URL, "k"), // llama-groq → fallback
	}
	srv, _ := newTestServerWithCache(t, clients, cache.NewMemory())

	create := `{
		"id":"cached-sentiment","name":"Cached Sentiment",
		"model":"gpt-4o-mini","fallback_models":["llama-groq"],"cache_enabled": true,
		"prompt_template":"Classify sentiment: {{.text}}",
		"input_schema":{"type":"object","required":["text"],"properties":{"text":{"type":"string"}}},
		"output_schema":{"type":"object","required":["label"],"properties":{"label":{"type":"string"}}}
	}`
	resp, _ := http.DefaultClient.Do(authReq(t, http.MethodPost, srv.URL+"/v1/tasks", create))
	resp.Body.Close()
	body := `{"inputs":{"text":"I love it"}}`

	// 1. Primary down → fallback serves and is cached under llama-groq (long TTL).
	_, first := doPredict(t, srv, body)
	if first.Model != "llama-groq" {
		t.Fatalf("with primary down, fallback should serve, got %q", first.Model)
	}
	if fallbackCalls.Load() != 1 {
		t.Fatalf("fallback calls after first predict: got %d, want 1", fallbackCalls.Load())
	}

	// 2. Primary recovers, and the chain structure changes (add a model) so the
	//    chain-level entry misses — the path that used to replay the stale
	//    per-model fallback answer.
	primaryHealthy.Store(true)
	put := `{"fallback_models":["llama-groq","gemini-2.5-flash"]}`
	r2, _ := http.DefaultClient.Do(authReq(t, http.MethodPut, srv.URL+"/v1/tasks/cached-sentiment", put))
	r2.Body.Close()

	// 3. The recovered primary must be called and serve — not the cached fallback.
	_, third := doPredict(t, srv, body)
	if third.Cached {
		t.Fatal("must not replay the stale fallback cache when the primary has recovered")
	}
	if third.Model != "gpt-4o-mini" {
		t.Fatalf("recovered primary should serve, got %q", third.Model)
	}
	if string(third.Output) != `{"label":"primary-recovered"}` {
		t.Fatalf("expected the recovered primary's answer, got %s", third.Output)
	}
	if fallbackCalls.Load() != 1 {
		t.Fatalf("fallback must not be called again: got %d calls", fallbackCalls.Load())
	}
}
