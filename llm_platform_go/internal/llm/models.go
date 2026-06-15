package llm

// DefaultModels is the canonical set of model keys used when none are specified.
var DefaultModels = []string{"gpt-4o-mini", "gpt-4o", "gemini-2.5-flash", "gemini-2.5-pro", "claude-sonnet-4-6"}

// ModelResult holds everything returned by one provider for one call.
type ModelResult struct {
	Model           string
	Response        *string // nil on failure
	LatencyMs       int
	InputTokens     int
	OutputTokens    int
	TotalTokens     int
	CostUSD         float64
	Success         bool
	Error           *string // nil on success
	ContextWindow   int
	MaxOutputTokens int
}
