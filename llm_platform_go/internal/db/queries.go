package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"llm_platform_go/internal/types"
)

func InsertRun(db *sql.DB, r *types.RunRow) error {
	_, err := exec(db, `
		INSERT INTO runs
			(run_id, session_id, prompt, system_prompt, image, model, response,
			 latency_ms, input_tokens, output_tokens, total_tokens,
			 cost_usd, success, error, user_id, user_email,
			 task_id, prompt_version, provider, fallback_used, cache_hit, is_test, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.RunID, r.SessionID, r.Prompt, r.SystemPrompt, imagesToColumn(r.Images), r.Model, r.Response,
		r.LatencyMs, r.InputTokens, r.OutputTokens, r.TotalTokens,
		r.CostUSD, boolToInt(r.Success), r.Error, r.UserID, r.UserEmail,
		r.TaskID, r.PromptVersion, r.Provider, boolToInt(r.FallbackUsed), boolToInt(r.CacheHit),
		boolToInt(r.IsTest),
		r.CreatedAt.UTC().Format("2006-01-02 15:04:05"),
	)
	return err
}

// InsertGatewayAttempt persists one model attempt within a run's fallback walk.
// created_at is supplied so a batch of attempts from one run shares a timestamp.
func InsertGatewayAttempt(db *sql.DB, a *types.GatewayAttempt) error {
	_, err := exec(db, `
		INSERT INTO gateway_attempts
			(run_id, task_id, seq, model, provider, outcome, fallback_used,
			 fallback_reason, response, error, http_status, infra_failure, retry_count,
			 latency_ms, input_tokens, output_tokens, total_tokens, cost_usd,
			 is_test, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.RunID, a.TaskID, a.Seq, a.Model, a.Provider, a.Outcome, boolToInt(a.FallbackUsed),
		a.FallbackReason, a.Response, a.Error, a.HTTPStatus, boolToInt(a.InfraFailure), a.RetryCount,
		a.LatencyMs, a.InputTokens, a.OutputTokens, a.TotalTokens, a.CostUSD,
		boolToInt(a.IsTest),
		a.CreatedAt.UTC().Format("2006-01-02 15:04:05"),
	)
	return err
}

// ListGatewayAttempts returns every attempt for one run_id, in walk order.
func ListGatewayAttempts(db *sql.DB, runID string) ([]types.GatewayAttempt, error) {
	rows, err := query(db, `
		SELECT id, run_id, task_id, seq, model, provider, outcome, fallback_used,
		       fallback_reason, response, error, http_status, infra_failure, retry_count,
		       latency_ms, input_tokens, output_tokens, total_tokens, cost_usd,
		       is_test, created_at
		FROM gateway_attempts
		WHERE run_id = ?
		ORDER BY seq ASC, id ASC`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanGatewayAttempts(rows)
}

func scanGatewayAttempts(rows *sql.Rows) ([]types.GatewayAttempt, error) {
	attempts := []types.GatewayAttempt{}
	for rows.Next() {
		var a types.GatewayAttempt
		var taskID, response sql.NullString
		var fallbackInt, infraInt, testInt int
		var createdAtStr string
		if err := rows.Scan(
			&a.ID, &a.RunID, &taskID, &a.Seq, &a.Model, &a.Provider, &a.Outcome, &fallbackInt,
			&a.FallbackReason, &response, &a.Error, &a.HTTPStatus, &infraInt, &a.RetryCount,
			&a.LatencyMs, &a.InputTokens, &a.OutputTokens, &a.TotalTokens, &a.CostUSD,
			&testInt, &createdAtStr,
		); err != nil {
			return nil, err
		}
		a.TaskID = nullStrPtr(taskID)
		a.Response = nullStrPtr(response)
		a.FallbackUsed = fallbackInt == 1
		a.InfraFailure = infraInt == 1
		a.IsTest = testInt == 1
		a.CreatedAt = parseTime(createdAtStr)
		attempts = append(attempts, a)
	}
	return attempts, rows.Err()
}

// imagesToColumn serializes a run's multimodal inputs for the runs.image TEXT
// column: NULL when there are none, otherwise a JSON array of the data URLs /
// image URLs. Storing an array (rather than a bare string) lets a single column
// hold one or many images without a schema change.
func imagesToColumn(imgs []string) any {
	if len(imgs) == 0 {
		return nil
	}
	b, err := json.Marshal(imgs)
	if err != nil {
		return nil
	}
	return string(b)
}

// ParseImagesColumn reads the runs.image column back into a slice. It accepts
// both the JSON-array form written by current code and a bare data URL written
// by older single-image rows, so historical runs render correctly.
func ParseImagesColumn(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if strings.HasPrefix(s, "[") {
		var out []string
		if err := json.Unmarshal([]byte(s), &out); err == nil {
			return out
		}
	}
	return []string{s}
}

// TaskSpendToday returns the UTC-today cost for one task across all callers
// (including Studio test calls — real spend is real spend). Backs the budget gate.
func TaskSpendToday(db *sql.DB, taskID string) (float64, error) {
	var spend float64
	err := queryRow(db, `
		SELECT COALESCE(SUM(cost_usd), 0) FROM runs
		WHERE task_id = ? AND created_at >= `+todayExpr(), taskID).Scan(&spend)
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
	err := queryRow(db, `
		SELECT COUNT(*), COALESCE(SUM(total_tokens),0), COALESCE(SUM(cost_usd),0),
		       COALESCE(AVG(latency_ms),0), COALESCE(AVG(success),0)
		FROM runs
		WHERE task_id = ? AND created_at >= `+daysAgoExpr(days),
		taskID).
		Scan(&totals.Runs, &totals.TotalTokens, &totals.CostUSD,
			&totals.AvgLatencyMs, &totals.SuccessRate)
	if err != nil {
		return nil, nil, err
	}

	rows, err := query(db, `
		SELECT substr(created_at,1,10) AS day,
		       COALESCE(SUM(cost_usd),0), COALESCE(SUM(total_tokens),0), COUNT(*)
		FROM runs
		WHERE task_id = ? AND created_at >= `+daysAgoExpr(days)+`
		GROUP BY day ORDER BY day ASC`,
		taskID)
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
	rows, err := query(db, `
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
		var dbID int
		var successInt, fallbackInt, cacheInt int
		var createdAtStr string
		err := rows.Scan(
			&dbID, &r.RunID, &r.SessionID, &r.Prompt, &r.SystemPrompt,
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

// InsertHealthEvent persists one (task, model) circuit transition for later
// observation. created_at is supplied so it matches the in-memory event time.
func InsertHealthEvent(db *sql.DB, e *types.HealthEvent) error {
	_, err := exec(db, `
		INSERT INTO model_health_events
			(task_id, model, provider, event, reason, consecutive_failures, cooldown_ms, state, created_at)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		e.TaskID, e.Model, e.Provider, e.Event, e.Reason,
		e.ConsecutiveFailures, e.CooldownMs, e.State,
		e.CreatedAt.UTC().Format("2006-01-02 15:04:05"),
	)
	return err
}

// ListHealthEvents returns one page of health events, newest first, optionally
// filtered by task and/or model, with their total count for pagination.
func ListHealthEvents(db *sql.DB, taskID, model string, page, pageSize int) ([]types.HealthEvent, int, error) {
	var clauses []string
	var args []any
	if taskID != "" {
		clauses = append(clauses, "task_id = ?")
		args = append(args, taskID)
	}
	if model != "" {
		clauses = append(clauses, "model = ?")
		args = append(args, model)
	}
	whereSQL := ""
	if len(clauses) > 0 {
		whereSQL = " WHERE " + strings.Join(clauses, " AND ")
	}

	var total int
	if err := queryRow(db, "SELECT COUNT(*) FROM model_health_events"+whereSQL, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	offset := (page - 1) * pageSize
	qArgs := append(append([]any{}, args...), pageSize, offset)
	rows, err := query(db, `
		SELECT id, task_id, model, provider, event, reason,
		       consecutive_failures, cooldown_ms, state, created_at
		FROM model_health_events`+whereSQL+`
		ORDER BY id DESC
		LIMIT ? OFFSET ?`, qArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	events := []types.HealthEvent{}
	for rows.Next() {
		var e types.HealthEvent
		var createdAtStr string
		if err := rows.Scan(&e.ID, &e.TaskID, &e.Model, &e.Provider, &e.Event, &e.Reason,
			&e.ConsecutiveFailures, &e.CooldownMs, &e.State, &createdAtStr); err != nil {
			return nil, 0, err
		}
		e.CreatedAt = parseTime(createdAtStr)
		events = append(events, e)
	}
	return events, total, rows.Err()
}

// RunFilter narrows the admin prompt-history list. Zero-value fields are
// ignored, so an empty filter lists every run.
type RunFilter struct {
	TaskID    string // exact match
	Model     string // exact match
	UserEmail string // case-insensitive substring
	Query     string // case-insensitive substring of the prompt text
	Success   *bool  // nil = either; else filter on success
	IsTest    *bool  // nil = both production and test; else filter on is_test
	// MaxID anchors the list to a point-in-time snapshot: only runs with
	// id <= MaxID are considered, so rows inserted after the anchor never shift
	// the pages while the user is browsing. 0 = no anchor (every run).
	MaxID int
}

// where builds the SQL WHERE clause + args shared by ListAllRuns and its count.
func (f RunFilter) where() (string, []any) {
	var clauses []string
	var args []any
	if f.TaskID != "" {
		clauses = append(clauses, "task_id = ?")
		args = append(args, f.TaskID)
	}
	if f.Model != "" {
		clauses = append(clauses, "model = ?")
		args = append(args, f.Model)
	}
	if f.UserEmail != "" {
		clauses = append(clauses, ciLike("user_email"))
		args = append(args, "%"+f.UserEmail+"%")
	}
	if f.Query != "" {
		clauses = append(clauses, ciLike("prompt"))
		args = append(args, "%"+f.Query+"%")
	}
	if f.Success != nil {
		clauses = append(clauses, "success = ?")
		args = append(args, boolToInt(*f.Success))
	}
	if f.IsTest != nil {
		clauses = append(clauses, "is_test = ?")
		args = append(args, boolToInt(*f.IsTest))
	}
	if f.MaxID > 0 {
		clauses = append(clauses, "id <= ?")
		args = append(args, f.MaxID)
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

// ListAllRuns returns one page of runs across ALL users (admin-only), newest
// first, with their total count for pagination. Rows are lightweight: the
// prompt is truncated to a preview and images are reduced to a count, so a page
// stays small no matter how large the underlying prompts or base64 images are.
//
// The list is anchored to a point-in-time snapshot via f.MaxID: only runs with
// id <= MaxID are returned, so rows inserted while the user pages through the
// history never shift the slices under them. When f.MaxID is 0 the current
// MAX(id) is resolved as the anchor; either way the anchor actually used is
// returned so the caller can pin subsequent page requests to it.
func ListAllRuns(database *sql.DB, f RunFilter, page, pageSize int) ([]types.RunListItem, int, int, error) {
	if f.MaxID <= 0 {
		// No anchor supplied — pin to the newest row right now. This is the
		// "current time" snapshot; every page the caller requests against this
		// anchor sees the same fixed set of rows.
		var maxID int
		if err := queryRow(database, "SELECT COALESCE(MAX(id), 0) FROM runs").Scan(&maxID); err != nil {
			return nil, 0, 0, err
		}
		f.MaxID = maxID
	}

	whereSQL, whereArgs := f.where()

	var total int
	if err := queryRow(database, "SELECT COUNT(*) FROM runs"+whereSQL, whereArgs...).Scan(&total); err != nil {
		return nil, 0, 0, err
	}

	offset := (page - 1) * pageSize
	args := append(append([]any{}, whereArgs...), pageSize, offset)
	// id DESC ≈ newest-first (id is the autoincrement insert order) and uses the
	// primary key, so paging stays cheap as the table grows.
	rows, err := query(database, `
		SELECT id, run_id, task_id, user_email, model, provider,
		       substr(prompt, 1, 200),
		       `+imageCountExpr()+` AS image_count,
		       success, cache_hit, fallback_used, is_test,
		       latency_ms, total_tokens, cost_usd, created_at
		FROM runs`+whereSQL+`
		ORDER BY id DESC
		LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, 0, 0, err
	}
	defer rows.Close()

	items := []types.RunListItem{}
	for rows.Next() {
		var it types.RunListItem
		var imageCount int
		var successInt, cacheInt, fallbackInt, testInt int
		var createdAtStr string
		if err := rows.Scan(
			&it.ID, &it.RunID, &it.TaskID, &it.UserEmail, &it.Model, &it.Provider,
			&it.PromptPreview,
			&imageCount,
			&successInt, &cacheInt, &fallbackInt, &testInt,
			&it.LatencyMs, &it.TotalTokens, &it.CostUSD, &createdAtStr,
		); err != nil {
			return nil, 0, 0, err
		}
		it.ImageCount = imageCount
		it.HasImage = imageCount > 0
		it.Success = successInt == 1
		it.CacheHit = cacheInt == 1
		it.FallbackUsed = fallbackInt == 1
		it.IsTest = testInt == 1
		it.CreatedAt = parseTime(createdAtStr)
		items = append(items, it)
	}
	return items, total, f.MaxID, rows.Err()
}

// GetRunDetail returns the full record for one run_id across all users
// (admin-only): the shared prompt/inputs plus one result per model row. Returns
// (nil, nil) when the run_id is unknown.
func GetRunDetail(database *sql.DB, runID string) (*types.RunDetailResponse, error) {
	rows, err := query(database, `
		SELECT prompt, system_prompt, image, model, response,
		       latency_ms, input_tokens, output_tokens, total_tokens,
		       cost_usd, success, error, user_id, user_email,
		       task_id, prompt_version, provider, fallback_used, cache_hit, is_test, created_at
		FROM runs
		WHERE run_id = ?
		ORDER BY id ASC`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var detail *types.RunDetailResponse
	for rows.Next() {
		var (
			prompt, model                               string
			systemPrompt, image, response, errMsg       sql.NullString
			userID, userEmail, taskID, provider         sql.NullString
			latency, inTok, outTok, totalTok, promptVer int
			cost                                        float64
			successInt, fallbackInt, cacheInt, testInt  int
			createdAtStr                                string
		)
		if err := rows.Scan(
			&prompt, &systemPrompt, &image, &model, &response,
			&latency, &inTok, &outTok, &totalTok,
			&cost, &successInt, &errMsg, &userID, &userEmail,
			&taskID, &promptVer, &provider, &fallbackInt, &cacheInt, &testInt, &createdAtStr,
		); err != nil {
			return nil, err
		}
		if detail == nil {
			detail = &types.RunDetailResponse{
				RunID:         runID,
				TaskID:        nullStrPtr(taskID),
				UserID:        nullStrPtr(userID),
				UserEmail:     nullStrPtr(userEmail),
				PromptVersion: promptVer,
				Prompt:        prompt,
				SystemPrompt:  nullStrPtr(systemPrompt),
				Images:        []string{},
				IsTest:        testInt == 1,
				CreatedAt:     parseTime(createdAtStr),
				Results:       []types.RunDetailResult{},
			}
			if image.Valid {
				detail.Images = ParseImagesColumn(image.String)
			}
		}
		detail.Results = append(detail.Results, types.RunDetailResult{
			Model:        model,
			Provider:     nullStrPtr(provider),
			Response:     nullStrPtr(response),
			Success:      successInt == 1,
			Error:        nullStrPtr(errMsg),
			LatencyMs:    latency,
			InputTokens:  inTok,
			OutputTokens: outTok,
			TotalTokens:  totalTok,
			CostUSD:      cost,
			CacheHit:     cacheInt == 1,
			FallbackUsed: fallbackInt == 1,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if detail == nil {
		return nil, nil // unknown run_id
	}

	// Attach the full gateway trace: every model the fallback walk touched for
	// this run (predictions only; playground /run rows have none).
	attempts, err := ListGatewayAttempts(database, runID)
	if err != nil {
		return nil, err
	}
	detail.Attempts = attempts
	return detail, nil
}

// DistinctRunModels lists the distinct models that appear in the runs table,
// alphabetically — used to populate the admin history's model filter.
func DistinctRunModels(database *sql.DB) ([]string, error) {
	rows, err := query(database, `SELECT DISTINCT model FROM runs ORDER BY model ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	models := []string{}
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		models = append(models, m)
	}
	return models, rows.Err()
}

func nullStrPtr(ns sql.NullString) *string {
	if !ns.Valid {
		return nil
	}
	s := ns.String
	return &s
}

// UpsertFeedback records (or updates) a star rating for one model response.
// One rating per (run_id, model, user); re-rating overwrites the prior value.
func UpsertFeedback(db *sql.DB, runID, model, userID string, rating int) error {
	_, err := exec(db, `
		INSERT INTO feedback (run_id, model, user_id, rating, created_at)
		VALUES (?,?,?,?, `+nowExpr()+`)
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
	trows, err := query(db, `
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
	rows, err := query(db, `
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
	drows, err := query(db, `
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
	rows, err := query(db, `
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
	rows, err := query(db, `
		SELECT id, run_id, session_id, prompt, system_prompt, image, model, response,
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
		var dbID int
		var successInt int
		var createdAtStr string
		var image sql.NullString
		err := rows.Scan(
			&dbID, &r.RunID, &r.SessionID, &r.Prompt, &r.SystemPrompt,
			&image, &r.Model, &r.Response,
			&r.LatencyMs, &r.InputTokens, &r.OutputTokens, &r.TotalTokens,
			&r.CostUSD, &successInt, &r.Error, &r.UserID, &r.UserEmail, &createdAtStr,
		)
		if err != nil {
			return nil, err
		}
		r.Success = successInt == 1
		r.CreatedAt = parseTime(createdAtStr)
		if image.Valid {
			r.Images = ParseImagesColumn(image.String)
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// GetLeaderboard returns the average manual rating per model within a session,
// ordered by average score desc. Ratings live in the feedback table (one row
// per run_id+model+user, upserted on re-rate), so the current value always
// reflects the latest rating.
//
// A playground run stores one runs row PER model under a single run_id, so a
// join on run_id alone would match every model's row and multiply each rating
// by the number of models in that run (inflating the count and weighting the
// average). We instead select the session's run_ids in a subquery and count
// each feedback row exactly once.
func GetLeaderboard(db *sql.DB, userID, sessionID string) ([]types.LeaderboardEntry, error) {
	rows, err := query(db, `
		SELECT f.model,
		       AVG(CAST(f.rating AS REAL)) AS avg_score,
		       COUNT(*)                    AS rating_count
		FROM feedback f
		WHERE f.run_id IN (
		        SELECT run_id FROM runs WHERE session_id = ? AND user_id = ?
		      )
		GROUP BY f.model
		ORDER BY avg_score DESC`, sessionID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	entries := []types.LeaderboardEntry{}
	for rows.Next() {
		var e types.LeaderboardEntry
		if err := rows.Scan(&e.Model, &e.AvgScore, &e.RatingCount); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
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
	res, err := exec(db,
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
	err := queryRow(db,
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
