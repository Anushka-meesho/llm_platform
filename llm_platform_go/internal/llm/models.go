package llm

// DefaultModels is the canonical set of provider keys used when none are specified.
var DefaultModels = []string{"gpt-4o-mini", "llama-groq", "gemini-flash"}

// ModelResult holds everything returned by one provider for one call.
type ModelResult struct {
	Model        string
	Provider     string  // which backend served it (openai/groq/gemini)
	Response     *string // nil on failure
	LatencyMs    int
	InputTokens  int
	OutputTokens int
	TotalTokens  int
	CostUSD      float64
	Success      bool
	Error        *string // nil on success
	FallbackUsed bool    // served by a model other than the requested primary
	Degraded     bool    // fallback used, or every model in the chain failed

	// infraFailure marks provider-infrastructure errors (5xx/429/network) as
	// opposed to config errors — drives fallback advancement and the breaker.
	infraFailure bool
}

// RunResult aggregates all model results from one /run call.
type RunResult struct {
	Prompt  string
	Results []ModelResult
}
