package llm

import (
	"context"
	"log"
	"time"
)

// Recovery prober — the "check whether a higher-priority model is back"
// half of the fallback algorithm.
//
// With probe-only breakers, production requests NEVER touch a provider whose
// circuit is open: they fail fast (<1ms) to the next model in the task's
// chain, so a sick provider costs latency exactly once (the failures that
// tripped the breaker) and never again while it's down. This loop is what
// re-engages it: every interval it sends a tiny 1-token request to each
// unhealthy provider; a successful probe closes the circuit, and because
// CallWithFallback walks the chain from the front on every request, the very
// next prediction is back on the highest-priority healthy model.

// probeTimeout bounds one health-check call. No retries — the next tick
// probes again anyway.
const probeTimeout = 5 * time.Second

// StartRecoveryProber switches the process breakers to probe-only mode and
// launches the background probe loop. Cancel ctx to stop it (server shutdown).
func StartRecoveryProber(ctx context.Context, clients *Clients, interval time.Duration) {
	defaultBreakers.SetProbeOnly(true)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				probeUnhealthy(ctx, clients)
			}
		}
	}()
}

// probeUnhealthy health-checks every provider whose circuit is not closed.
func probeUnhealthy(ctx context.Context, clients *Clients) {
	targets := probeTargets(clients)
	for _, providerName := range defaultBreakers.Unhealthy() {
		t, ok := targets[providerName]
		if !ok {
			continue // no configured client — nothing to probe
		}

		probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
		_, err := t.client.Call(probeCtx, &chatRequest{
			Model:     t.modelID,
			Messages:  []ChatMessage{{Role: "user", Content: "ping"}},
			MaxTokens: 1,
		})
		cancel()

		// A completed exchange — success or a 4xx config error — proves the
		// provider is reachable and serving; only infra failures keep it open.
		if !isInfraFailure(err) {
			defaultBreakers.RecordSuccess(providerName)
			log.Printf("recovery prober: provider %s is healthy again — circuit closed, traffic returns to it", providerName)
		}
	}
}

type probeTarget struct {
	client  Provider
	modelID string
}

// probeTargets maps breaker keys (provider attribution names) to a configured
// client + one concrete model ID to probe with, derived from the routing
// registry so new providers get probed automatically.
func probeTargets(clients *Clients) map[string]probeTarget {
	out := map[string]probeTarget{}
	for _, cfg := range registry {
		if c := cfg.clientFn(clients); c != nil {
			out[cfg.provider] = probeTarget{client: c, modelID: cfg.modelID}
		}
	}
	return out
}
