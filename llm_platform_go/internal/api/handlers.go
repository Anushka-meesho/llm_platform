package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"llm_platform_go/internal/db"
	"llm_platform_go/internal/llm"
	"llm_platform_go/internal/types"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type Handler struct {
	DB      *sql.DB
	Clients *llm.Clients
}

// POST /run — streams results via Server-Sent Events as each model completes.
// Event sequence: meta → result (×N, fastest first) → done.
func (h *Handler) RunEndpoint(w http.ResponseWriter, r *http.Request) {
	var req types.RunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid request body: "+err.Error())
		return
	}
	if req.Prompt == "" {
		writeError(w, http.StatusUnprocessableEntity, "prompt must not be empty")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// 90 s ceiling so a hung provider can't block forever.
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()

	runID := uuid.New().String()
	wallStart := time.Now()
	now := time.Now().UTC()

	writeSSE(w, "meta", map[string]string{"run_id": runID})
	flusher.Flush()

	ch, count := llm.StreamAll(ctx, h.Clients, &req)

	var sessionID *string
	if req.SessionID != "" {
		sessionID = &req.SessionID
	}
	var sysPrompt *string
	if req.SystemPrompt != "" {
		sysPrompt = &req.SystemPrompt
	}

	var succeeded, failed int
	for range count {
		mr := <-ch

		if mr.Success {
			succeeded++
		} else {
			failed++
		}

		writeSSE(w, "result", types.ModelResultResponse{
			Model:           mr.Model,
			Response:        mr.Response,
			LatencyMs:       mr.LatencyMs,
			InputTokens:     mr.InputTokens,
			OutputTokens:    mr.OutputTokens,
			TotalTokens:     mr.TotalTokens,
			CostUSD:         mr.CostUSD,
			Success:         mr.Success,
			Error:           mr.Error,
			ContextWindow:   mr.ContextWindow,
			MaxOutputTokens: mr.MaxOutputTokens,
		})
		flusher.Flush()

		row := &types.RunRow{
			RunID:        runID,
			SessionID:    sessionID,
			Prompt:       req.Prompt,
			SystemPrompt: sysPrompt,
			Model:        mr.Model,
			Response:     mr.Response,
			LatencyMs:    mr.LatencyMs,
			InputTokens:  mr.InputTokens,
			OutputTokens: mr.OutputTokens,
			TotalTokens:  mr.TotalTokens,
			CostUSD:      mr.CostUSD,
			Success:      mr.Success,
			Error:        mr.Error,
			CreatedAt:    now,
		}
		go func(r *types.RunRow) { _ = db.InsertRun(h.DB, r) }(row)
	}

	writeSSE(w, "done", map[string]int{
		"total_wall_clock_ms": int(time.Since(wallStart).Milliseconds()),
		"models_succeeded":    succeeded,
		"models_failed":       failed,
	})
	flusher.Flush()
}

// GET /sessions
func (h *Handler) ListSessions(w http.ResponseWriter, r *http.Request) {
	page := queryIntOrDefault(r, "page", 1)
	pageSize := queryIntOrDefault(r, "page_size", 8)
	if pageSize > 100 {
		pageSize = 100
	}
	if page < 1 {
		page = 1
	}

	sessions, total, err := db.ListSessions(h.DB, page, pageSize)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, types.SessionListResponse{
		Page:          page,
		PageSize:      pageSize,
		TotalSessions: total,
		TotalPages:    db.TotalPages(total, pageSize),
		Sessions:      sessions,
	})
}

// GET /sessions/{session_id}
func (h *Handler) GetSession(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "session_id")

	rows, err := db.GetSession(h.DB, sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error: "+err.Error())
		return
	}
	if len(rows) == 0 {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	// Group rows by run_id, preserving chronological order.
	orderMap := make(map[string]int) // runID → index in turns slice
	var turns []types.SessionTurn

	for _, row := range rows {
		idx, exists := orderMap[row.RunID]
		if !exists {
			idx = len(turns)
			orderMap[row.RunID] = idx
			turns = append(turns, types.SessionTurn{
				RunID:        row.RunID,
				Prompt:       row.Prompt,
				SystemPrompt: row.SystemPrompt,
				CreatedAt:    row.CreatedAt,
				Results:      []types.TurnResult{},
			})
		}
		turns[idx].Results = append(turns[idx].Results, types.TurnResult{
			Model:        row.Model,
			Response:     row.Response,
			LatencyMs:    row.LatencyMs,
			InputTokens:  row.InputTokens,
			OutputTokens: row.OutputTokens,
			TotalTokens:  row.TotalTokens,
			CostUSD:      row.CostUSD,
			Success:      row.Success,
			Error:        row.Error,
			Rating:       row.Rating,
			Note:         row.Note,
		})
	}

	writeJSON(w, http.StatusOK, types.SessionDetailResponse{
		SessionID: sessionID,
		Turns:     turns,
	})
}

// DELETE /sessions
func (h *Handler) DeleteSessions(w http.ResponseWriter, r *http.Request) {
	var req types.DeleteSessionsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid request body: "+err.Error())
		return
	}

	deleted, err := db.DeleteSessions(h.DB, req.SessionIDs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, types.DeleteSessionsResponse{
		DeletedCount: int(deleted),
		SessionIDs:   req.SessionIDs,
	})
}

// GET /health
func (h *Handler) HealthCheck(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":           "ok",
		"models_available": llm.DefaultModels,
	})
}

// GET /models — probes each registered model with a 1-token request and
// returns which ones the virtual key can actually reach.
func (h *Handler) ModelsCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	results := llm.CheckModels(ctx, h.Clients)
	writeJSON(w, http.StatusOK, map[string]interface{}{"models": results})
}

// GET /pricing — returns the full pricing table from pricing.json.
func (h *Handler) GetPricing(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{"models": llm.GetAllRates()})
}

// POST /ratings
func (h *Handler) SaveRating(w http.ResponseWriter, r *http.Request) {
	var req types.RatingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid request body: "+err.Error())
		return
	}
	if req.RunID == "" || req.Model == "" || req.SessionID == "" {
		writeError(w, http.StatusUnprocessableEntity, "run_id, model, and session_id are required")
		return
	}
	if req.Rating < 1 || req.Rating > 5 {
		writeError(w, http.StatusUnprocessableEntity, "rating must be between 1 and 5")
		return
	}
	if err := db.UpsertRating(h.DB, req.RunID, req.Model, req.SessionID, req.Rating, req.Note); err != nil {
		writeError(w, http.StatusInternalServerError, "database error: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// GET /sessions/{session_id}/leaderboard
func (h *Handler) GetLeaderboard(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "session_id")
	entries, err := db.GetLeaderboard(h.DB, sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, types.LeaderboardResponse{
		SessionID: sessionID,
		Entries:   entries,
	})
}

func queryIntOrDefault(r *http.Request, key string, def int) int {
	s := r.URL.Query().Get(key)
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
