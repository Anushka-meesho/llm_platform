package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"llm_platform_go/internal/auth"
	"llm_platform_go/internal/cache"
	"llm_platform_go/internal/db"
	"llm_platform_go/internal/health"
	"llm_platform_go/internal/llm"
	"llm_platform_go/internal/tasks"
	"llm_platform_go/internal/types"
	"llm_platform_go/internal/users"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// AuthConfig carries everything the auth handlers and middleware need to mint
// and validate session tokens.
type AuthConfig struct {
	Secret      []byte
	CookieName  string
	Issuer      string
	Domain      string
	Secure      bool
	TokenExpiry time.Duration
}

type Handler struct {
	DB      *sql.DB
	Clients *llm.Clients
	Users   users.Store
	Tasks   *tasks.Store
	Runs     *db.RunWriter            // async observability writer; nil → synchronous inserts
	Attempts *db.GatewayAttemptWriter // async gateway-trace writer; nil → synchronous inserts
	Cache    cache.Cache              // prediction cache; nil → caching off
	Health   *health.Tracker          // per-(task, model) circuit breaker; nil → no gating
	Auth     AuthConfig

	spend spendCache // budget gate's in-memory daily-spend view (no hot-path SUM)
}

// requireUser pulls the authenticated user from context. Handlers behind
// RequireAuth can assume it's present, but we guard defensively.
func requireUser(w http.ResponseWriter, r *http.Request) (*auth.User, bool) {
	u, ok := auth.FromContext(r.Context())
	if !ok || u == nil {
		writeErr(w, r, Unauthorized(CodeUnauthorized, "not authenticated"))
		return nil, false
	}
	return u, true
}

// redactedTask returns a task view safe for the given user. Principals without
// task:view_prompt (service callers) get the prompt template and system prompt
// blanked — they integrate against the task contract (schemas + metadata), not
// the prompt internals. Returns a copy so the shared config-cache entry is never
// mutated; callers with the permission get the original untouched.
func redactedTask(user *auth.User, t *tasks.Task) *tasks.Task {
	if user == nil || user.Can(auth.PermTaskViewPrompt) {
		return t
	}
	cp := *t
	cp.PromptTemplate = ""
	cp.SystemPrompt = ""
	return &cp
}

// imagesFromConversations extracts image URLs from the last user message in any
// model_conversations entry. All models receive the same user message, so the
// first match is sufficient.
func imagesFromConversations(convs map[string][]types.Message) []string {
	for _, msgs := range convs {
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].Role != "user" {
				continue
			}
			parts, ok := msgs[i].Content.([]interface{})
			if !ok {
				return nil
			}
			var out []string
			for _, part := range parts {
				pm, ok := part.(map[string]interface{})
				if !ok {
					continue
				}
				if pm["type"] == "image_url" {
					if iu, ok := pm["image_url"].(map[string]interface{}); ok {
						if url, ok := iu["url"].(string); ok && url != "" {
							out = append(out, url)
						}
					}
				}
			}
			return out
		}
	}
	return nil
}

// POST /run
func (h *Handler) RunEndpoint(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}

	var req types.RunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, r, Unprocessable(CodeInvalidBody, "invalid request body: %s", err.Error()))
		return
	}
	if req.Prompt == "" {
		writeErr(w, r, Unprocessable(CodeValidationFailed, "prompt must not be empty"))
		return
	}

	userID := user.Subject
	userEmail := user.Email
	runID := uuid.New().String()
	wallStart := time.Now()

	runResult := llm.RunAll(r.Context(), h.Clients, &req)

	totalMs := int(time.Since(wallStart).Milliseconds())

	var succeeded, failed int
	respResults := make([]types.ModelResultResponse, 0, len(runResult.Results))

	now := time.Now().UTC()
	playgroundID := tasks.PlaygroundTaskID
	images := imagesFromConversations(req.ModelConversations)

	for _, mr := range runResult.Results {
		var sessionID *string
		if req.SessionID != "" {
			sessionID = &req.SessionID
		}
		var sysPrompt *string
		if req.SystemPrompt != "" {
			sysPrompt = &req.SystemPrompt
		}
		provider := mr.Provider

		row := &types.RunRow{
			RunID:        runID,
			SessionID:    sessionID,
			Prompt:       req.Prompt,
			SystemPrompt: sysPrompt,
			Images:       images,
			Model:        mr.Model,
			Response:     mr.Response,
			LatencyMs:    mr.LatencyMs,
			InputTokens:  mr.InputTokens,
			OutputTokens: mr.OutputTokens,
			TotalTokens:  mr.TotalTokens,
			CostUSD:      mr.CostUSD,
			Success:      mr.Success,
			Error:        mr.Error,
			UserID:       &userID,
			UserEmail:    &userEmail,
			TaskID:       &playgroundID, // Compare UI usage is cost-attributed like any task
			Provider:     &provider,
			CreatedAt:    now,
		}
		h.insertRun(row) // observability write — never fails the response

		if mr.Success {
			succeeded++
		} else {
			failed++
		}

		respResults = append(respResults, types.ModelResultResponse{
			Model:        mr.Model,
			Response:     mr.Response,
			LatencyMs:    mr.LatencyMs,
			InputTokens:  mr.InputTokens,
			OutputTokens: mr.OutputTokens,
			TotalTokens:  mr.TotalTokens,
			CostUSD:      mr.CostUSD,
			Success:      mr.Success,
			Error:        mr.Error,
		})
	}

	var sysPromptPtr *string
	if req.SystemPrompt != "" {
		sysPromptPtr = &req.SystemPrompt
	}

	writeJSON(w, http.StatusOK, types.RunResponse{
		RunID:            runID,
		Prompt:           req.Prompt,
		SystemPrompt:     sysPromptPtr,
		Results:          respResults,
		TotalWallClockMs: totalMs,
		ModelsSucceeded:  succeeded,
		ModelsFailed:     failed,
	})
}

// GET /sessions
func (h *Handler) ListSessions(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	page := queryIntOrDefault(r, "page", 1)
	pageSize := queryIntOrDefault(r, "page_size", 8)
	if pageSize > 100 {
		pageSize = 100
	}
	if page < 1 {
		page = 1
	}

	sessions, total, err := db.ListSessions(h.DB, user.Subject, page, pageSize)
	if err != nil {
		writeErr(w, r, Internal(CodeDBError, "list sessions").WithCause(err))
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
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	sessionID := chi.URLParam(r, "session_id")

	rows, err := db.GetSession(h.DB, user.Subject, sessionID)
	if err != nil {
		writeErr(w, r, Internal(CodeDBError, "load session %q", sessionID).WithCause(err))
		return
	}
	if len(rows) == 0 {
		writeErr(w, r, NotFound(CodeSessionNotFound, "session not found"))
		return
	}

	// Group rows by run_id, preserving chronological order.
	type turnKey struct {
		runID     string
		createdAt time.Time
	}
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
				Images:       row.Images,
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
		})
	}

	writeJSON(w, http.StatusOK, types.SessionDetailResponse{
		SessionID: sessionID,
		Turns:     turns,
	})
}

// GET /sessions/{session_id}/leaderboard
func (h *Handler) GetLeaderboard(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	sessionID := chi.URLParam(r, "session_id")
	entries, err := db.GetLeaderboard(h.DB, user.Subject, sessionID)
	if err != nil {
		writeErr(w, r, Internal(CodeDBError, "load leaderboard %q", sessionID).WithCause(err))
		return
	}
	writeJSON(w, http.StatusOK, types.LeaderboardResponse{
		SessionID: sessionID,
		Entries:   entries,
	})
}

// DELETE /sessions
func (h *Handler) DeleteSessions(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	var req types.DeleteSessionsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, r, Unprocessable(CodeInvalidBody, "invalid request body: %s", err.Error()))
		return
	}

	deleted, err := db.DeleteSessions(h.DB, user.Subject, req.SessionIDs)
	if err != nil {
		writeErr(w, r, Internal(CodeDBError, "delete sessions").WithCause(err))
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
		"models_available": llm.AllModels(),
	})
}

// GET /pricing — serves the pricing table so the frontend estimates with the
// same rates the backend uses for actual cost calculation.
func (h *Handler) Pricing(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{"pricing": llm.PricingTable()})
}

// POST /feedback  {run_id, model, rating}
func (h *Handler) Feedback(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	var req types.FeedbackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, r, Unprocessable(CodeInvalidBody, "invalid request body: %s", err.Error()))
		return
	}
	if req.RunID == "" || req.Model == "" {
		writeErr(w, r, Unprocessable(CodeValidationFailed, "run_id and model are required"))
		return
	}
	if req.Rating < 1 || req.Rating > 5 {
		writeErr(w, r, Unprocessable(CodeValidationFailed, "rating must be between 1 and 5"))
		return
	}

	if err := db.UpsertFeedback(h.DB, req.RunID, req.Model, user.Subject, req.Rating); err != nil {
		writeErr(w, r, Internal(CodeDBError, "save rating").WithCause(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"run_id": req.RunID, "model": req.Model, "rating": req.Rating,
	})
}

// GET /dashboard — per-user usage aggregates.
func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	stats, err := db.DashboardStats(h.DB, user.Subject)
	if err != nil {
		writeErr(w, r, Internal(CodeDBError, "load dashboard stats").WithCause(err))
		return
	}
	writeJSON(w, http.StatusOK, stats)
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
