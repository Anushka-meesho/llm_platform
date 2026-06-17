package llm

// DefaultModels is the canonical set of provider keys used when none are specified.
var DefaultModels = []string{"gpt-4o-mini", "gemini-2.5-flash", "llama-groq"}

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

	// infraFailure marks provider-infrastructure errors (5xx/429/network/timeout)
	// — drives the circuit breaker.
	infraFailure bool
	// fallbackEligible marks errors that should advance the fallback chain to
	// the next model: infra failures AND provider-config failures (401/403/404,
	// open circuit, provider not configured) that are specific to one provider
	// and may not afflict the next. A 400/422 content error is NOT eligible —
	// the request itself is bad and would fail the same way downstream.
	fallbackEligible bool
}

// RunResult aggregates all model results from one /run call.
type RunResult struct {
	Prompt  string
	Results []ModelResult
}
