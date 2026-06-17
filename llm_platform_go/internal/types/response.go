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
	Images       []string // multimodal inputs (data URLs / image URLs), in submission order; empty for text-only runs
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

// ── Admin: prompt history ────────────────────────────────────────────────────

// RunListItem is one row of the admin prompt-history list. It is deliberately
// lightweight: the prompt is truncated to a preview and full responses/images
// are omitted, so a page of history stays small regardless of how large the
// underlying prompts or base64 images are. The detail endpoint serves the rest.
type RunListItem struct {
	ID            int       `json:"id"`
	RunID         string    `json:"run_id"`
	TaskID        *string   `json:"task_id"`
	UserEmail     *string   `json:"user_email"`
	Model         string    `json:"model"`
	Provider      *string   `json:"provider"`
	PromptPreview string    `json:"prompt_preview"`
	HasImage      bool      `json:"has_image"`
	ImageCount    int       `json:"image_count"`
	Success       bool      `json:"success"`
	CacheHit      bool      `json:"cache_hit"`
	FallbackUsed  bool      `json:"fallback_used"`
	IsTest        bool      `json:"is_test"`
	LatencyMs     int       `json:"latency_ms"`
	TotalTokens   int       `json:"total_tokens"`
	CostUSD       float64   `json:"cost_usd"`
	CreatedAt     time.Time `json:"created_at"`
}

type RunListResponse struct {
	Page       int           `json:"page"`
	PageSize   int           `json:"page_size"`
	TotalRuns  int           `json:"total_runs"`
	TotalPages int           `json:"total_pages"`
	Runs       []RunListItem `json:"runs"`
}

// RunDetailResult is one model's outcome within a run (a playground /run stores
// one row per model; a task predict stores exactly one).
type RunDetailResult struct {
	Model        string  `json:"model"`
	Provider     *string `json:"provider"`
	Response     *string `json:"response"`
	Success      bool    `json:"success"`
	Error        *string `json:"error"`
	LatencyMs    int     `json:"latency_ms"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	TotalTokens  int     `json:"total_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	CacheHit     bool    `json:"cache_hit"`
	FallbackUsed bool    `json:"fallback_used"`
}

// RunDetailResponse is the full record for one run_id, shared prompt/inputs on
// top and per-model results below. Images carry the full data URLs (this is the
// only endpoint that returns them).
type RunDetailResponse struct {
	RunID         string            `json:"run_id"`
	TaskID        *string           `json:"task_id"`
	UserID        *string           `json:"user_id"`
	UserEmail     *string           `json:"user_email"`
	PromptVersion int               `json:"prompt_version"`
	Prompt        string            `json:"prompt"`
	SystemPrompt  *string           `json:"system_prompt"`
	Images        []string          `json:"images"`
	IsTest        bool              `json:"is_test"`
	CreatedAt     time.Time         `json:"created_at"`
	Results       []RunDetailResult `json:"results"`
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
