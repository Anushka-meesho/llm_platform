package types

import "time"

type ModelResultResponse struct {
	Model           string  `json:"model"`
	Response        *string `json:"response"`          // null on failure
	LatencyMs       int     `json:"latency_ms"`
	InputTokens     int     `json:"input_tokens"`
	OutputTokens    int     `json:"output_tokens"`
	TotalTokens     int     `json:"total_tokens"`
	CostUSD         float64 `json:"cost_usd"`
	Success         bool    `json:"success"`
	Error           *string `json:"error"`             // null on success
	ContextWindow   int     `json:"context_window"`
	MaxOutputTokens int     `json:"max_output_tokens"`
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
	Rating       *int    `json:"rating"`
	Note         *string `json:"note"`
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
	CreatedAt    time.Time
	Rating       *int
	Note         *string
}
