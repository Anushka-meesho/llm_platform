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
			 cost_usd, success, error, user_id, user_email,
			 task_id, prompt_version, provider, fallback_used, cache_hit, is_test, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.RunID, r.SessionID, r.Prompt, r.SystemPrompt, r.Model, r.Response,
		r.LatencyMs, r.InputTokens, r.OutputTokens, r.TotalTokens,
		r.CostUSD, boolToInt(r.Success), r.Error, r.UserID, r.UserEmail,
		r.TaskID, r.PromptVersion, r.Provider, boolToInt(r.FallbackUsed), boolToInt(r.CacheHit),
		boolToInt(r.IsTest),
		r.CreatedAt.UTC().Format("2006-01-02 15:04:05"),
	)
	return err
}

// TaskSpendToday returns the UTC-today cost for one task across all callers
// (including Studio test calls — real spend is real spend). Backs the budget gate.
func TaskSpendToday(db *sql.DB, taskID string) (float64, error) {
	var spend float64
	err := db.QueryRow(`
		SELECT COALESCE(SUM(cost_usd), 0) FROM runs
		WHERE task_id = ? AND created_at >= date('now')`, taskID).Scan(&spend)
	return spend, err
}

// TaskDailyStats returns per-day aggregates for one task over the last N days
// (all callers), newest last. Backs GET /v1/tasks/{id}/stats.
func TaskDailyStats(db *sql.DB, taskID string, days int) ([]types.DailyPoint, *types.TaskStats, error) {
	if days <= 0 || days > 365 {
		days = 30
	}

	var totals types.TaskStats
	totals.TaskID = taskID
	err := db.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(total_tokens),0), COALESCE(SUM(cost_usd),0),
		       COALESCE(AVG(latency_ms),0), COALESCE(AVG(success),0)
		FROM runs
		WHERE task_id = ? AND created_at >= date('now', ?)`,
		taskID, fmt.Sprintf("-%d days", days)).
		Scan(&totals.Runs, &totals.TotalTokens, &totals.CostUSD,
			&totals.AvgLatencyMs, &totals.SuccessRate)
	if err != nil {
		return nil, nil, err
	}

	rows, err := db.Query(`
		SELECT substr(created_at,1,10) AS day,
		       COALESCE(SUM(cost_usd),0), COALESCE(SUM(total_tokens),0), COUNT(*)
		FROM runs
		WHERE task_id = ? AND created_at >= date('now', ?)
		GROUP BY day ORDER BY day ASC`,
		taskID, fmt.Sprintf("-%d days", days))
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	daily := []types.DailyPoint{}
	for rows.Next() {
		var p types.DailyPoint
		if err := rows.Scan(&p.Date, &p.CostUSD, &p.TotalTokens, &p.Runs); err != nil {
			return nil, nil, err
		}
		daily = append(daily, p)
	}
	return daily, &totals, rows.Err()
}

// GetRunByID returns all rows for one run_id (a /run produces one row per
// model; a task predict produces exactly one). Scoped to the user.
func GetRunByID(db *sql.DB, userID, runID string) ([]types.RunRow, error) {
	rows, err := db.Query(`
		SELECT id, run_id, session_id, prompt, system_prompt, model, response,
		       latency_ms, input_tokens, output_tokens, total_tokens,
		       cost_usd, success, error, user_id, user_email,
		       task_id, prompt_version, provider, fallback_used, cache_hit, created_at
		FROM runs
		WHERE run_id = ? AND user_id = ?
		ORDER BY id ASC`, runID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []types.RunRow
	for rows.Next() {
		var r types.RunRow
		var successInt, fallbackInt, cacheInt int
		var createdAtStr string
		err := rows.Scan(
			&r.ID, &r.RunID, &r.SessionID, &r.Prompt, &r.SystemPrompt,
			&r.Model, &r.Response,
			&r.LatencyMs, &r.InputTokens, &r.OutputTokens, &r.TotalTokens,
			&r.CostUSD, &successInt, &r.Error, &r.UserID, &r.UserEmail,
			&r.TaskID, &r.PromptVersion, &r.Provider, &fallbackInt, &cacheInt, &createdAtStr,
		)
		if err != nil {
			return nil, err
		}
		r.Success = successInt == 1
		r.FallbackUsed = fallbackInt == 1
		r.CacheHit = cacheInt == 1
		r.CreatedAt = parseTime(createdAtStr)
		result = append(result, r)
	}
	return result, rows.Err()
}

// UpsertFeedback records (or updates) a star rating for one model response.
// One rating per (run_id, model, user); re-rating overwrites the prior value.
func UpsertFeedback(db *sql.DB, runID, model, userID string, rating int) error {
	_, err := db.Exec(`
		INSERT INTO feedback (run_id, model, user_id, rating, created_at)
		VALUES (?,?,?,?, datetime('now'))
		ON CONFLICT(run_id, model, user_id)
		DO UPDATE SET rating = excluded.rating, created_at = excluded.created_at`,
		runID, model, userID, rating,
	)
	return err
}

// DashboardStats aggregates a single user's runs into totals, a per-model
// breakdown (including average star rating), and a daily time series.
func DashboardStats(db *sql.DB, userID string) (*types.DashboardResponse, error) {
	resp := &types.DashboardResponse{
		ByTask:  []types.TaskStats{},
		ByModel: []types.ModelStats{},
		Daily:   []types.DailyPoint{},
	}

	// Per-task breakdown — the platform's primary cost dimension.
	trows, err := db.Query(`
		SELECT
			COALESCE(task_id, 'untagged')   AS task,
			COUNT(*)                        AS runs,
			COALESCE(SUM(total_tokens),0)   AS total_tokens,
			COALESCE(SUM(cost_usd),0)       AS cost_usd,
			COALESCE(AVG(latency_ms),0)     AS avg_latency,
			COALESCE(AVG(success),0)        AS success_rate
		FROM runs
		WHERE user_id = ?
		GROUP BY task
		ORDER BY cost_usd DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer trows.Close()
	for trows.Next() {
		var t types.TaskStats
		if err := trows.Scan(&t.TaskID, &t.Runs, &t.TotalTokens, &t.CostUSD,
			&t.AvgLatencyMs, &t.SuccessRate); err != nil {
			return nil, err
		}
		resp.ByTask = append(resp.ByTask, t)
	}
	if err := trows.Err(); err != nil {
		return nil, err
	}

	// Per-model breakdown. Ratings are joined from a pre-aggregated subquery so
	// the LEFT JOIN can't fan out and inflate run/token/cost sums.
	rows, err := db.Query(`
		SELECT
			r.model,
			COUNT(*)                        AS runs,
			COALESCE(SUM(r.total_tokens),0) AS total_tokens,
			COALESCE(SUM(r.cost_usd),0)     AS cost_usd,
			COALESCE(AVG(r.latency_ms),0)   AS avg_latency,
			COALESCE(f.avg_rating,0)        AS avg_rating,
			COALESCE(f.rating_count,0)      AS rating_count
		FROM runs r
		LEFT JOIN (
			SELECT model, AVG(rating) AS avg_rating, COUNT(*) AS rating_count
			FROM feedback WHERE user_id = ? GROUP BY model
		) f ON f.model = r.model
		WHERE r.user_id = ?
		GROUP BY r.model
		ORDER BY cost_usd DESC`, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var m types.ModelStats
		if err := rows.Scan(&m.Model, &m.Runs, &m.TotalTokens, &m.CostUSD,
			&m.AvgLatencyMs, &m.AvgRating, &m.RatingCount); err != nil {
			return nil, err
		}
		resp.ByModel = append(resp.ByModel, m)
		resp.TotalRuns += m.Runs
		resp.TotalTokens += m.TotalTokens
		resp.TotalCostUSD += m.CostUSD
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Daily time series.
	drows, err := db.Query(`
		SELECT
			substr(created_at, 1, 10)     AS day,
			COALESCE(SUM(cost_usd),0)     AS cost_usd,
			COALESCE(SUM(total_tokens),0) AS total_tokens,
			COUNT(*)                      AS runs
		FROM runs
		WHERE user_id = ?
		GROUP BY day
		ORDER BY day ASC`, userID)
	if err != nil {
		return nil, err
	}
	defer drows.Close()

	for drows.Next() {
		var p types.DailyPoint
		if err := drows.Scan(&p.Date, &p.CostUSD, &p.TotalTokens, &p.Runs); err != nil {
			return nil, err
		}
		resp.Daily = append(resp.Daily, p)
	}
	return resp, drows.Err()
}

// ListSessions returns paginated session summaries for one user, ordered by
// most-recently-updated.
func ListSessions(db *sql.DB, userID string, page, pageSize int) ([]types.SessionSummary, int, error) {
	total, err := countSessions(db, userID)
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
		WHERE session_id IS NOT NULL AND user_id = ?
		GROUP BY session_id
		ORDER BY MAX(created_at) DESC
		LIMIT ? OFFSET ?`, userID, pageSize, offset)
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

// GetSession returns all runs for one user's session in chronological order.
func GetSession(db *sql.DB, userID, sessionID string) ([]types.RunRow, error) {
	rows, err := db.Query(`
		SELECT id, run_id, session_id, prompt, system_prompt, model, response,
		       latency_ms, input_tokens, output_tokens, total_tokens,
		       cost_usd, success, error, user_id, user_email, created_at
		FROM runs
		WHERE session_id = ? AND user_id = ?
		ORDER BY created_at ASC`, sessionID, userID)
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
			&r.CostUSD, &successInt, &r.Error, &r.UserID, &r.UserEmail, &createdAtStr,
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

// DeleteSessions removes the given sessions, scoped to one user so a user can
// only delete their own sessions.
func DeleteSessions(db *sql.DB, userID string, sessionIDs []string) (int64, error) {
	if len(sessionIDs) == 0 {
		return 0, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(sessionIDs)), ",")
	args := make([]interface{}, 0, len(sessionIDs)+1)
	for _, id := range sessionIDs {
		args = append(args, id)
	}
	args = append(args, userID)
	res, err := db.Exec(
		fmt.Sprintf("DELETE FROM runs WHERE session_id IN (%s) AND user_id = ?", placeholders),
		args...,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func countSessions(db *sql.DB, userID string) (int, error) {
	var n int
	err := db.QueryRow(
		"SELECT COUNT(DISTINCT session_id) FROM runs WHERE session_id IS NOT NULL AND user_id = ?",
		userID,
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
	// We store DATETIME as "2006-01-02 15:04:05", but the modernc.org/sqlite
	// driver round-trips DATETIME columns through time.Time and re-emits them as
	// RFC3339 when scanned into a string — so accept both forms or timestamps
	// read back as the zero time.
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
	}
	var t time.Time
	var err error
	for _, layout := range layouts {
		if t, err = time.Parse(layout, s); err == nil {
			break
		}
	}
	if err != nil {
		return time.Time{}
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
