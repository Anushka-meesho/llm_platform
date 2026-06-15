package tests

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"llm_platform_go/internal/llm"
	"llm_platform_go/internal/tasks"
)

// fakeModelServer serves an OpenAI-compatible /chat/completions endpoint that
// always returns `content` with fixed usage numbers.
func fakeModelServer(t *testing.T, content string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": content}},
			},
			"usage": map[string]any{"prompt_tokens": 42, "completion_tokens": 10},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newPredictServer wires a test server whose gpt-4o-mini routes to a fake
// model returning `modelOutput`, with one registered classification task.
func newPredictServer(t *testing.T, modelOutput string) (*httptest.Server, *sql.DB) {
	t.Helper()
	fake := fakeModelServer(t, modelOutput)
	clients := &llm.Clients{
		OpenAI: llm.NewOpenAICompatProvider(fake.URL, "test-key"),
	}
	srv, database := newTestServerWithClients(t, clients)

	// Register the test task via the API (exercises CreateTask too).
	taskJSON := `{
		"id": "sentiment",
		"name": "Sentiment",
		"model": "gpt-4o-mini",
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
	return srv, database
}

func TestPredictHappyPath(t *testing.T) {
	srv, _ := newPredictServer(t, `{"label":"positive"}`)

	resp, err := http.DefaultClient.Do(authReq(t, http.MethodPost,
		srv.URL+"/v1/tasks/sentiment/predict", `{"inputs":{"text":"I love it"}}`))
	if err != nil {
		t.Fatalf("predict: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("predict status: got %d, want 200", resp.StatusCode)
	}

	var out struct {
		TaskRunID     string          `json:"task_run_id"`
		TaskID        string          `json:"task_id"`
		PromptVersion int             `json:"prompt_version"`
		Provider      string          `json:"provider"`
		Output        json.RawMessage `json:"output"`
		OutputValid   *bool           `json:"output_valid"`
		Usage         struct {
			TotalTokens int     `json:"total_tokens"`
			CostUSD     float64 `json:"cost_usd"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if out.TaskID != "sentiment" || out.PromptVersion != 1 || out.Provider != "openai" {
		t.Errorf("attribution wrong: %+v", out)
	}
	if out.OutputValid == nil || !*out.OutputValid {
		t.Errorf("output_valid: got %v, want true", out.OutputValid)
	}
	var parsed map[string]string
	if err := json.Unmarshal(out.Output, &parsed); err != nil || parsed["label"] != "positive" {
		t.Errorf("output: got %s", out.Output)
	}
	if out.Usage.TotalTokens != 52 {
		t.Errorf("total tokens: got %d, want 52", out.Usage.TotalTokens)
	}
	if out.Usage.CostUSD <= 0 {
		t.Errorf("cost should be > 0, got %v", out.Usage.CostUSD)
	}

	// Poll endpoint returns the task-stamped run.
	poll, err := http.DefaultClient.Do(authReq(t, http.MethodGet,
		srv.URL+"/v1/tasks/runs/"+out.TaskRunID, ""))
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if poll.StatusCode != http.StatusOK {
		t.Fatalf("poll status: got %d, want 200", poll.StatusCode)
	}
	var polled struct {
		TaskID        *string `json:"task_id"`
		PromptVersion int     `json:"prompt_version"`
	}
	_ = json.NewDecoder(poll.Body).Decode(&polled)
	if polled.TaskID == nil || *polled.TaskID != "sentiment" || polled.PromptVersion != 1 {
		t.Errorf("polled run not task-stamped: %+v", polled)
	}
}

func TestPredictInputValidation(t *testing.T) {
	srv, _ := newPredictServer(t, `{"label":"positive"}`)

	cases := []struct {
		name string
		body string
	}{
		{"missing required field", `{"inputs":{}}`},
		{"wrong type", `{"inputs":{"text":42}}`},
		{"no inputs key", `{}`},
	}
	for _, tc := range cases {
		resp, err := http.DefaultClient.Do(authReq(t, http.MethodPost,
			srv.URL+"/v1/tasks/sentiment/predict", tc.body))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s: got %d, want 422", tc.name, resp.StatusCode)
		}
	}
}

func TestPredictInvalidOutputFlagged(t *testing.T) {
	// Model returns JSON that violates the output schema.
	srv, _ := newPredictServer(t, `{"wrong_key":"oops"}`)

	resp, err := http.DefaultClient.Do(authReq(t, http.MethodPost,
		srv.URL+"/v1/tasks/sentiment/predict", `{"inputs":{"text":"meh"}}`))
	if err != nil {
		t.Fatalf("predict: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (invalid output is flagged, not failed)", resp.StatusCode)
	}

	var out struct {
		OutputValid *bool           `json:"output_valid"`
		Output      json.RawMessage `json:"output"`
		RawResponse *string         `json:"raw_response"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out.OutputValid == nil || *out.OutputValid {
		t.Errorf("output_valid: got %v, want false", out.OutputValid)
	}
	if len(out.Output) > 0 && string(out.Output) != "null" {
		t.Errorf("output should be null for invalid responses, got %s", out.Output)
	}
	if out.RawResponse == nil {
		t.Error("raw_response should be preserved for debugging")
	}
}

func TestPredictUnknownTask(t *testing.T) {
	srv, _ := newTestServer(t)
	resp, err := http.DefaultClient.Do(authReq(t, http.MethodPost,
		srv.URL+"/v1/tasks/no-such-task/predict", `{"inputs":{"x":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", resp.StatusCode)
	}
}

func TestRunStampedAsPlayground(t *testing.T) {
	fake := fakeModelServer(t, "hello there")
	clients := &llm.Clients{OpenAI: llm.NewOpenAICompatProvider(fake.URL, "k")}
	srv, database := newTestServerWithClients(t, clients)

	resp, err := http.DefaultClient.Do(authReq(t, http.MethodPost, srv.URL+"/run",
		`{"prompt":"hi","models":["gpt-4o-mini"]}`))
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("run: %v status=%d", err, resp.StatusCode)
	}

	var taskID, provider string
	err = database.QueryRow(
		"SELECT COALESCE(task_id,''), COALESCE(provider,'') FROM runs ORDER BY id DESC LIMIT 1",
	).Scan(&taskID, &provider)
	if err != nil {
		t.Fatalf("query run row: %v", err)
	}
	if taskID != tasks.PlaygroundTaskID {
		t.Errorf("task_id: got %q, want %q", taskID, tasks.PlaygroundTaskID)
	}
	if provider != "openai" {
		t.Errorf("provider: got %q, want openai", provider)
	}
}

func TestDashboardByTask(t *testing.T) {
	srv, _ := newPredictServer(t, `{"label":"positive"}`)

	// One prediction → dashboard should show the sentiment task.
	_, err := http.DefaultClient.Do(authReq(t, http.MethodPost,
		srv.URL+"/v1/tasks/sentiment/predict", `{"inputs":{"text":"nice"}}`))
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.DefaultClient.Do(authReq(t, http.MethodGet, srv.URL+"/dashboard", ""))
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		ByTask []struct {
			TaskID      string  `json:"task_id"`
			Runs        int     `json:"runs"`
			SuccessRate float64 `json:"success_rate"`
		} `json:"by_task"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(d.ByTask) != 1 {
		t.Fatalf("by_task len: got %d, want 1 (%+v)", len(d.ByTask), d.ByTask)
	}
	row := d.ByTask[0]
	if row.TaskID != "sentiment" || row.Runs != 1 || row.SuccessRate != 1 {
		t.Errorf("by_task row wrong: %+v", row)
	}
}
