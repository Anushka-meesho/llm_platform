package llm

import "context"

// ModelCacheLookup is consulted before each live model call during a fallback
// walk. Returning (result, true) supplies a cached answer for that model: the
// chain treats it as a definitive success (no provider call) and stops there.
// Returning (_, false) means "not cached — call the provider live".
//
// It is checked in chain order, so a fallback model's cached answer is only
// ever used when the walk has actually reached that model in this request
// (every higher-priority model was tried live and advanced the chain). A
// recovered primary is therefore always called rather than shadowed by a stale
// lower-priority cache entry.
type ModelCacheLookup func(model string) (ModelResult, bool)

// CallWithFallback tries each model in order until one produces a definitive
// answer. It advances down the chain on failures that are specific to one
// provider — infrastructure trouble (5xx/429, network, open circuit) and
// provider-configuration errors (401/403/404, provider not configured) — since
// a different provider may succeed. A content-level outcome (success, or a
// 400/422 bad-request) is returned immediately, because retrying a bad request
// on a different model would just spend money on the same bug.
//
// The returned result carries FallbackUsed (served by a non-primary model)
// and Degraded (fallback used, or the whole chain failed) for the
// X-Platform-Degraded caller contract.
func CallWithFallback(ctx context.Context, clients *Clients, models []string, messages []ChatMessage, temperature float32, maxTokens int) ModelResult {
	return CallWithFallbackCached(ctx, clients, models, messages, temperature, maxTokens, nil)
}

// CallWithFallbackCached is CallWithFallback with an optional per-model cache
// consulted as the walk reaches each model (nil lookup ⇒ identical behaviour).
func CallWithFallbackCached(ctx context.Context, clients *Clients, models []string, messages []ChatMessage, temperature float32, maxTokens int, lookup ModelCacheLookup) ModelResult {
	if len(models) == 0 {
		return ModelResult{Success: false, Degraded: true, Error: strPtr("no models configured")}
	}

	var last ModelResult
	for i, model := range models {
		if lookup != nil {
			if cached, ok := lookup(model); ok {
				cached.FallbackUsed = i > 0
				cached.Degraded = i > 0
				return cached // cached answer for this model — definitive, stop here
			}
		}

		last = CallModel(ctx, clients, model, messages, temperature, maxTokens)
		last.FallbackUsed = i > 0
		last.Degraded = i > 0

		if last.Success || !last.fallbackEligible {
			return last // definitive answer (good or content error) — stop here
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
