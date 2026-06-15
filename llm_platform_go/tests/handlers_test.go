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
	"llm_platform_go/internal/auth"
	"llm_platform_go/internal/cache"
	appdb "llm_platform_go/internal/db"
	"llm_platform_go/internal/llm"
	"llm_platform_go/internal/tasks"
	"llm_platform_go/internal/types"
	"llm_platform_go/internal/users"

	_ "modernc.org/sqlite"
)

const (
	testSecret = "test-secret"
	testIssuer = "test"
)

// testToken is a valid session token for the seeded demo "admin" user, used to
// authenticate requests against the protected endpoints.
func testToken(t *testing.T) string {
	t.Helper()
	u := &auth.User{Subject: "u-admin", Email: "admin@demo.local", Name: "Admin"}
	tok, err := auth.IssueToken(u, []byte(testSecret), testIssuer, time.Hour)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	return tok
}

// jsonBody wraps a JSON string as a request body reader.
func jsonBody(s string) *bytes.Buffer { return bytes.NewBufferString(s) }

// authReq builds a request carrying the test session token.
func authReq(t *testing.T, method, url, body string) *http.Request {
	t.Helper()
	var r *http.Request
	var err error
	if body == "" {
		r, err = http.NewRequest(method, url, nil)
	} else {
		r, err = http.NewRequest(method, url, bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
	}
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	r.Header.Set("Authorization", "Bearer "+testToken(t))
	return r
}

// newTestServer wires a fully authenticated test server with nil model
// clients — model calls fail, which is fine for endpoint-shape tests. For
// tests that need successful model output, use newTestServerWithClients.
func newTestServer(t *testing.T) (*httptest.Server, *sql.DB) {
	return newTestServerWithClients(t, &llm.Clients{})
}

func newTestServerWithClients(t *testing.T, clients *llm.Clients) (*httptest.Server, *sql.DB) {
	return newTestServerWithCache(t, clients, nil)
}

func newTestServerWithCache(t *testing.T, clients *llm.Clients, predictionCache cache.Cache) (*httptest.Server, *sql.DB) {
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

	taskStore := tasks.NewStore(database)
	if err := tasks.SeedPlayground(taskStore); err != nil {
		t.Fatalf("seed playground: %v", err)
	}

	router := api.NewRouter(api.RouterDeps{
		DB:      database,
		Clients: clients,
		Users:   users.NewDemoStore(),
		Tasks:   taskStore,
		Cache:   predictionCache,
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

func TestRunEndpointRequiresAuth(t *testing.T) {
	srv, _ := newTestServer(t)
	resp, err := http.Post(srv.URL+"/run", "application/json", bytes.NewBufferString(`{"prompt":"hi"}`))
	if err != nil {
		t.Fatalf("POST /run: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", resp.StatusCode)
	}
}

func TestRunEndpointEmptyPrompt(t *testing.T) {
	srv, _ := newTestServer(t)
	resp, err := http.DefaultClient.Do(authReq(t, http.MethodPost, srv.URL+"/run", `{"prompt":""}`))
	if err != nil {
		t.Fatalf("POST /run: %v", err)
	}
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status: got %d, want 422", resp.StatusCode)
	}
}

func TestRunEndpointInvalidJSON(t *testing.T) {
	srv, _ := newTestServer(t)
	resp, err := http.DefaultClient.Do(authReq(t, http.MethodPost, srv.URL+"/run", "not-json"))
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
	resp, err := http.DefaultClient.Do(authReq(t, http.MethodPost, srv.URL+"/run", body))
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
	resp, err := http.DefaultClient.Do(authReq(t, http.MethodGet, srv.URL+"/sessions", ""))
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
	resp, err := http.DefaultClient.Do(authReq(t, http.MethodGet, srv.URL+"/sessions/no-such-id", ""))
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
		UserID:    strPtr("u-admin"),
		CreatedAt: time.Now().UTC(),
	}
	if err := appdb.InsertRun(database, row); err != nil {
		t.Fatalf("InsertRun: %v", err)
	}

	resp, err := http.DefaultClient.Do(authReq(t, http.MethodGet, srv.URL+"/sessions/"+sessID, ""))
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
			UserID:    strPtr("u-admin"),
			CreatedAt: time.Now().UTC(),
		}
		if err := appdb.InsertRun(database, row); err != nil {
			t.Fatalf("InsertRun: %v", err)
		}
	}

	body := `{"session_ids":["d1","d2"]}`
	resp, err := http.DefaultClient.Do(authReq(t, http.MethodDelete, srv.URL+"/sessions", body))
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
