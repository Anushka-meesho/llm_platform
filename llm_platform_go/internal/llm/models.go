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

	// Attempts is the full detailed trace of every model the fallback walk
	// touched for this prediction — one entry per model, in walk order,
	// including failures, schema-invalid responses, skipped-unhealthy models,
	// and cache hits. It is populated only on the result returned by the
	// fallback walk (CallWithFallbackOpts); a bare CallModel leaves it nil.
	Attempts []Attempt

	// infraFailure marks provider-infrastructure errors (5xx/429/network/timeout)
	// — drives the circuit breaker.
	infraFailure bool
	// fallbackEligible marks errors that should advance the fallback chain to
	// the next model: infra failures AND provider-config failures (401/403/404,
	// open circuit, provider not configured) that are specific to one provider
	// and may not afflict the next. A 400/422 content error is NOT eligible —
	// the request itself is bad and would fail the same way downstream.
	fallbackEligible bool

	// httpStatus is the last upstream HTTP status CallModel saw for this model
	// (200 on success, 0 when the call never reached a response — e.g. network
	// error or unconfigured provider). retryCount is how many upstream HTTP
	// attempts CallModel made (1 = served on the first try, no retry). Both feed
	// the per-attempt gateway trace.
	httpStatus int
	retryCount int
}

// Attempt is one model the fallback walk touched for a single prediction: a
// live provider call, a cache hit, or a model skipped as unhealthy. One "run"
// (prediction) produces one Attempt per model the walk reached, in order — the
// detailed record of every gateway API call behind the single answer served to
// the caller. It is the source for the gateway_attempts table.
type Attempt struct {
	Seq      int // 0-based position in the walk (0 = the configured primary)
	Model    string
	Provider string
	// Outcome is one of: "success" (served the answer), "error" (provider/infra/
	// config failure), "schema_invalid" (succeeded but failed output validation),
	// "skipped_unhealthy" (circuit open — never called), "cache_hit" (served from
	// the per-model prediction cache, no provider call).
	Outcome      string
	FallbackUsed bool // this attempt was a fallback model (Seq > 0)
	// FallbackReason explains why the walk advanced PAST this model (the error
	// message, "output failed schema validation", or "model unhealthy (circuit
	// open)"). Empty when this attempt produced the served answer.
	FallbackReason string
	// Response is the content the provider returned for this attempt, when there
	// was one — present for a success, a cache hit, and crucially a
	// "schema_invalid" attempt (the model DID answer; it just failed validation),
	// so the wasted output is preserved in the trace. nil for errors/skips.
	Response     *string
	Error        string // classified error message; empty on success/cache hit
	HTTPStatus   int    // last upstream HTTP status (0 if no response reached)
	InfraFailure   bool   // provider-infrastructure trouble (5xx/429/network)
	RetryCount     int    // upstream HTTP attempts made for this model (1 = no retry)
	LatencyMs      int    // this model call's duration
	InputTokens    int
	OutputTokens   int
	TotalTokens    int
	CostUSD        float64
}

// RunResult aggregates all model results from one /run call.
type RunResult struct {
	Prompt  string
	Results []ModelResult
}
