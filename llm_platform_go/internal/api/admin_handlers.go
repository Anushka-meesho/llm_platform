package api

import (
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
