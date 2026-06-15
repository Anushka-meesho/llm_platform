package db

import (
	"database/sql"
	"fmt"
	"math"
	"strings"
	"time"

	"llm_platform_go/internal/types"
)

func InsertRun(db *sql.DB, r *types.RunRow) error {
	_, err := db.Exec(`
		INSERT INTO runs
			(run_id, session_id, prompt, system_prompt, model, response,
			 latency_ms, input_tokens, output_tokens, total_tokens,
			 cost_usd, success, error, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.RunID, r.SessionID, r.Prompt, r.SystemPrompt, r.Model, r.Response,
		r.LatencyMs, r.InputTokens, r.OutputTokens, r.TotalTokens,
		r.CostUSD, boolToInt(r.Success), r.Error,
		r.CreatedAt.UTC().Format("2006-01-02 15:04:05"),
	)
	return err
}

// ListSessions returns paginated session summaries ordered by most-recently-updated.
func ListSessions(db *sql.DB, page, pageSize int) ([]types.SessionSummary, int, error) {
	total, err := countSessions(db)
	if err != nil {
		return nil, 0, err
	}

	offset := (page - 1) * pageSize
	rows, err := db.Query(`
		SELECT
			session_id,
			MIN(prompt)      AS first_prompt,
			COUNT(DISTINCT run_id) AS turn_count,
			MIN(created_at)  AS created_at
		FROM runs
		WHERE session_id IS NOT NULL
		GROUP BY session_id
		ORDER BY MAX(created_at) DESC
		LIMIT ? OFFSET ?`, pageSize, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var sessions []types.SessionSummary
	for rows.Next() {
		var s types.SessionSummary
		var firstPrompt, createdAtStr string
		if err := rows.Scan(&s.SessionID, &firstPrompt, &s.TurnCount, &createdAtStr); err != nil {
			return nil, 0, err
		}
		// Truncate first_prompt to 80 chars like the Python version does.
		if len(firstPrompt) > 80 {
			firstPrompt = firstPrompt[:80] + "..."
		}
		s.FirstPrompt = firstPrompt
		s.CreatedAt = parseTime(createdAtStr)
		sessions = append(sessions, s)
	}
	if sessions == nil {
		sessions = []types.SessionSummary{}
	}
	return sessions, total, rows.Err()
}

// GetSession returns all runs for a session in chronological order.
func GetSession(db *sql.DB, sessionID string) ([]types.RunRow, error) {
	rows, err := db.Query(`
		SELECT id, run_id, session_id, prompt, system_prompt, model, response,
		       latency_ms, input_tokens, output_tokens, total_tokens,
		       cost_usd, success, error, created_at, rating, note
		FROM runs
		WHERE session_id = ?
		ORDER BY created_at ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []types.RunRow
	for rows.Next() {
		var r types.RunRow
		var successInt int
		var createdAtStr string
		err := rows.Scan(
			&r.ID, &r.RunID, &r.SessionID, &r.Prompt, &r.SystemPrompt,
			&r.Model, &r.Response,
			&r.LatencyMs, &r.InputTokens, &r.OutputTokens, &r.TotalTokens,
			&r.CostUSD, &successInt, &r.Error, &createdAtStr,
			&r.Rating, &r.Note,
		)
		if err != nil {
			return nil, err
		}
		r.Success = successInt == 1
		r.CreatedAt = parseTime(createdAtStr)
		result = append(result, r)
	}
	return result, rows.Err()
}

// UpsertRating saves a 1–5 star rating and optional note for a specific (run_id, model, session_id) row.
func UpsertRating(db *sql.DB, runID, model, sessionID string, rating int, note string) error {
	res, err := db.Exec(
		"UPDATE runs SET rating = ?, note = ? WHERE run_id = ? AND model = ? AND session_id = ?",
		rating, note, runID, model, sessionID,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("no run found for run_id=%s model=%s session_id=%s", runID, model, sessionID)
	}
	return nil
}

// GetLeaderboard returns average scores per model for a session, ordered by avg score desc.
func GetLeaderboard(db *sql.DB, sessionID string) ([]types.LeaderboardEntry, error) {
	rows, err := db.Query(`
		SELECT model,
		       AVG(CAST(rating AS REAL)) AS avg_score,
		       COUNT(rating)             AS rating_count
		FROM runs
		WHERE session_id = ? AND rating IS NOT NULL
		GROUP BY model
		ORDER BY avg_score DESC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []types.LeaderboardEntry
	for rows.Next() {
		var e types.LeaderboardEntry
		if err := rows.Scan(&e.Model, &e.AvgScore, &e.RatingCount); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	if entries == nil {
		entries = []types.LeaderboardEntry{}
	}
	return entries, rows.Err()
}

// DeleteSessions removes all runs belonging to the given session IDs.
func DeleteSessions(db *sql.DB, sessionIDs []string) (int64, error) {
	if len(sessionIDs) == 0 {
		return 0, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(sessionIDs)), ",")
	args := make([]interface{}, len(sessionIDs))
	for i, id := range sessionIDs {
		args[i] = id
	}
	res, err := db.Exec(
		fmt.Sprintf("DELETE FROM runs WHERE session_id IN (%s)", placeholders),
		args...,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func countSessions(db *sql.DB) (int, error) {
	var n int
	err := db.QueryRow(
		"SELECT COUNT(DISTINCT session_id) FROM runs WHERE session_id IS NOT NULL",
	).Scan(&n)
	return n, err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func parseTime(s string) time.Time {
	// SQLite stores DATETIME as "2006-01-02 15:04:05"
	t, err := time.Parse("2006-01-02 15:04:05", s)
	if err != nil {
		// Fallback: try with fractional seconds
		t, err = time.Parse("2006-01-02 15:04:05.999999999", s)
		if err != nil {
			return time.Time{}
		}
	}
	// Report as UTC to keep consistent with Python's UTC default.
	return time.Date(t.Year(), t.Month(), t.Day(),
		t.Hour(), t.Minute(), t.Second(),
		t.Nanosecond(), time.UTC)
}

// TotalPages computes ceiling division.
func TotalPages(total, pageSize int) int {
	if pageSize <= 0 {
		return 0
	}
	return int(math.Ceil(float64(total) / float64(pageSize)))
}
