package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"llm_platform_go/internal/auth"
	"llm_platform_go/internal/db"
	"llm_platform_go/internal/tasks"

	"github.com/go-chi/chi/v5"
)

// resolveTask is the shared 404/500 lookup for task-scoped handlers.
func (h *Handler) resolveTask(w http.ResponseWriter, r *http.Request) (*tasks.Task, bool) {
	t, err := h.Tasks.Get(chi.URLParam(r, "task_id"))
	if errors.Is(err, tasks.ErrNotFound) {
		writeError(w, http.StatusNotFound, "task not found")
		return nil, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error: "+err.Error())
		return nil, false
	}
	return t, true
}

// GET /v1/tasks/{task_id}/versions — prompt history, newest first.
func (h *Handler) ListPromptVersions(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	task, ok := h.resolveTask(w, r)
	if !ok {
		return
	}
	versions, err := h.Tasks.ListVersions(task.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error: "+err.Error())
		return
	}
	// Callers (no task:view_prompt) get version metadata but not the prompt
	// bodies. ListVersions returns by-value rows, so blanking here is safe.
	if !user.Can(auth.PermTaskViewPrompt) {
		for i := range versions {
			versions[i].PromptTemplate = ""
			versions[i].SystemPrompt = ""
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"task_id":        task.ID,
		"active_version": task.PromptVersion,
		"versions":       versions,
	})
}

// POST /v1/tasks/{task_id}/versions {prompt_template, system_prompt?, note?}
// Saves a draft WITHOUT activating it.
func (h *Handler) SaveDraftVersion(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	task, ok := h.resolveTask(w, r)
	if !ok {
		return
	}

	var req struct {
		PromptTemplate string `json:"prompt_template"`
		SystemPrompt   string `json:"system_prompt"`
		Note           string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid request body: "+err.Error())
		return
	}

	version, err := h.Tasks.SaveDraft(task.ID, req.PromptTemplate, req.SystemPrompt, req.Note, user.Subject)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"task_id": task.ID,
		"version": version,
		"active":  false,
	})
}

// POST /v1/tasks/{task_id}/deploy {version}
// Activates a saved version. Phase 2 adds the eval quality gate here.
func (h *Handler) DeployVersion(w http.ResponseWriter, r *http.Request) {
	task, ok := h.resolveTask(w, r)
	if !ok {
		return
	}

	var req struct {
		Version int `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Version <= 0 {
		writeError(w, http.StatusUnprocessableEntity, "version (positive integer) is required")
		return
	}

	// ── Phase 2 eval gate slots in here:
	// verify an eval run exists for (task, version, active model) and that all
	// task thresholds are met before allowing the deploy.

	if err := h.Tasks.Deploy(task.ID, req.Version); err != nil {
		if errors.Is(err, tasks.ErrVersionNotFound) {
			writeError(w, http.StatusNotFound, "version not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "deploy failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"task_id":        task.ID,
		"active_version": req.Version,
		"status":         "deployed",
	})
}

// DELETE /v1/tasks/{task_id}/versions/{version}
// Removes a prompt version from the history. Admin-only (gated by the router).
// The active version can't be deleted — deploy a different one first.
func (h *Handler) DeleteVersion(w http.ResponseWriter, r *http.Request) {
	task, ok := h.resolveTask(w, r)
	if !ok {
		return
	}
	version, err := strconv.Atoi(chi.URLParam(r, "version"))
	if err != nil || version <= 0 {
		writeError(w, http.StatusUnprocessableEntity, "version must be a positive integer")
		return
	}

	switch err := h.Tasks.DeleteVersion(task.ID, version); {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]any{
			"task_id": task.ID,
			"version": version,
			"status":  "deleted",
		})
	case errors.Is(err, tasks.ErrVersionNotFound):
		writeError(w, http.StatusNotFound, "version not found")
	case errors.Is(err, tasks.ErrVersionActive):
		writeError(w, http.StatusConflict, "cannot delete the active version — deploy another version first")
	default:
		writeError(w, http.StatusInternalServerError, "delete failed: "+err.Error())
	}
}

// POST /v1/tasks/{task_id}/test {inputs, version?, model?}
// Runs the prediction pipeline without counting as production traffic
// (run row flagged is_test). Supports testing a draft version and/or an
// alternative model — the Studio test panel.
func (h *Handler) TestTask(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	task, ok := h.resolveTask(w, r)
	if !ok {
		return
	}

	var req struct {
		Inputs  json.RawMessage `json:"inputs"`
		Version int             `json:"version,omitempty"`
		Model   string          `json:"model,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid request body: "+err.Error())
		return
	}

	opts := predictOptions{isTest: true, overrideModel: req.Model}
	if req.Version > 0 && req.Version != task.PromptVersion {
		v, err := h.Tasks.GetVersion(task.ID, req.Version)
		if errors.Is(err, tasks.ErrVersionNotFound) {
			writeError(w, http.StatusNotFound, "version not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "database error: "+err.Error())
			return
		}
		opts.overrideVersion = v
	}

	outcome, herr := h.executePrediction(r.Context(), task, req.Inputs, user, opts)
	if herr != nil {
		writeError(w, herr.status, herr.detail)
		return
	}

	status := http.StatusOK
	if !outcome.Result.Success {
		status = http.StatusBadGateway
	}
	writeJSON(w, status, shapePredictResponse(task, outcome))
}

// GET /v1/tasks/{task_id}/stats?days=30 — task-scoped usage across all callers.
func (h *Handler) TaskStats(w http.ResponseWriter, r *http.Request) {
	task, ok := h.resolveTask(w, r)
	if !ok {
		return
	}
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 {
		days = 30
	}

	daily, totals, err := db.TaskDailyStats(h.DB, task.ID, days)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"task_id": task.ID,
		"days":    days,
		"totals":  totals,
		"daily":   daily,
	})
}
