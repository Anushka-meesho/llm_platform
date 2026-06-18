package llm

import (
	"context"
	"errors"
)

// isInfraFailure reports whether an error indicates provider infrastructure
// trouble as opposed to a request configuration problem (4xx other than 429) or
// caller cancellation. It distinguishes "the provider is sick" (429/5xx/network)
// from "this request is bad" so the fallback walk can decide whether retrying or
// advancing the chain is worthwhile.
func isInfraFailure(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false // caller went away — not the provider's fault
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.HTTPStatusCode == 429 || apiErr.HTTPStatusCode >= 500
	}
	// Network errors, timeouts, deadline exceeded, malformed responses.
	return true
}

// shouldFallback reports whether an error on one model should advance the
// fallback chain to the next model. That's broader than isInfraFailure: besides
// provider-availability trouble (429/5xx/network/timeout), it also covers
// provider-*configuration* failures — 401 (missing/invalid key), 403
// (unauthorized), 404 (model/endpoint not found) — because those are specific
// to that one provider and the next provider in the chain may well succeed. A
// 400/422 content error stays definitive: the request itself is bad and would
// fail identically downstream, so we don't spend money retrying it.
func shouldFallback(err error) bool {
	if err == nil {
		return false
	}
	if isInfraFailure(err) {
		return true
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		switch apiErr.HTTPStatusCode {
		case 401, 403, 404:
			return true
		}
	}
	return false
}
