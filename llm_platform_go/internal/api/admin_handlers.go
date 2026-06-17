package api

import (
	"encoding/json"
	"net/http"

	"llm_platform_go/internal/db"
	"llm_platform_go/internal/types"

	"github.com/go-chi/chi/v5"
)

// AdminListRuns serves the admin prompt-history list: every user's runs,
// newest first, paginated and filterable. Admin-only (gated by RequireAdmin).
//
// GET /v1/admin/runs?page=&page_size=&task_id=&model=&user_email=&q=&status=&type=
//
//	status: "success" | "error"            (default: any)
//	type:   "production" | "test"          (default: both)
func (h *Handler) AdminListRuns(w http.ResponseWriter, r *http.Request) {
	page := queryIntOrDefault(r, "page", 1)
	if page < 1 {
		page = 1
	}
	pageSize := queryIntOrDefault(r, "page_size", 25)
	if pageSize < 1 {
		pageSize = 25
	}
	if pageSize > 100 {
		pageSize = 100
	}

	filter := db.RunFilter{
		TaskID:    r.URL.Query().Get("task_id"),
		Model:     r.URL.Query().Get("model"),
		UserEmail: r.URL.Query().Get("user_email"),
		Query:     r.URL.Query().Get("q"),
	}
	switch r.URL.Query().Get("status") {
	case "success":
		t := true
		filter.Success = &t
	case "error":
		f := false
		filter.Success = &f
	}
	switch r.URL.Query().Get("type") {
	case "production":
		f := false
		filter.IsTest = &f
	case "test":
		t := true
		filter.IsTest = &t
	}

	runs, total, err := db.ListAllRuns(h.DB, filter, page, pageSize)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, types.RunListResponse{
		Page:       page,
		PageSize:   pageSize,
		TotalRuns:  total,
		TotalPages: db.TotalPages(total, pageSize),
		Runs:       runs,
	})
}

// AdminGetRun serves the full record for one run_id, including the complete
// prompt, system prompt, per-model responses, and the image data URLs.
//
// GET /v1/admin/runs/{run_id}
func (h *Handler) AdminGetRun(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "run_id")
	detail, err := db.GetRunDetail(h.DB, runID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error: "+err.Error())
		return
	}
	if detail == nil {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

// AdminRunModels lists the distinct models seen in the runs table, for the
// history page's model filter dropdown.
//
// GET /v1/admin/runs/models
func (h *Handler) AdminRunModels(w http.ResponseWriter, r *http.Request) {
	models, err := db.DistinctRunModels(h.DB)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

// AdminModelHealth returns the live circuit state of every tracked
// (task, model). Admin-only.
//
// GET /v1/admin/model-health
func (h *Handler) AdminModelHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, types.ModelHealthResponse{
		Enabled:  h.Health.Enabled(),
		Statuses: h.Health.Snapshot(),
	})
}

// AdminResetModelHealth forces one (task, model) back to healthy — the admin
// override. Admin-only.
//
// POST /v1/admin/model-health/reset  {"task_id":"…","model":"…"}
func (h *Handler) AdminResetModelHealth(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	var req struct {
		TaskID string `json:"task_id"`
		Model  string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid JSON body")
		return
	}
	if req.TaskID == "" || req.Model == "" {
		writeError(w, http.StatusUnprocessableEntity, "task_id and model are required")
		return
	}
	if h.Health == nil || !h.Health.Reset(req.TaskID, req.Model, user.Email) {
		writeError(w, http.StatusNotFound, "no health state tracked for that task/model")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"task_id": req.TaskID,
		"model":   req.Model,
		"state":   "healthy",
	})
}

// AdminModelHealthEvents returns the persisted health/fallback events, newest
// first, optionally filtered by task and/or model. Admin-only.
//
// GET /v1/admin/model-health/events?task_id=&model=&page=&page_size=
func (h *Handler) AdminModelHealthEvents(w http.ResponseWriter, r *http.Request) {
	page := queryIntOrDefault(r, "page", 1)
	if page < 1 {
		page = 1
	}
	pageSize := queryIntOrDefault(r, "page_size", 50)
	if pageSize < 1 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}
	taskID := r.URL.Query().Get("task_id")
	model := r.URL.Query().Get("model")

	events, total, err := db.ListHealthEvents(h.DB, taskID, model, page, pageSize)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, types.HealthEventsResponse{
		Page:       page,
		PageSize:   pageSize,
		TotalCount: total,
		TotalPages: db.TotalPages(total, pageSize),
		Events:     events,
	})
}
