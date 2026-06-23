package llm

import (
	"context"
	"fmt"
)

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
func CallWithFallbackOpts(ctx context.Context, clients *Clients, models []string, messages []ChatMessage, temperature float32, maxTokens int, opts FallbackOptions) (result ModelResult) {
	if len(models) == 0 {
		return ModelResult{Success: false, Degraded: true, Error: strPtr("no models configured")}
	}

	// attempts records every model the walk touches, in order. It is attached to
	// whichever result is ultimately returned so the caller can persist the full
	// gateway trace (every fallback, its reason, errors, retries, latencies).
	var attempts []Attempt
	defer func() { result.Attempts = attempts }()

	var last ModelResult
	attempted := false
	for i, model := range models {
		// Circuit breaker: skip models marked unhealthy for this task — no call.
		if opts.Gate != nil && !opts.Gate.Allow(model) {
			last = skippedResult(model)
			last.FallbackUsed = i > 0
			attempts = append(attempts, attemptFrom(i, last, "skipped_unhealthy",
				"model unhealthy (circuit open) for this task"))
			continue
		}
		attempted = true

		if opts.Lookup != nil {
			if cached, ok := opts.Lookup(model); ok {
				cached.FallbackUsed = i > 0
				cached.Degraded = i > 0
				attempts = append(attempts, attemptFrom(i, cached, "cache_hit", ""))
				return cached // cached answer for this model — definitive, stop here
			}
		}

		last = CallModel(ctx, clients, model, messages, temperature, maxTokens, nil)
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
				attempts = append(attempts, attemptFrom(i, last, "schema_invalid",
					"output failed schema validation"))
				if ctx.Err() != nil {
					break
				}
				continue // schematic failure — try the next model
			}
			if opts.Gate != nil {
				opts.Gate.RecordSuccess(model)
			}
			attempts = append(attempts, attemptFrom(i, last, "success", ""))
			return last // usable answer — stop here
		}

		if !last.fallbackEligible {
			// 400/422 content error — bad input, don't penalize health. Definitive,
			// so no fallback reason (the walk stops here on purpose, not by falling
			// through to another model).
			attempts = append(attempts, attemptFrom(i, last, "error", ""))
			return last
		}
		// Provider/infra/auth failure — count it and advance the chain.
		reason := "model call failed"
		if last.Error != nil {
			reason = *last.Error
		}
		if opts.Gate != nil {
			opts.Gate.RecordFailure(model, reason)
		}
		attempts = append(attempts, attemptFrom(i, last, "error", reason))
		if ctx.Err() != nil {
			break // caller gone — don't burn the rest of the chain
		}
	}

	if !attempted {
		// Every model in the chain was skipped as unhealthy (circuit open) — no
		// model was even callable, so there is nothing usable to serve this task.
		return ModelResult{Success: false, Degraded: true,
			Error: strPtr(fmt.Sprintf(
				"no usable model for this task — all %d configured model(s) are unhealthy (circuit open)",
				len(models)))}
	}

	// The walk fell through every model. Two very different outcomes land here,
	// and they must not be conflated:
	//
	//   - A genuine chain failure: no model produced a usable API response
	//     (infra/provider errors everywhere). last.Success is false. Lead with the
	//     no-usable-model summary so the caller sees the whole chain is down.
	//
	//   - Every model answered, but the output failed schema validation on all of
	//     them. last.Success is true — this is the platform's "flagged, not
	//     failed" contract: the caller still gets a 200 with the raw response and
	//     output_valid=false. Pasting an "all models failed" error onto it would
	//     be a lie (it's what made such a run render as "ok" with a failure error
	//     attached). Leave it as a degraded success; each model's invalid output
	//     is preserved per-model in the gateway trace.
	last.Degraded = true
	if !last.Success {
		if last.Error != nil {
			last.Error = strPtr(fmt.Sprintf(
				"no usable model for this task — all %d configured model(s) failed; last error: %s",
				len(models), *last.Error))
		} else {
			last.Error = strPtr(fmt.Sprintf(
				"no usable model for this task — all %d configured model(s) failed", len(models)))
		}
	}
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

// attemptFrom snapshots one model's outcome within the walk into an Attempt for
// the gateway trace. outcome is the classified result ("success", "error",
// "schema_invalid", "skipped_unhealthy", "cache_hit"); fallbackReason explains
// why the walk advanced past this model (empty when it produced the answer).
func attemptFrom(seq int, r ModelResult, outcome, fallbackReason string) Attempt {
	errMsg := ""
	if r.Error != nil {
		errMsg = *r.Error
	}
	return Attempt{
		Seq:            seq,
		Model:          r.Model,
		Provider:       r.Provider,
		Outcome:        outcome,
		FallbackUsed:   seq > 0,
		FallbackReason: fallbackReason,
		Response:       r.Response,
		Error:          errMsg,
		HTTPStatus:     r.httpStatus,
		InfraFailure:   r.infraFailure,
		RetryCount:     r.retryCount,
		LatencyMs:      r.LatencyMs,
		InputTokens:    r.InputTokens,
		OutputTokens:   r.OutputTokens,
		TotalTokens:    r.TotalTokens,
		CostUSD:        r.CostUSD,
	}
}
