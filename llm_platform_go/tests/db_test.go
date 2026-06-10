package tests

import (
	"database/sql"
	"strconv"
	"testing"
	"time"

	appdb "llm_platform_go/internal/db"
	"llm_platform_go/internal/types"

	_ "modernc.org/sqlite"
)

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	database.SetMaxOpenConns(1)
	if err := appdb.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func strPtr(s string) *string { return &s }

func TestInsertAndGetSession(t *testing.T) {
	database := newTestDB(t)

	sessionID := "sess-1"
	row := &types.RunRow{
		RunID:     "run-1",
		SessionID: &sessionID,
		Prompt:    "What is Go?",
		Model:     "gpt-4o-mini",
		Response:  strPtr("Go is a statically typed language."),
		LatencyMs: 500,
		InputTokens:  10,
		OutputTokens: 20,
		TotalTokens:  30,
		CostUSD:   0.000123,
		Success:   true,
		CreatedAt: time.Now().UTC(),
	}

	if err := appdb.InsertRun(database, row); err != nil {
		t.Fatalf("InsertRun: %v", err)
	}

	rows, err := appdb.GetSession(database, sessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}

	got := rows[0]
	if got.RunID != row.RunID {
		t.Errorf("RunID: got %q, want %q", got.RunID, row.RunID)
	}
	if got.Prompt != row.Prompt {
		t.Errorf("Prompt: got %q, want %q", got.Prompt, row.Prompt)
	}
	if !got.Success {
		t.Error("expected Success=true")
	}
	if got.Response == nil || *got.Response != *row.Response {
		t.Errorf("Response mismatch")
	}
}

func TestGetSessionUnknown(t *testing.T) {
	database := newTestDB(t)
	rows, err := appdb.GetSession(database, "no-such-session")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected 0 rows for unknown session, got %d", len(rows))
	}
}

func TestListSessionsPagination(t *testing.T) {
	database := newTestDB(t)
	now := time.Now().UTC()

	// Insert 5 sessions × 2 turns each = 10 rows
	for sess := 1; sess <= 5; sess++ {
		sessID := "sess-" + itoa(sess)
		for turn := 1; turn <= 2; turn++ {
			row := &types.RunRow{
				RunID:     "run-" + itoa(sess) + "-" + itoa(turn),
				SessionID: strPtr(sessID),
				Prompt:    "prompt " + itoa(sess),
				Model:     "gpt-4o-mini",
				Response:  strPtr("response"),
				Success:   true,
				CreatedAt: now.Add(time.Duration(sess) * time.Minute),
			}
			if err := appdb.InsertRun(database, row); err != nil {
				t.Fatalf("InsertRun: %v", err)
			}
		}
	}

	// Page 1, size 3 → 3 sessions
	sessions, total, err := appdb.ListSessions(database, 1, 3)
	if err != nil {
		t.Fatalf("ListSessions p1: %v", err)
	}
	if total != 5 {
		t.Errorf("total: got %d, want 5", total)
	}
	if len(sessions) != 3 {
		t.Errorf("page 1 len: got %d, want 3", len(sessions))
	}
	if appdb.TotalPages(total, 3) != 2 {
		t.Errorf("total pages: got %d, want 2", appdb.TotalPages(total, 3))
	}

	// Page 2, size 3 → 2 sessions
	sessions2, _, err := appdb.ListSessions(database, 2, 3)
	if err != nil {
		t.Fatalf("ListSessions p2: %v", err)
	}
	if len(sessions2) != 2 {
		t.Errorf("page 2 len: got %d, want 2", len(sessions2))
	}
}

func TestListSessionsOrderedByMostRecent(t *testing.T) {
	database := newTestDB(t)
	base := time.Now().UTC()

	for i, sessID := range []string{"old-sess", "new-sess"} {
		row := &types.RunRow{
			RunID:     "run-" + itoa(i),
			SessionID: strPtr(sessID),
			Prompt:    "prompt",
			Model:     "gpt-4o-mini",
			Success:   true,
			CreatedAt: base.Add(time.Duration(i) * time.Hour),
		}
		if err := appdb.InsertRun(database, row); err != nil {
			t.Fatalf("InsertRun: %v", err)
		}
	}

	sessions, _, err := appdb.ListSessions(database, 1, 10)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if sessions[0].SessionID != "new-sess" {
		t.Errorf("expected newest session first, got %q", sessions[0].SessionID)
	}
}

func TestDeleteSessions(t *testing.T) {
	database := newTestDB(t)
	now := time.Now().UTC()

	for _, sessID := range []string{"s1", "s2", "s3"} {
		row := &types.RunRow{
			RunID:     "run-" + sessID,
			SessionID: strPtr(sessID),
			Prompt:    "p",
			Model:     "gpt-4o-mini",
			Success:   true,
			CreatedAt: now,
		}
		if err := appdb.InsertRun(database, row); err != nil {
			t.Fatalf("InsertRun: %v", err)
		}
	}

	deleted, err := appdb.DeleteSessions(database, []string{"s1", "s2"})
	if err != nil {
		t.Fatalf("DeleteSessions: %v", err)
	}
	if deleted != 2 {
		t.Errorf("deleted count: got %d, want 2", deleted)
	}

	// s3 should still exist
	rows, _ := appdb.GetSession(database, "s3")
	if len(rows) != 1 {
		t.Errorf("s3 should still have 1 row, got %d", len(rows))
	}
}

func TestDeleteSessionsNonExistent(t *testing.T) {
	database := newTestDB(t)
	deleted, err := appdb.DeleteSessions(database, []string{"ghost-1", "ghost-2"})
	if err != nil {
		t.Fatalf("DeleteSessions: %v", err)
	}
	if deleted != 0 {
		t.Errorf("expected 0 deleted, got %d", deleted)
	}
}

func TestDeleteSessionsEmpty(t *testing.T) {
	database := newTestDB(t)
	deleted, err := appdb.DeleteSessions(database, []string{})
	if err != nil {
		t.Fatalf("DeleteSessions empty: %v", err)
	}
	if deleted != 0 {
		t.Errorf("expected 0 deleted, got %d", deleted)
	}
}

func itoa(n int) string {
	return strconv.Itoa(n)
}
