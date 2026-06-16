package types

import "time"

type ModelResultResponse struct {
	Model        string  `json:"model"`
	Response     *string `json:"response"`      // null on failure
	LatencyMs    int     `json:"latency_ms"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	TotalTokens  int     `json:"total_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	Success      bool    `json:"success"`
	Error        *string `json:"error"` // null on success
}

type RunResponse struct {
	RunID            string                `json:"run_id"`
	Prompt           string                `json:"prompt"`
	SystemPrompt     *string               `json:"system_prompt"`
	Results          []ModelResultResponse `json:"results"`
	TotalWallClockMs int                   `json:"total_wall_clock_ms"`
	ModelsSucceeded  int                   `json:"models_succeeded"`
	ModelsFailed     int                   `json:"models_failed"`
}

type SessionSummary struct {
	SessionID   string    `json:"session_id"`
	FirstPrompt string    `json:"first_prompt"`
	TurnCount   int       `json:"turn_count"`
	CreatedAt   time.Time `json:"created_at"`
}

type SessionListResponse struct {
	Page          int              `json:"page"`
	PageSize      int              `json:"page_size"`
	TotalSessions int              `json:"total_sessions"`
	TotalPages    int              `json:"total_pages"`
	Sessions      []SessionSummary `json:"sessions"`
}

type TurnResult struct {
	Model        string  `json:"model"`
	Response     *string `json:"response"`
	LatencyMs    int     `json:"latency_ms"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	TotalTokens  int     `json:"total_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	Success      bool    `json:"success"`
	Error        *string `json:"error"`
}

type SessionTurn struct {
	RunID        string       `json:"run_id"`
	Prompt       string       `json:"prompt"`
	SystemPrompt *string      `json:"system_prompt"`
	CreatedAt    time.Time    `json:"created_at"`
	Results      []TurnResult `json:"results"`
}

type SessionDetailResponse struct {
	SessionID string        `json:"session_id"`
	Turns     []SessionTurn `json:"turns"`
}

type DeleteSessionsResponse struct {
	DeletedCount int      `json:"deleted_count"`
	SessionIDs   []string `json:"session_ids"`
}

// LeaderboardEntry is one model's average manual rating within a session.
type LeaderboardEntry struct {
	Model       string  `json:"model"`
	AvgScore    float64 `json:"avg_score"`
	RatingCount int     `json:"rating_count"`
}

type LeaderboardResponse struct {
	SessionID string             `json:"session_id"`
	Entries   []LeaderboardEntry `json:"entries"`
}

// RunRow is the internal DB representation — one row in the runs table.
type RunRow struct {
	ID           int
	RunID        string
	SessionID    *string
	Prompt       string
	SystemPrompt *string
	Model        string
	Response     *string
	LatencyMs    int
	InputTokens  int
	OutputTokens int
	TotalTokens  int
	CostUSD      float64
	Success      bool
	Error        *string
	UserID       *string
	UserEmail    *string
	// Task keying + observability (Phase 0)
	TaskID        *string
	PromptVersion int
	Provider      *string
	FallbackUsed  bool
	CacheHit      bool
	IsTest        bool // Studio test-panel call, not production traffic
	CreatedAt     time.Time
}

// ── Feedback ────────────────────────────────────────────────────────────────

type FeedbackRequest struct {
	RunID  string `json:"run_id"`
	Model  string `json:"model"`
	Rating int    `json:"rating"` // 1–5
}

// ── Dashboard ─────────────────────────────────────────────────────────────

type ModelStats struct {
	Model        string  `json:"model"`
	Runs         int     `json:"runs"`
	TotalTokens  int     `json:"total_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
	AvgRating    float64 `json:"avg_rating"` // 0 if no ratings yet
	RatingCount  int     `json:"rating_count"`
}

type DailyPoint struct {
	Date        string  `json:"date"` // YYYY-MM-DD
	CostUSD     float64 `json:"cost_usd"`
	TotalTokens int     `json:"total_tokens"`
	Runs        int     `json:"runs"`
}

type TaskStats struct {
	TaskID       string  `json:"task_id"`
	Runs         int     `json:"runs"`
	TotalTokens  int     `json:"total_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
	SuccessRate  float64 `json:"success_rate"` // 0..1
}

type DashboardResponse struct {
	TotalRuns    int          `json:"total_runs"`
	TotalTokens  int          `json:"total_tokens"`
	TotalCostUSD float64      `json:"total_cost_usd"`
	ByTask       []TaskStats  `json:"by_task"`
	ByModel      []ModelStats `json:"by_model"`
	Daily        []DailyPoint `json:"daily"`
}
