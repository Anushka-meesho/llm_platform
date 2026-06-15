package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"llm_platform_go/internal/db"
	"llm_platform_go/internal/tasks"
	"llm_platform_go/internal/types"

	"github.com/go-chi/chi/v5"
)

// ── Task registry CRUD ───────────────────────────────────────────────────────

// POST /v1/tasks
func (h *Handler) CreateTask(w http.ResponseWriter, r *http.Request) {
	var t tasks.Task
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid request body: "+err.Error())
		return
	}
	if err := h.Tasks.Create(&t); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

// GET /v1/tasks
func (h *Handler) ListTasks(w http.ResponseWriter, r *http.Request) {
	list, err := h.Tasks.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": list})
}

// GET /v1/tasks/{task_id}
func (h *Handler) GetTask(w http.ResponseWriter, r *http.Request) {
	t, err := h.Tasks.Get(chi.URLParam(r, "task_id"))
	if errors.Is(err, tasks.ErrNotFound) {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// PUT /v1/tasks/{task_id} — merge semantics: fields present in the body
// overwrite, absent fields keep their current values (so a partial update
// can't accidentally deactivate a task or wipe its schemas).
func (h *Handler) UpdateTask(w http.ResponseWriter, r *http.Request) {
	existing, err := h.Tasks.Get(chi.URLParam(r, "task_id"))
	if errors.Is(err, tasks.ErrNotFound) {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error: "+err.Error())
		return
	}

	t := *existing // start from current state; body overwrites what it carries
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid request body: "+err.Error())
		return
	}
	t.ID = existing.ID
	if err := h.Tasks.Update(&t); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// ── Prediction ───────────────────────────────────────────────────────────────

type predictRequest struct {
	Inputs json.RawMessage `json:"inputs"`
}

type predictUsage struct {
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	TotalTokens  int     `json:"total_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

type predictResponse struct {
	TaskRunID     string          `json:"task_run_id"`
	TaskID        string          `json:"task_id"`
	PromptVersion int             `json:"prompt_version"`
	Model         string          `json:"model"`
	Provider      string          `json:"provider"`
	Output        json.RawMessage `json:"output"`       // parsed JSON when output schema validates; null otherwise
	OutputValid   *bool           `json:"output_valid"` // null when task has no output schema
	RawResponse   *string         `json:"raw_response"`
	Error         *string         `json:"error"`
	FallbackUsed  bool            `json:"fallback_used"`
	Cached        bool            `json:"cached"` // served from the prediction cache (zero cost)
	Usage         predictUsage    `json:"usage"`
	LatencyMs     int             `json:"latency_ms"`
}

// POST /v1/tasks/{task_id}/predict — the platform's core endpoint:
// resolve config → budget gate → execute (validate/render/call/validate) →
// degraded signaling → respond.
func (h *Handler) Predict(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}

	task, err := h.Tasks.Get(chi.URLParam(r, "task_id"))
	if errors.Is(err, tasks.ErrNotFound) {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error: "+err.Error())
		return
	}
	if !task.Active {
		writeError(w, http.StatusConflict, "task is inactive")
		return
	}

	// Budget gate: per-task daily spend cap. 0 = budget-exempt. Spend comes
	// from the in-memory cache (DB refresh ≤ every 5s), not a per-request SUM.
	if task.DailyBudgetUSD > 0 {
		spend, err := h.currentSpend(task.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "budget check failed: "+err.Error())
			return
		}
		if spend >= task.DailyBudgetUSD {
			w.Header().Set("Retry-After", strconv.Itoa(secondsToUTCMidnight()))
			writeError(w, http.StatusTooManyRequests,
				fmt.Sprintf("daily budget exhausted ($%.4f of $%.2f)", spend, task.DailyBudgetUSD))
			return
		}
		if spend >= 0.8*task.DailyBudgetUSD {
			log.Printf("budget warning: task %s at %.0f%% of daily budget ($%.4f / $%.2f)",
				task.ID, 100*spend/task.DailyBudgetUSD, spend, task.DailyBudgetUSD)
		}
	}

	var req predictRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid request body: "+err.Error())
		return
	}

	outcome, herr := h.executePrediction(r.Context(), task, req.Inputs, user, predictOptions{useCache: true})
	if herr != nil {
		writeError(w, herr.status, herr.detail)
		return
	}

	// Degraded contract (design doc §6): fallback served it, or the chain failed.
	if outcome.Result.Degraded {
		w.Header().Set("X-Platform-Degraded", "true")
	}

	status := http.StatusOK
	if !outcome.Result.Success {
		status = http.StatusBadGateway // upstream model failure
	}
	writeJSON(w, status, shapePredictResponse(task, outcome))
}

// secondsToUTCMidnight returns the Retry-After value for budget 429s.
func secondsToUTCMidnight() int {
	now := time.Now().UTC()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).Add(24 * time.Hour)
	return int(time.Until(midnight).Seconds()) + 1
}

// GET /v1/tasks/runs/{run_id} — poll endpoint (design doc §4). Sync-only for
// now; becomes the async-result fetch in Phase 2.
func (h *Handler) GetTaskRun(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	rows, err := db.GetRunByID(h.DB, user.Subject, chi.URLParam(r, "run_id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error: "+err.Error())
		return
	}
	if len(rows) == 0 {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}

	results := make([]types.ModelResultResponse, 0, len(rows))
	for _, row := range rows {
		results = append(results, types.ModelResultResponse{
			Model:        row.Model,
			Response:     row.Response,
			LatencyMs:    row.LatencyMs,
			InputTokens:  row.InputTokens,
			OutputTokens: row.OutputTokens,
			TotalTokens:  row.TotalTokens,
			CostUSD:      row.CostUSD,
			Success:      row.Success,
			Error:        row.Error,
		})
	}
	first := rows[0]
	writeJSON(w, http.StatusOK, map[string]any{
		"run_id":         first.RunID,
		"task_id":        first.TaskID,
		"prompt_version": first.PromptVersion,
		"provider":       first.Provider,
		"created_at":     first.CreatedAt,
		"results":        results,
	})
}
