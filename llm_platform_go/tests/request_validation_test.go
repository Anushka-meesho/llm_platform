package tests

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	appdb "llm_platform_go/internal/db"
	"llm_platform_go/internal/types"
)

// detailOf reads the {"detail":"..."} error body the API returns on 4xx.
func detailOf(t *testing.T, resp *http.Response) string {
	t.Helper()
	var body struct {
		Detail string `json:"detail"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return body.Detail
}

// TestRequestValidationRejectsBadBodies exercises the schema middleware on a
// representative set of endpoints: malformed bodies are rejected with 422 and
// the validation-failed detail before the handler runs.
func TestRequestValidationRejectsBadBodies(t *testing.T) {
	srv, _ := newTestServer(t)

	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"feedback unknown field", http.MethodPost, "/feedback", `{"run_id":"r","model":"m","rating":4,"oops":true}`},
		{"feedback rating out of range", http.MethodPost, "/feedback", `{"run_id":"r","model":"m","rating":42}`},
		{"feedback rating wrong type", http.MethodPost, "/feedback", `{"run_id":"r","model":"m","rating":"high"}`},
		{"run missing prompt", http.MethodPost, "/run", `{"models":["gpt-4o-mini"]}`},
		{"run bad temperature", http.MethodPost, "/run", `{"prompt":"hi","temperature":5}`},
		{"create_task missing model", http.MethodPost, "/v1/tasks", `{"id":"t1","name":"T","prompt_template":"p"}`},
		{"create_task bad id slug", http.MethodPost, "/v1/tasks", `{"id":"Bad_ID","name":"T","prompt_template":"p","model":"gpt-4o-mini"}`},
		{"deploy version zero", http.MethodPost, "/v1/tasks/playground/deploy", `{"version":0}`},
		{"shadow no items", http.MethodPost, "/v1/shadow/compare", `{"task_id":"playground","items":[]}`},
		{"empty body", http.MethodPost, "/feedback", ``},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.DefaultClient.Do(authReq(t, tc.method, srv.URL+tc.path, tc.body))
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("status: got %d, want 422", resp.StatusCode)
			}
			if d := detailOf(t, resp); !strings.Contains(d, "request validation failed") &&
				!strings.Contains(d, "request body is required") {
				t.Errorf("detail %q does not attribute to the validation middleware", d)
			}
		})
	}
}

// TestRequestValidationAcceptsValidBody confirms a well-formed body passes the
// validation gate (here: feedback on a seeded run returns 200, not a 422).
func TestRequestValidationAcceptsValidBody(t *testing.T) {
	srv, database := newTestServer(t)

	row := &types.RunRow{
		RunID:     "run-valid",
		Prompt:    "p",
		Model:     "gpt-4o-mini",
		Response:  strPtr("r"),
		Success:   true,
		UserID:    strPtr("u-admin"),
		CreatedAt: time.Now().UTC(),
	}
	if err := appdb.InsertRun(database, row); err != nil {
		t.Fatalf("InsertRun: %v", err)
	}

	resp, err := http.DefaultClient.Do(authReq(t, http.MethodPost, srv.URL+"/feedback",
		`{"run_id":"run-valid","model":"gpt-4o-mini","rating":5}`))
	if err != nil {
		t.Fatalf("feedback: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200 (detail: %q)", resp.StatusCode, detailOf(t, resp))
	}
}
