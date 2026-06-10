package llm

// DefaultModels is the canonical set of provider keys used when none are specified.
var DefaultModels = []string{"gpt-4o-mini", "llama-groq", "gemini-flash"}

// ModelResult holds everything returned by one provider for one call.
type ModelResult struct {
	Model        string
	Response     *string // nil on failure
	LatencyMs    int
	InputTokens  int
	OutputTokens int
	TotalTokens  int
	CostUSD      float64
	Success      bool
	Error        *string // nil on success
}

// RunResult aggregates all model results from one /run call.
type RunResult struct {
	Prompt  string
	Results []ModelResult
}
