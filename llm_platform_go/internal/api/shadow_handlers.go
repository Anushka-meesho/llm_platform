package api

import (
	"encoding/json"
	"net/http"
	"reflect"
	"sort"
	"sync"
	"time"

	"llm_platform_go/internal/auth"
	"llm_platform_go/internal/tasks"
)

// Shadow harness (design doc §4.3 Phase 1): run labelled items through the
// platform and measure field-level agreement + latency, producing the numbers
// the success metric is judged on ("accuracy within 2%, latency within 200ms").

const (
	shadowMaxItems    = 200
	shadowConcurrency = 6
	shadowMaxMismatch = 20 // mismatch examples kept in the report
)

type shadowItem struct {
	Inputs         json.RawMessage `json:"inputs"`
	ExpectedOutput json.RawMessage `json:"expected_output"`
}

type shadowMismatch struct {
	Item     int    `json:"item"`
	Field    string `json:"field"`
	Expected any    `json:"expected"`
	Got      any    `json:"got"`
}

type shadowReport struct {
	ID                int              `json:"id"`
	TaskID            string           `json:"task_id"`
	Items             int              `json:"items"`
	MatchRate         float64          `json:"match_rate"` // matched fields / expected fields
	ItemsFullyMatched int              `json:"items_fully_matched"`
	AvgLatencyMs      float64          `json:"avg_latency_ms"`
	P50LatencyMs      int              `json:"p50_latency_ms"`
	P95LatencyMs      int              `json:"p95_latency_ms"`
	TotalCostUSD      float64          `json:"total_cost_usd"`
	Mismatches        []shadowMismatch `json:"mismatches"`
	CreatedAt         time.Time        `json:"created_at"`
}

// POST /v1/shadow/compare {task_id, items: [{inputs, expected_output}]}
func (h *Handler) ShadowCompare(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}

	var req struct {
		TaskID string       `json:"task_id"`
		Items  []shadowItem `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, r, Unprocessable(CodeInvalidBody, "invalid request body: %s", err.Error()))
		return
	}
	if req.TaskID == "" || len(req.Items) == 0 {
		writeErr(w, r, Unprocessable(CodeValidationFailed, "task_id and items are required"))
		return
	}
	if len(req.Items) > shadowMaxItems {
		writeErr(w, r, Unprocessable(CodeValidationFailed, "too many items (max %d per request)", shadowMaxItems))
		return
	}

	task, err := h.Tasks.Get(req.TaskID)
	if err != nil {
		writeErr(w, r, NotFound(CodeTaskNotFound, "task not found"))
		return
	}

	// Run items concurrently (bounded), preserving order.
	results := make([]shadowItemResult, len(req.Items))
	sem := make(chan struct{}, shadowConcurrency)
	var wg sync.WaitGroup

	for i, item := range req.Items {
		wg.Add(1)
		go func(idx int, it shadowItem) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[idx] = h.runShadowItem(r, task, user, idx, it)
		}(i, item)
	}
	wg.Wait()

	// Aggregate.
	report := shadowReport{TaskID: task.ID, Items: len(req.Items), CreatedAt: time.Now().UTC()}
	var latencies []int
	var matchedFields, totalFields int
	for _, res := range results {
		latencies = append(latencies, res.latencyMs)
		report.TotalCostUSD += res.costUSD
		matchedFields += res.matched
		totalFields += res.total
		if res.total > 0 && res.matched == res.total {
			report.ItemsFullyMatched++
		}
		if len(report.Mismatches) < shadowMaxMismatch {
			report.Mismatches = append(report.Mismatches, res.mismatches...)
		}
	}
	if len(report.Mismatches) > shadowMaxMismatch {
		report.Mismatches = report.Mismatches[:shadowMaxMismatch]
	}
	if report.Mismatches == nil {
		report.Mismatches = []shadowMismatch{}
	}
	if totalFields > 0 {
		report.MatchRate = float64(matchedFields) / float64(totalFields)
	}
	sort.Ints(latencies)
	var sum int
	for _, l := range latencies {
		sum += l
	}
	if len(latencies) > 0 {
		report.AvgLatencyMs = float64(sum) / float64(len(latencies))
		report.P50LatencyMs = latencies[len(latencies)/2]
		report.P95LatencyMs = latencies[(len(latencies)*95)/100]
	}

	// Persist.
	details, _ := json.Marshal(map[string]any{
		"items_fully_matched": report.ItemsFullyMatched,
		"p50_latency_ms":      report.P50LatencyMs,
		"mismatches":          report.Mismatches,
	})
	res, err := h.DB.Exec(`
		INSERT INTO shadow_reports
			(task_id, created_by, items, match_rate, avg_latency_ms, p95_latency_ms,
			 total_cost_usd, details, created_at)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		task.ID, user.Subject, report.Items, report.MatchRate, report.AvgLatencyMs,
		float64(report.P95LatencyMs), report.TotalCostUSD, string(details),
		report.CreatedAt.Format("2006-01-02 15:04:05"))
	if err != nil {
		writeErr(w, r, Internal(CodeInternal, "persist shadow report").WithCause(err))
		return
	}
	if id, err := res.LastInsertId(); err == nil {
		report.ID = int(id)
	}

	writeJSON(w, http.StatusOK, report)
}

// shadowItemResult is the per-item score.
type shadowItemResult struct {
	latencyMs  int
	costUSD    float64
	matched    int
	total      int
	mismatches []shadowMismatch
}

// runShadowItem executes one labelled item and scores it field-by-field.
func (h *Handler) runShadowItem(r *http.Request, task *tasks.Task, user *auth.User, idx int, it shadowItem) (out shadowItemResult) {
	expected := flattenJSON("", it.ExpectedOutput)
	out.total = len(expected)

	outcome, herr := h.executePrediction(r.Context(), task, it.Inputs, user,
		predictOptions{isTest: true}) // shadow traffic is not production traffic
	if herr != nil || !outcome.Result.Success || outcome.Output == nil {
		reason := "prediction failed"
		if herr != nil {
			reason = herr.Message
		} else if outcome.Result.Error != nil {
			reason = *outcome.Result.Error
		} else if outcome.Output == nil {
			reason = "output failed schema validation"
		}
		if herr == nil && outcome != nil {
			out.latencyMs = outcome.Result.LatencyMs
			out.costUSD = outcome.Result.CostUSD
		}
		out.mismatches = append(out.mismatches, shadowMismatch{
			Item: idx, Field: "(prediction)", Expected: "success", Got: reason,
		})
		return out
	}

	out.latencyMs = outcome.Result.LatencyMs
	out.costUSD = outcome.Result.CostUSD

	actual := flattenJSON("", outcome.Output)
	for field, want := range expected {
		got, present := actual[field]
		if present && reflect.DeepEqual(want, got) {
			out.matched++
			continue
		}
		if !present {
			got = nil
		}
		out.mismatches = append(out.mismatches, shadowMismatch{
			Item: idx, Field: field, Expected: want, Got: got,
		})
	}
	return out
}

// flattenJSON turns nested JSON into dotted leaf paths → values, so
// {"attributes":{"color":"red"},"confidence":0.9} compares per-leaf
// ("attributes.color", "confidence"). The *expected* document drives which
// fields are scored.
func flattenJSON(prefix string, raw json.RawMessage) map[string]any {
	out := map[string]any{}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return out
	}
	flattenValue(prefix, v, out)
	return out
}

func flattenValue(prefix string, v any, out map[string]any) {
	if m, ok := v.(map[string]any); ok {
		for k, child := range m {
			key := k
			if prefix != "" {
				key = prefix + "." + k
			}
			flattenValue(key, child, out)
		}
		return
	}
	if prefix == "" {
		prefix = "(root)"
	}
	out[prefix] = v
}

// GET /v1/shadow/reports?task_id=
func (h *Handler) ListShadowReports(w http.ResponseWriter, r *http.Request) {
	taskID := r.URL.Query().Get("task_id")

	query := `SELECT id, task_id, items, match_rate, avg_latency_ms, p95_latency_ms,
	                 total_cost_usd, details, created_at
	          FROM shadow_reports`
	args := []any{}
	if taskID != "" {
		query += " WHERE task_id = ?"
		args = append(args, taskID)
	}
	query += " ORDER BY id DESC LIMIT 50"

	rows, err := h.DB.Query(query, args...)
	if err != nil {
		writeErr(w, r, Internal(CodeDBError, "list shadow reports").WithCause(err))
		return
	}
	defer rows.Close()

	reports := []map[string]any{}
	for rows.Next() {
		var id, items int
		var tID, detailsStr, createdAt string
		var matchRate, avgLat, p95Lat, cost float64
		if err := rows.Scan(&id, &tID, &items, &matchRate, &avgLat, &p95Lat, &cost,
			&detailsStr, &createdAt); err != nil {
			writeErr(w, r, Internal(CodeInternal, "scan rows").WithCause(err))
			return
		}
		var details map[string]any
		_ = json.Unmarshal([]byte(detailsStr), &details)
		reports = append(reports, map[string]any{
			"id": id, "task_id": tID, "items": items, "match_rate": matchRate,
			"avg_latency_ms": avgLat, "p95_latency_ms": p95Lat,
			"total_cost_usd": cost, "details": details, "created_at": createdAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"reports": reports})
}
