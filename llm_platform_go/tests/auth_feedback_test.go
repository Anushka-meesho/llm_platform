package tests

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"llm_platform_go/internal/auth"
	appdb "llm_platform_go/internal/db"
	"llm_platform_go/internal/types"
	"llm_platform_go/internal/users"

	_ "modernc.org/sqlite"
)

func TestDemoStore(t *testing.T) {
	s := users.NewDemoStore()

	list, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) < 2 {
		t.Fatalf("expected at least 2 seeded users, got %d", len(list))
	}

	u, err := s.GetByID(context.Background(), "u-admin")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if u.Email != "admin@demo.local" {
		t.Errorf("email: got %q", u.Email)
	}

	if _, err := s.GetByID(context.Background(), "nope"); err != users.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestTokenRoundTrip(t *testing.T) {
	u := &auth.User{Subject: "u-admin", Email: "admin@demo.local", Name: "Admin"}
	tok, err := auth.IssueToken(u, []byte(testSecret), testIssuer, time.Hour)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	got, err := auth.ParseToken(tok, []byte(testSecret))
	if err != nil {
		t.Fatalf("ParseToken: %v", err)
	}
	if got.Subject != u.Subject || got.Email != u.Email {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	// Wrong secret must fail.
	if _, err := auth.ParseToken(tok, []byte("wrong")); err == nil {
		t.Error("expected error parsing with wrong secret")
	}
}

func TestLoginFlow(t *testing.T) {
	srv, _ := newTestServer(t)

	resp, err := http.Post(srv.URL+"/auth/login", "application/json",
		jsonBody(`{"user_id":"u-admin"}`))
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login status: got %d, want 200", resp.StatusCode)
	}
	// A session cookie must be set.
	var found bool
	for _, c := range resp.Cookies() {
		if c.Name == "llm_platform_token" && c.Value != "" {
			found = true
		}
	}
	if !found {
		t.Error("expected session cookie to be set")
	}

	// Unknown user → 401.
	resp2, _ := http.Post(srv.URL+"/auth/login", "application/json", jsonBody(`{"user_id":"ghost"}`))
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("unknown user status: got %d, want 401", resp2.StatusCode)
	}
}

func TestFeedbackAndDashboard(t *testing.T) {
	srv, database := newTestServer(t)

	// Seed a run for u-admin so dashboard has data.
	sess := "s-dash"
	row := &types.RunRow{
		RunID:       "run-dash",
		SessionID:   &sess,
		Prompt:      "p",
		Model:       "gpt-4o-mini",
		Response:    strPtr("r"),
		LatencyMs:   100,
		TotalTokens: 30,
		CostUSD:     0.0005,
		Success:     true,
		UserID:      strPtr("u-admin"),
		CreatedAt:   time.Now().UTC(),
	}
	if err := appdb.InsertRun(database, row); err != nil {
		t.Fatalf("InsertRun: %v", err)
	}

	// Submit feedback.
	resp, err := http.DefaultClient.Do(authReq(t, http.MethodPost, srv.URL+"/feedback",
		`{"run_id":"run-dash","model":"gpt-4o-mini","rating":4}`))
	if err != nil {
		t.Fatalf("feedback: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("feedback status: got %d, want 200", resp.StatusCode)
	}

	// Out-of-range rating rejected.
	bad, _ := http.DefaultClient.Do(authReq(t, http.MethodPost, srv.URL+"/feedback",
		`{"run_id":"run-dash","model":"gpt-4o-mini","rating":9}`))
	if bad.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("bad rating status: got %d, want 422", bad.StatusCode)
	}

	// Dashboard reflects the run + rating.
	dresp, err := http.DefaultClient.Do(authReq(t, http.MethodGet, srv.URL+"/dashboard", ""))
	if err != nil {
		t.Fatalf("dashboard: %v", err)
	}
	var d types.DashboardResponse
	if err := json.NewDecoder(dresp.Body).Decode(&d); err != nil {
		t.Fatalf("decode dashboard: %v", err)
	}
	if d.TotalRuns != 1 {
		t.Errorf("total_runs: got %d, want 1", d.TotalRuns)
	}
	if len(d.ByModel) != 1 || d.ByModel[0].AvgRating != 4 {
		t.Errorf("expected one model with avg rating 4, got %+v", d.ByModel)
	}
	if len(d.Daily) != 1 {
		t.Errorf("daily points: got %d, want 1", len(d.Daily))
	}
}
