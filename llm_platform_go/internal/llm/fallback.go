package llm

import "context"

// CallWithFallback tries each model in order until one produces a definitive
// answer. It advances down the chain only on *infrastructure* failures
// (provider 5xx/429, network errors, open circuit) — a content-level outcome
// (success, or a 4xx config error) is returned immediately, because retrying
// a bad request on a different model would just spend money on the same bug.
//
// The returned result carries FallbackUsed (served by a non-primary model)
// and Degraded (fallback used, or the whole chain failed) for the
// X-Platform-Degraded caller contract.
func CallWithFallback(ctx context.Context, clients *Clients, models []string, messages []ChatMessage, temperature float32, maxTokens int) ModelResult {
	if len(models) == 0 {
		return ModelResult{Success: false, Degraded: true, Error: strPtr("no models configured")}
	}

	var last ModelResult
	for i, model := range models {
		last = CallModel(ctx, clients, model, messages, temperature, maxTokens)
		last.FallbackUsed = i > 0
		last.Degraded = i > 0

		if last.Success || !last.infraFailure {
			return last // definitive answer (good or config error) — stop here
		}
		if ctx.Err() != nil {
			break // caller gone — don't burn the rest of the chain
		}
	}

	// Whole chain failed on infra errors.
	last.Degraded = true
	return last
}

func strPtr(s string) *string { return &s }
