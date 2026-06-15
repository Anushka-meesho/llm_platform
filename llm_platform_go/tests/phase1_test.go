package tests

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	appdb "llm_platform_go/internal/db"
	"llm_platform_go/internal/llm"
	"llm_platform_go/internal/types"

	_ "modernc.org/sqlite"
)

// ── Budget enforcement ───────────────────────────────────────────────────────

func TestBudgetEnforcement(t *testing.T) {
	llm.ResetBreakers()
	t.Cleanup(llm.ResetBreakers)
	srv, database := newPredictServer(t, `{"label":"positive"}`)

	// Give the sentiment task a tiny budget.
	body := `{"name":"Sentiment","model":"gpt-4o-mini",
		"prompt_template":"Classify sentiment: {{.text}}",
		"input_schema": {"type":"object","required":["text"],"properties":{"text":{"type":"string"}}},
		"daily_budget_usd": 0.0001}`
	resp, err := http.DefaultClient.Do(authReq(t, http.MethodPut, srv.URL+"/v1/tasks/sentiment", body))
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("update task: %v status=%d", err, resp.StatusCode)
	}

	// Seed today's spend beyond the budget.
	taskID := "sentiment"
	row := &types.RunRow{
		RunID: "seed-spend", Prompt: "p", Model: "gpt-4o-mini",
		CostUSD: 0.5, Success: true,
		UserID: strPtr("u-admin"), TaskID: &taskID,
		CreatedAt: time.Now().UTC(),
	}
	if err := appdb.InsertRun(database, row); err != nil {
		t.Fatal(err)
	}

	// Next predict must be rejected with 429 + Retry-After.
	resp, err = http.DefaultClient.Do(authReq(t, http.MethodPost,
		srv.URL+"/v1/tasks/sentiment/predict", `{"inputs":{"text":"hi"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status: got %d, want 429", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Error("Retry-After header missing")
	}

	// Budget 0 = exempt: clear the budget, predict succeeds.
	body = strings.Replace(body, `"daily_budget_usd": 0.0001`, `"daily_budget_usd": 0`, 1)
	_, _ = http.DefaultClient.Do(authReq(t, http.MethodPut, srv.URL+"/v1/tasks/sentiment", body))
	resp, _ = http.DefaultClient.Do(authReq(t, http.MethodPost,
		srv.URL+"/v1/tasks/sentiment/predict", `{"inputs":{"text":"hi"}}`))
	if resp.StatusCode != http.StatusOK {
		t.Errorf("budget-exempt predict: got %d, want 200", resp.StatusCode)
	}

	if _, err := appdb.TaskSpendToday(database, "sentiment"); err != nil {
		t.Errorf("TaskSpendToday: %v", err)
	}
}

// The budget gate reads an in-memory spend view (refreshed from the DB at most
// every few seconds) with each prediction's cost folded in locally. A predict
// that crosses the cap must therefore block the very next request — no DB
// round-trip or refresh window in between.
func TestBudgetIncrementsWithoutDBRefresh(t *testing.T) {
	llm.ResetBreakers()
	t.Cleanup(llm.ResetBreakers)
	srv, _ := newPredictServer(t, `{"label":"positive"}`)

	// Budget below one fake-model call's cost (42 in + 10 out tokens of
	// gpt-4o-mini ≈ $0.0000123), so the first success exhausts it.
	body := `{"name":"Sentiment","model":"gpt-4o-mini",
		"prompt_template":"Classify sentiment: {{.text}}",
		"input_schema": {"type":"object","required":["text"],"properties":{"text":{"type":"string"}}},
		"daily_budget_usd": 0.00001}`
	resp, err := http.DefaultClient.Do(authReq(t, http.MethodPut, srv.URL+"/v1/tasks/sentiment", body))
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("update task: %v status=%d", err, resp.StatusCode)
	}

	resp, err = http.DefaultClient.Do(authReq(t, http.MethodPost,
		srv.URL+"/v1/tasks/sentiment/predict", `{"inputs":{"text":"hi"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first predict (spend 0 < budget): got %d, want 200", resp.StatusCode)
	}

	resp, err = http.DefaultClient.Do(authReq(t, http.MethodPost,
		srv.URL+"/v1/tasks/sentiment/predict", `{"inputs":{"text":"hi again"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second predict must hit the locally incremented cap: got %d, want 429", resp.StatusCode)
	}
}

// ── Prompt versions: draft → test → deploy ───────────────────────────────────

func TestPromptVersionLifecycle(t *testing.T) {
	llm.ResetBreakers()
	t.Cleanup(llm.ResetBreakers)
	srv, database := newPredictServer(t, `{"label":"positive"}`)

	// Creating the task recorded version 1.
	resp, _ := http.DefaultClient.Do(authReq(t, http.MethodGet, srv.URL+"/v1/tasks/sentiment/versions", ""))
	var hist struct {
		ActiveVersion int `json:"active_version"`
		Versions      []struct {
			Version int  `json:"version"`
			Active  bool `json:"active"`
		} `json:"versions"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&hist)
	if hist.ActiveVersion != 1 || len(hist.Versions) != 1 || !hist.Versions[0].Active {
		t.Fatalf("initial history wrong: %+v", hist)
	}

	// Save a draft — does NOT activate.
	draft := `{"prompt_template":"V2 classify: {{.text}}","note":"trying v2"}`
	resp, _ = http.DefaultClient.Do(authReq(t, http.MethodPost, srv.URL+"/v1/tasks/sentiment/versions", draft))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("draft status: %d", resp.StatusCode)
	}
	var saved struct {
		Version int `json:"version"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&saved)
	if saved.Version != 2 {
		t.Fatalf("draft version: got %d, want 2", saved.Version)
	}

	// Predict still uses version 1.
	resp, _ = http.DefaultClient.Do(authReq(t, http.MethodPost,
		srv.URL+"/v1/tasks/sentiment/predict", `{"inputs":{"text":"hi"}}`))
	var pred struct {
		PromptVersion int `json:"prompt_version"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&pred)
	if pred.PromptVersion != 1 {
		t.Errorf("predict before deploy: version %d, want 1", pred.PromptVersion)
	}

	// Test the draft (overrides version, flagged is_test).
	resp, _ = http.DefaultClient.Do(authReq(t, http.MethodPost,
		srv.URL+"/v1/tasks/sentiment/test", `{"inputs":{"text":"hi"},"version":2}`))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("test endpoint status: %d", resp.StatusCode)
	}
	var testResp struct {
		PromptVersion int `json:"prompt_version"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&testResp)
	if testResp.PromptVersion != 2 {
		t.Errorf("test used version %d, want 2", testResp.PromptVersion)
	}
	var isTest int
	var testedPrompt string
	_ = database.QueryRow(
		"SELECT is_test, prompt FROM runs WHERE task_id='sentiment' ORDER BY id DESC LIMIT 1",
	).Scan(&isTest, &testedPrompt)
	if isTest != 1 {
		t.Error("test run not flagged is_test")
	}
	if !strings.HasPrefix(testedPrompt, "V2 classify:") {
		t.Errorf("test did not render the draft template: %q", testedPrompt)
	}

	// Deploy v2 → predict now stamps version 2 with the new template.
	resp, _ = http.DefaultClient.Do(authReq(t, http.MethodPost,
		srv.URL+"/v1/tasks/sentiment/deploy", `{"version":2}`))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("deploy status: %d", resp.StatusCode)
	}
	resp, _ = http.DefaultClient.Do(authReq(t, http.MethodPost,
		srv.URL+"/v1/tasks/sentiment/predict", `{"inputs":{"text":"hi"}}`))
	_ = json.NewDecoder(resp.Body).Decode(&pred)
	if pred.PromptVersion != 2 {
		t.Errorf("predict after deploy: version %d, want 2", pred.PromptVersion)
	}

	// Deploying a missing version → 404.
	resp, _ = http.DefaultClient.Do(authReq(t, http.MethodPost,
		srv.URL+"/v1/tasks/sentiment/deploy", `{"version":99}`))
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("deploy unknown version: got %d, want 404", resp.StatusCode)
	}
}

// ── Shadow comparison ────────────────────────────────────────────────────────

func TestShadowCompare(t *testing.T) {
	llm.ResetBreakers()
	t.Cleanup(llm.ResetBreakers)
	// Model always answers {"label":"positive"} — one expected matches, one doesn't.
	srv, _ := newPredictServer(t, `{"label":"positive"}`)

	body := `{
		"task_id": "sentiment",
		"items": [
			{"inputs":{"text":"great"},  "expected_output":{"label":"positive"}},
			{"inputs":{"text":"awful"},  "expected_output":{"label":"negative"}}
		]
	}`
	resp, err := http.DefaultClient.Do(authReq(t, http.MethodPost, srv.URL+"/v1/shadow/compare", body))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("shadow status: %d", resp.StatusCode)
	}

	var report struct {
		Items             int     `json:"items"`
		MatchRate         float64 `json:"match_rate"`
		ItemsFullyMatched int     `json:"items_fully_matched"`
		P95LatencyMs      int     `json:"p95_latency_ms"`
		Mismatches        []struct {
			Field string `json:"field"`
		} `json:"mismatches"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&report)

	if report.Items != 2 || report.ItemsFullyMatched != 1 {
		t.Errorf("items=%d fully=%d, want 2/1", report.Items, report.ItemsFullyMatched)
	}
	if report.MatchRate != 0.5 {
		t.Errorf("match_rate: got %v, want 0.5", report.MatchRate)
	}
	if len(report.Mismatches) != 1 || report.Mismatches[0].Field != "label" {
		t.Errorf("mismatches wrong: %+v", report.Mismatches)
	}

	// Report persisted and listable.
	resp, _ = http.DefaultClient.Do(authReq(t, http.MethodGet,
		srv.URL+"/v1/shadow/reports?task_id=sentiment", ""))
	var list struct {
		Reports []struct {
			MatchRate float64 `json:"match_rate"`
		} `json:"reports"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&list)
	if len(list.Reports) != 1 || list.Reports[0].MatchRate != 0.5 {
		t.Errorf("persisted reports wrong: %+v", list.Reports)
	}
}

// ── Task stats endpoint ──────────────────────────────────────────────────────

func TestTaskStatsEndpoint(t *testing.T) {
	llm.ResetBreakers()
	t.Cleanup(llm.ResetBreakers)
	srv, _ := newPredictServer(t, `{"label":"positive"}`)

	_, _ = http.DefaultClient.Do(authReq(t, http.MethodPost,
		srv.URL+"/v1/tasks/sentiment/predict", `{"inputs":{"text":"hi"}}`))

	resp, _ := http.DefaultClient.Do(authReq(t, http.MethodGet,
		srv.URL+"/v1/tasks/sentiment/stats?days=7", ""))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stats status: %d", resp.StatusCode)
	}
	var stats struct {
		Totals struct {
			Runs int `json:"runs"`
		} `json:"totals"`
		Daily []struct {
			Runs int `json:"runs"`
		} `json:"daily"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&stats)
	if stats.Totals.Runs != 1 || len(stats.Daily) != 1 {
		t.Errorf("stats wrong: %+v", stats)
	}
}

// ── RunWriter ────────────────────────────────────────────────────────────────

func TestRunWriter(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	if err := appdb.Migrate(database); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	w := appdb.NewRunWriter(database, 16)
	for i := 0; i < 10; i++ {
		ok := w.Write(&types.RunRow{
			RunID: "rw", Prompt: "p", Model: "m", CreatedAt: time.Now().UTC(),
		})
		if !ok {
			t.Fatalf("write %d dropped unexpectedly", i)
		}
	}
	w.Close() // flushes

	var n int
	_ = database.QueryRow("SELECT COUNT(*) FROM runs WHERE run_id='rw'").Scan(&n)
	if n != 10 {
		t.Errorf("rows written: got %d, want 10", n)
	}

	// Writes after close are dropped, not panicking.
	if w.Write(&types.RunRow{RunID: "late", CreatedAt: time.Now().UTC()}) {
		t.Error("write after close should report dropped")
	}
	if w.Dropped() != 1 {
		t.Errorf("dropped count: got %d, want 1", w.Dropped())
	}
}
