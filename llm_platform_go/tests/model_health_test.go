package tests

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"llm_platform_go/internal/api"
	"llm_platform_go/internal/auth"
	appdb "llm_platform_go/internal/db"
	"llm_platform_go/internal/health"
	"llm_platform_go/internal/llm"
	"llm_platform_go/internal/tasks"
	"llm_platform_go/internal/users"

	"database/sql"
)

// newHealthServer builds a server whose predict path is gated by a real
// per-(task, model) breaker (threshold 2 so tests trip quickly) writing events
// to the DB. The fake provider's behaviour is controlled by `respond`.
func newHealthServer(t *testing.T, respond func(modelID string) (status int, body string)) (*httptest.Server, *sql.DB) {
	t.Helper()

	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		status, body := respond(req.Model)
		w.Header().Set("Content-Type", "application/json")
		if status != http.StatusOK {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "boom"}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": body}}},
			"usage":   map[string]any{"prompt_tokens": 5, "completion_tokens": 5},
		})
	}))
	t.Cleanup(fake.Close)

	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	database.SetMaxOpenConns(1)
	if err := appdb.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	_ = llm.LoadPricingFromMap(map[string]llm.Rate{
		"gpt-4o":      {InputPer1M: 1, OutputPer1M: 1},
		"gpt-4o-mini": {InputPer1M: 1, OutputPer1M: 1},
	})

	taskStore := tasks.NewStore(database)
	healthWriter := appdb.NewHealthEventWriter(database, 0)
	t.Cleanup(healthWriter.Close)
	tracker := health.NewTracker(health.Config{
		Enabled: true, Threshold: 2, BaseCooldown: time.Hour, MaxCooldown: time.Hour, Factor: 2,
	}, healthWriter.Write)

	router := api.NewRouter(api.RouterDeps{
		DB:      database,
		Clients: &llm.Clients{Meesho: llm.NewOpenAICompatProvider(fake.URL, "k")},
		Users:   users.NewDemoStore(),
		Tasks:   taskStore,
		Health:  tracker,
		Auth: api.AuthConfig{
			Secret: []byte(testSecret), CookieName: "llm_platform_token",
			Issuer: testIssuer, TokenExpiry: time.Hour,
		},
	})
	srv := httptest.NewServer(router)
	t.Cleanup(func() { srv.Close(); database.Close() })
	return srv, database
}

func createTask(t *testing.T, srv *httptest.Server, body string) {
	t.Helper()
	resp, err := http.DefaultClient.Do(authReq(t, http.MethodPost, srv.URL+"/v1/tasks", body))
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("create task: err=%v status=%v", err, resp.StatusCode)
	}
	resp.Body.Close()
}

func predict(t *testing.T, srv *httptest.Server, taskID, inputs string) *http.Response {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"inputs": json.RawMessage(inputs)})
	resp, err := http.DefaultClient.Do(authReq(t, http.MethodPost,
		srv.URL+"/v1/tasks/"+taskID+"/predict", string(body)))
	if err != nil {
		t.Fatalf("predict: %v", err)
	}
	return resp
}

// TestSchemaInvalidFallsBackToNextModel: primary returns valid-JSON-but-wrong-
// schema, fallback returns a schema-valid answer → the fallback's answer wins
// in the same request, and the primary is recorded against health.
func TestSchemaInvalidFallsBackToNextModel(t *testing.T) {
	srv, _ := newHealthServer(t, func(modelID string) (int, string) {
		if modelID == "openai/gpt-4o" { // primary: missing required "label"
			return http.StatusOK, `{"wrong":"shape"}`
		}
		return http.StatusOK, `{"label":"ok"}` // fallback: valid
	})
	createTask(t, srv, `{
		"id":"schemafb","name":"T","model":"gpt-4o","fallback_models":["gpt-4o-mini"],
		"prompt_template":"Classify {{.text}}",
		"input_schema":{"type":"object","required":["text"],"properties":{"text":{"type":"string"}}},
		"output_schema":{"type":"object","required":["label"],"properties":{"label":{"type":"string"}}}
	}`)

	resp := predict(t, srv, "schemafb", `{"text":"hi"}`)
	defer resp.Body.Close()
	var out struct {
		Model        string `json:"model"`
		OutputValid  *bool  `json:"output_valid"`
		FallbackUsed bool   `json:"fallback_used"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if out.Model != "gpt-4o-mini" || !out.FallbackUsed || out.OutputValid == nil || !*out.OutputValid {
		t.Fatalf("schema-invalid primary should fall back to a valid model: %+v", out)
	}
}

// TestModelTripsAndAdminReset: a model that keeps failing trips unhealthy after
// the threshold, surfaces in the admin snapshot with persisted events, and an
// admin reset restores it to healthy.
func TestModelTripsAndAdminReset(t *testing.T) {
	srv, _ := newHealthServer(t, func(modelID string) (int, string) {
		return http.StatusUnauthorized, "" // apikey/subscription failure — fallback-eligible, not retried
	})
	createTask(t, srv, `{
		"id":"flaky","name":"Flaky","model":"gpt-4o",
		"prompt_template":"Do {{.text}}",
		"input_schema":{"type":"object","required":["text"],"properties":{"text":{"type":"string"}}}
	}`)

	// Threshold is 2 — two predicts trip gpt-4o for task "flaky".
	for i := 0; i < 2; i++ {
		predict(t, srv, "flaky", `{"text":"hi"}`).Body.Close()
	}

	// Snapshot must show gpt-4o unhealthy.
	resp, err := http.DefaultClient.Do(authReq(t, http.MethodGet, srv.URL+"/v1/admin/model-health", ""))
	if err != nil {
		t.Fatalf("get health: %v", err)
	}
	var snap struct {
		Enabled  bool `json:"enabled"`
		Statuses []struct {
			TaskID string `json:"task_id"`
			Model  string `json:"model"`
			State  string `json:"state"`
		} `json:"statuses"`
	}
	json.NewDecoder(resp.Body).Decode(&snap)
	resp.Body.Close()
	if !snap.Enabled {
		t.Fatal("health should report enabled")
	}
	var found bool
	for _, s := range snap.Statuses {
		if s.TaskID == "flaky" && s.Model == "gpt-4o" {
			found = true
			if s.State != "unhealthy" {
				t.Errorf("gpt-4o should be unhealthy, got %s", s.State)
			}
		}
	}
	if !found {
		t.Fatalf("gpt-4o/flaky missing from snapshot: %+v", snap.Statuses)
	}

	// Events persisted (give the async writer a beat to drain).
	var eventsTotal int
	for attempt := 0; attempt < 20; attempt++ {
		r, _ := http.DefaultClient.Do(authReq(t, http.MethodGet, srv.URL+"/v1/admin/model-health/events?task_id=flaky", ""))
		var ev struct {
			TotalCount int `json:"total_count"`
		}
		json.NewDecoder(r.Body).Decode(&ev)
		r.Body.Close()
		eventsTotal = ev.TotalCount
		if eventsTotal > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if eventsTotal == 0 {
		t.Error("expected persisted health events for the flaky task")
	}

	// Admin reset → healthy.
	r, err := http.DefaultClient.Do(authReq(t, http.MethodPost, srv.URL+"/v1/admin/model-health/reset",
		`{"task_id":"flaky","model":"gpt-4o"}`))
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if r.StatusCode != http.StatusOK {
		t.Fatalf("reset status: %d", r.StatusCode)
	}
	r.Body.Close()

	resp, _ = http.DefaultClient.Do(authReq(t, http.MethodGet, srv.URL+"/v1/admin/model-health", ""))
	json.NewDecoder(resp.Body).Decode(&snap)
	resp.Body.Close()
	for _, s := range snap.Statuses {
		if s.TaskID == "flaky" && s.Model == "gpt-4o" && s.State != "healthy" {
			t.Errorf("after reset gpt-4o should be healthy, got %s", s.State)
		}
	}

	// Unknown reset → 404; non-admin → 403.
	r, _ = http.DefaultClient.Do(authReq(t, http.MethodPost, srv.URL+"/v1/admin/model-health/reset",
		`{"task_id":"nope","model":"nope"}`))
	if r.StatusCode != http.StatusNotFound {
		t.Errorf("unknown reset want 404, got %d", r.StatusCode)
	}
	r.Body.Close()
	r, _ = http.DefaultClient.Do(roleReq(t, auth.RoleCreator, http.MethodGet, srv.URL+"/v1/admin/model-health", ""))
	if r.StatusCode != http.StatusForbidden {
		t.Errorf("non-admin health view want 403, got %d", r.StatusCode)
	}
	r.Body.Close()
}
