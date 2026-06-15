package tests

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
	// With nil gateway, each model call will fail — but the SSE stream shape is still correct.
	body := `{"prompt":"hello","models":["gpt-4o-mini"]}`
	resp, err := http.Post(srv.URL+"/run", "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("POST /run: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type: got %q, want text/event-stream", ct)
	}

	// Parse SSE events from the stream.
	var runID string
	var results []types.ModelResultResponse
	var eventType, data string

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			eventType = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data = strings.TrimPrefix(line, "data: ")
		case line == "" && eventType != "":
			switch eventType {
			case "meta":
				var m struct {
					RunID string `json:"run_id"`
				}
				json.Unmarshal([]byte(data), &m) //nolint:errcheck
				runID = m.RunID
			case "result":
				var r types.ModelResultResponse
				json.Unmarshal([]byte(data), &r) //nolint:errcheck
				results = append(results, r)
			}
			eventType, data = "", ""
		}
	}

	if runID == "" {
		t.Error("meta event run_id should not be empty")
	}
	if len(results) != 1 {
		t.Errorf("result events: got %d, want 1", len(results))
	}
	if len(results) > 0 && results[0].Model != "gpt-4o-mini" {
		t.Errorf("model: got %q, want gpt-4o-mini", results[0].Model)
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
