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

// HealthGate is the per-(task, model) circuit breaker consulted during a
// fallback walk. Allow gates whether a model may be called at all (an unhealthy
// model is skipped without a call); RecordSuccess/RecordFailure feed the
// breaker the outcome of each attempt so it can trip / recover. The walk is
// task-agnostic, so the caller supplies a gate already bound to one task.
type HealthGate interface {
	Allow(model string) bool
	RecordSuccess(model string)
	RecordFailure(model, reason string)
}

// OutputValidator reports whether a model's raw response is usable for the task
// (e.g. it passes the output schema). A response that fails validation is a
// "schematic" failure: the walk treats it like a provider error — records it
// against health and falls back to the next model. nil ⇒ accept everything.
type OutputValidator func(text string) bool

// FallbackOptions bundles the optional hooks for CallWithFallbackOpts. A
// zero-value FallbackOptions reproduces the plain CallWithFallback behaviour.
type FallbackOptions struct {
	Lookup   ModelCacheLookup
	Gate     HealthGate
	Validate OutputValidator
}

// CallWithFallbackCached is CallWithFallback with an optional per-model cache
// consulted as the walk reaches each model (nil lookup ⇒ identical behaviour).
func CallWithFallbackCached(ctx context.Context, clients *Clients, models []string, messages []ChatMessage, temperature float32, maxTokens int, lookup ModelCacheLookup) ModelResult {
	return CallWithFallbackOpts(ctx, clients, models, messages, temperature, maxTokens, FallbackOptions{Lookup: lookup})
}

// CallWithFallbackOpts is the full fallback walk with circuit-breaker health
// gating and output validation. For each model in priority order it:
//   - skips the model entirely if the health gate marks it unhealthy;
//   - serves a cached answer if the lookup hits;
//   - otherwise calls it live. A usable success (passes Validate) is returned
//     and recorded healthy. A provider error or a schema-invalid response is
//     recorded against health and advances the chain. A 400/422 content error
//     is returned immediately (bad input — not the model's fault).
func CallWithFallbackOpts(ctx context.Context, clients *Clients, models []string, messages []ChatMessage, temperature float32, maxTokens int, opts FallbackOptions) ModelResult {
	if len(models) == 0 {
		return ModelResult{Success: false, Degraded: true, Error: strPtr("no models configured")}
	}

	var last ModelResult
	attempted := false
	for i, model := range models {
		// Circuit breaker: skip models marked unhealthy for this task — no call.
		if opts.Gate != nil && !opts.Gate.Allow(model) {
			last = skippedResult(model)
			last.FallbackUsed = i > 0
			continue
		}
		attempted = true

		if opts.Lookup != nil {
			if cached, ok := opts.Lookup(model); ok {
				cached.FallbackUsed = i > 0
				cached.Degraded = i > 0
				return cached // cached answer for this model — definitive, stop here
			}
		}

		last = CallModel(ctx, clients, model, messages, temperature, maxTokens)
		last.FallbackUsed = i > 0
		last.Degraded = i > 0

		if last.Success {
			// Provider returned content — is it usable for the task?
			if opts.Validate != nil && last.Response != nil && !opts.Validate(*last.Response) {
				if opts.Gate != nil {
					opts.Gate.RecordFailure(model, "output failed schema validation")
				}
				last.fallbackEligible = true
				last.Degraded = true
				if ctx.Err() != nil {
					break
				}
				continue // schematic failure — try the next model
			}
			if opts.Gate != nil {
				opts.Gate.RecordSuccess(model)
			}
			return last // usable answer — stop here
		}

		if !last.fallbackEligible {
			return last // 400/422 content error — bad input, don't penalize health
		}
		// Provider/infra/auth failure — count it and advance the chain.
		if opts.Gate != nil {
			reason := "model call failed"
			if last.Error != nil {
				reason = *last.Error
			}
			opts.Gate.RecordFailure(model, reason)
		}
		if ctx.Err() != nil {
			break // caller gone — don't burn the rest of the chain
		}
	}

	if !attempted {
		// Every model in the chain was skipped as unhealthy.
		return ModelResult{Success: false, Degraded: true,
			Error: strPtr("all models are unhealthy for this task — circuit open")}
	}

	// Whole chain failed (infra errors and/or schema-invalid everywhere).
	last.Degraded = true
	return last
}

// skippedResult is the placeholder for a model the breaker skipped without
// calling. It is fallback-eligible so the walk advances to the next model.
func skippedResult(model string) ModelResult {
	msg := "model skipped — unhealthy (circuit open) for this task"
	return ModelResult{
		Model:            model,
		Provider:         ProviderName(model),
		Success:          false,
		Error:            &msg,
		Degraded:         true,
		infraFailure:     true,
		fallbackEligible: true,
	}
}

func strPtr(s string) *string { return &s }
