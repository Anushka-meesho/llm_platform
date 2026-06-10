package tests

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"llm_platform_go/internal/api"
	appdb "llm_platform_go/internal/db"
	"llm_platform_go/internal/llm"
	"llm_platform_go/internal/types"

	_ "modernc.org/sqlite"
)

// mockClients returns Clients with nil underlying openai.Client values.
// The runner will fail on each call, which is fine for endpoint-shape tests
// that don't need real LLM responses.
// For tests that need success responses, insert pre-cooked DB rows directly.
func newTestServer(t *testing.T) (*httptest.Server, *sql.DB) {
	t.Helper()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	database.SetMaxOpenConns(1)
	if err := appdb.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Load pricing so CalculateCost doesn't blow up.
	_ = llm.LoadPricingFromMap(map[string]llm.Rate{
		"gpt-4o-mini": {InputPer1M: 0.15, OutputPer1M: 0.60},
	})

	clients := &llm.Clients{} // nil clients → callSingleModel returns errors, which is fine
	router := api.NewRouter(database, clients)

	srv := httptest.NewServer(router)
	t.Cleanup(func() {
		srv.Close()
		database.Close()
	})
	return srv, database
}

func TestHealthCheck(t *testing.T) {
	srv, _ := newTestServer(t)
	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}
}

func TestRunEndpointEmptyPrompt(t *testing.T) {
	srv, _ := newTestServer(t)
	body := `{"prompt":""}`
	resp, err := http.Post(srv.URL+"/run", "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("POST /run: %v", err)
	}
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status: got %d, want 422", resp.StatusCode)
	}
}

func TestRunEndpointInvalidJSON(t *testing.T) {
	srv, _ := newTestServer(t)
	resp, err := http.Post(srv.URL+"/run", "application/json", bytes.NewBufferString("not-json"))
	if err != nil {
		t.Fatalf("POST /run: %v", err)
	}
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status: got %d, want 422", resp.StatusCode)
	}
}

func TestRunEndpointReturnsShape(t *testing.T) {
	srv, _ := newTestServer(t)
	// With nil clients, each model call will fail — but the response shape is still correct.
	body := `{"prompt":"hello","models":["gpt-4o-mini"]}`
	resp, err := http.Post(srv.URL+"/run", "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("POST /run: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}

	var result types.RunResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.RunID == "" {
		t.Error("run_id should not be empty")
	}
	if result.Prompt != "hello" {
		t.Errorf("prompt: got %q, want %q", result.Prompt, "hello")
	}
	if len(result.Results) != 1 {
		t.Errorf("results len: got %d, want 1", len(result.Results))
	}
}

func TestListSessionsEmpty(t *testing.T) {
	srv, _ := newTestServer(t)
	resp, err := http.Get(srv.URL + "/sessions")
	if err != nil {
		t.Fatalf("GET /sessions: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}

	var result types.SessionListResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Sessions) != 0 {
		t.Errorf("sessions: got %d, want 0", len(result.Sessions))
	}
}

func TestGetSessionNotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	resp, err := http.Get(srv.URL + "/sessions/no-such-id")
	if err != nil {
		t.Fatalf("GET /sessions/no-such-id: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", resp.StatusCode)
	}
}

func TestGetSessionFound(t *testing.T) {
	srv, database := newTestServer(t)

	sessID := "test-session"
	row := &types.RunRow{
		RunID:     "run-xyz",
		SessionID: &sessID,
		Prompt:    "test prompt",
		Model:     "gpt-4o-mini",
		Response:  strPtr("a response"),
		Success:   true,
		CreatedAt: time.Now().UTC(),
	}
	if err := appdb.InsertRun(database, row); err != nil {
		t.Fatalf("InsertRun: %v", err)
	}

	resp, err := http.Get(srv.URL + "/sessions/" + sessID)
	if err != nil {
		t.Fatalf("GET /sessions/%s: %v", sessID, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}

	var result types.SessionDetailResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.SessionID != sessID {
		t.Errorf("session_id: got %q, want %q", result.SessionID, sessID)
	}
	if len(result.Turns) != 1 {
		t.Errorf("turns: got %d, want 1", len(result.Turns))
	}
}

func TestDeleteSessionsEndpoint(t *testing.T) {
	srv, database := newTestServer(t)

	for _, sid := range []string{"d1", "d2"} {
		s := sid
		row := &types.RunRow{
			RunID:     "run-" + sid,
			SessionID: &s,
			Prompt:    "p",
			Model:     "gpt-4o-mini",
			Success:   true,
			CreatedAt: time.Now().UTC(),
		}
		if err := appdb.InsertRun(database, row); err != nil {
			t.Fatalf("InsertRun: %v", err)
		}
	}

	body := `{"session_ids":["d1","d2"]}`
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/sessions", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE /sessions: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}

	var result types.DeleteSessionsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.DeletedCount != 2 {
		t.Errorf("deleted_count: got %d, want 2", result.DeletedCount)
	}
}
