package tests

import (
	"database/sql"
	"testing"
	"time"

	appdb "llm_platform_go/internal/db"
	"llm_platform_go/internal/types"

	_ "modernc.org/sqlite"
)

// TestMigrateAddsResponseToExistingAttempts guards the upgrade path: a database
// whose gateway_attempts table predates the response column must gain it on
// Migrate, rather than leaving every run-detail load failing on "no such
// column: response".
func TestMigrateAddsResponseToExistingAttempts(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { database.Close() })

	// Simulate an older DB: gateway_attempts as it first shipped — every column
	// except response.
	if _, err := database.Exec(`
		CREATE TABLE gateway_attempts (
			id                INTEGER PRIMARY KEY AUTOINCREMENT,
			run_id            TEXT NOT NULL,
			task_id           TEXT,
			seq               INTEGER NOT NULL DEFAULT 0,
			model             TEXT NOT NULL,
			provider          TEXT NOT NULL DEFAULT '',
			outcome           TEXT NOT NULL,
			fallback_used     INTEGER NOT NULL DEFAULT 0,
			fallback_reason   TEXT NOT NULL DEFAULT '',
			error             TEXT NOT NULL DEFAULT '',
			http_status       INTEGER NOT NULL DEFAULT 0,
			infra_failure     INTEGER NOT NULL DEFAULT 0,
			retry_count       INTEGER NOT NULL DEFAULT 0,
			latency_ms        INTEGER NOT NULL DEFAULT 0,
			input_tokens      INTEGER NOT NULL DEFAULT 0,
			output_tokens     INTEGER NOT NULL DEFAULT 0,
			total_tokens      INTEGER NOT NULL DEFAULT 0,
			cost_usd          REAL    NOT NULL DEFAULT 0.0,
			is_test           INTEGER NOT NULL DEFAULT 0,
			created_at        DATETIME NOT NULL DEFAULT (datetime('now'))
		)`); err != nil {
		t.Fatalf("seed old table: %v", err)
	}

	if err := appdb.Migrate(database); err != nil {
		t.Fatalf("migrate should upgrade the table in place: %v", err)
	}

	// The response column must now exist and be queryable.
	if _, err := appdb.ListGatewayAttempts(database, "any"); err != nil {
		t.Fatalf("ListGatewayAttempts after migrate: %v", err)
	}
}

// TestGatewayAttemptsPersistence covers the full gateway-trace round trip:
// several attempts for one run are inserted, read back in walk order, and
// attached to the run's detail alongside the served run row.
func TestGatewayAttemptsPersistence(t *testing.T) {
	database := newTestDB(t)

	runID := "run-trace-1"
	taskID := "task-abc"
	now := time.Now().UTC()

	// The run row: the single answer ultimately served (the fallback model won).
	if err := appdb.InsertRun(database, &types.RunRow{
		RunID:        runID,
		Prompt:       "extract attributes",
		Model:        "llama-groq",
		Response:     strPtr("{}"),
		Success:      true,
		UserID:       strPtr(testUser),
		UserEmail:    strPtr("t@example.com"),
		TaskID:       &taskID,
		Provider:     strPtr("groq"),
		FallbackUsed: true,
		CreatedAt:    now,
	}); err != nil {
		t.Fatalf("InsertRun: %v", err)
	}

	// The trace: primary failed (infra), fallback served the answer.
	attempts := []types.GatewayAttempt{
		{
			RunID: runID, TaskID: &taskID, Seq: 0, Model: "gpt-4o-mini", Provider: "openai",
			Outcome: "schema_invalid", FallbackUsed: false,
			FallbackReason: "output failed schema validation",
			Response:       strPtr(`{"oops":"not the shape we asked for"}`),
			HTTPStatus:     200, RetryCount: 1, LatencyMs: 900,
			InputTokens: 12, OutputTokens: 8, TotalTokens: 20, CostUSD: 0.0002,
			CreatedAt: now,
		},
		{
			RunID: runID, TaskID: &taskID, Seq: 1, Model: "llama-groq", Provider: "groq",
			Outcome: "success", FallbackUsed: true, HTTPStatus: 200, RetryCount: 1,
			LatencyMs: 800, InputTokens: 10, OutputTokens: 5, TotalTokens: 15, CostUSD: 0.0001,
			CreatedAt: now,
		},
	}
	for i := range attempts {
		if err := appdb.InsertGatewayAttempt(database, &attempts[i]); err != nil {
			t.Fatalf("InsertGatewayAttempt seq=%d: %v", i, err)
		}
	}

	// ListGatewayAttempts returns them in walk order.
	got, err := appdb.ListGatewayAttempts(database, runID)
	if err != nil {
		t.Fatalf("ListGatewayAttempts: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 attempts, got %d", len(got))
	}
	if got[0].Seq != 0 || got[0].Outcome != "schema_invalid" ||
		got[0].HTTPStatus != 200 || got[0].FallbackReason == "" {
		t.Errorf("attempt 0 did not round-trip: %+v", got[0])
	}
	// The schema-invalid attempt must preserve what the model returned — it still
	// cost tokens, so the wasted output stays visible in the trace.
	if got[0].Response == nil || *got[0].Response != `{"oops":"not the shape we asked for"}` {
		t.Errorf("schema_invalid response did not round-trip: %v", got[0].Response)
	}
	if got[0].TotalTokens != 20 || got[0].CostUSD == 0 {
		t.Errorf("schema_invalid attempt should keep its token/cost usage: %+v", got[0])
	}
	if got[1].Seq != 1 || got[1].Outcome != "success" || !got[1].FallbackUsed ||
		got[1].Model != "llama-groq" || got[1].TotalTokens != 15 {
		t.Errorf("attempt 1 did not round-trip: %+v", got[1])
	}

	// GetRunDetail attaches the trace to the run's detail.
	detail, err := appdb.GetRunDetail(database, runID)
	if err != nil {
		t.Fatalf("GetRunDetail: %v", err)
	}
	if detail == nil {
		t.Fatal("expected a run detail, got nil")
	}
	if len(detail.Attempts) != 2 {
		t.Fatalf("run detail should carry 2 attempts, got %d", len(detail.Attempts))
	}
	if detail.Attempts[0].Model != "gpt-4o-mini" || detail.Attempts[1].Model != "llama-groq" {
		t.Errorf("attempts attached out of order: %+v", detail.Attempts)
	}
}
