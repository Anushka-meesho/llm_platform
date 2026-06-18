# Circuit Breaker

Sources: [llm_platform_go/internal/llm/breaker.go](../llm_platform_go/internal/llm/breaker.go), [llm_platform_go/internal/llm/prober.go](../llm_platform_go/internal/llm/prober.go), [llm_platform_go/internal/health/tracker.go](../llm_platform_go/internal/health/tracker.go)

The platform has two independent circuit breaker layers. They operate at different granularities and protect different things.

## Layer 1 — Provider-wide breaker

**Where:** `internal/llm/breaker.go`, checked inside `CallModel` before every live call.

**Keyed on:** Provider name (`"openai"`, `"groq"`, `"gemini"`, `"anthropic"`).

**Scope:** All traffic across all tasks for that provider.

**State machine:**

```
Closed ──(3 consecutive infra failures)──► Open
  ▲                                          │
  │                                          │ (30s cooldown)
  │                                          ▼
  └──(probe succeeds)────────────────── Half-Open
                                             │
                                        (probe fails)
                                             │
                                             ▼
                                           Open (restart cooldown)
```

**Behaviour:**
- **Closed** → requests pass through normally.
- **Open** → `CallModel` returns an error immediately without making a network call. The fallback walk treats this as a fallback-eligible error and advances to the next model.
- **Half-Open** → exactly one probe is allowed through; outcome determines next state.

**Trip condition:** 3 consecutive infrastructure failures (429, 5xx, network error, timeout) on calls to that provider. A successful response resets the counter.

## Layer 2 — Per-(task, model) health gate

**Where:** `internal/health/tracker.go`, consulted by the fallback walk as the `Gate` hook.

**Keyed on:** `(task_id, model)` pair.

**Scope:** Production predictions for that specific task only. Test runs and shadow comparisons bypass this gate.

**State machine:**

```
Healthy ──(3 failures)──► Unhealthy
   ▲                           │
   │                           │ (cooldown: 30s → 1m → 2m → ... → 30m)
   │                           ▼
   └──(probe succeeds)──── Probing
                               │
                          (probe fails)
                               │
                               ▼
                           Unhealthy (cooldown doubles)
```

**Difference from Layer 1:**
- Exponential backoff: each re-trip doubles the cooldown, capped at `HEALTH_MAX_COOLDOWN` (default 30 min). A repeatedly flapping model backs off further each time.
- Trips on **schema validation failure** in addition to infra errors. If a model keeps returning output that doesn't match the task's output schema, it is marked unhealthy for that task.
- State is scoped per task — if `gpt-4o` is unhealthy for task `sentiment-analysis`, it still serves other tasks normally.

**Behaviour:**
- **Healthy** → `Gate.Allow(model)` returns true; walk proceeds normally.
- **Unhealthy** → `Gate.Allow` returns false; walk skips the model entirely (no network call).
- **Probing** → `Gate.Allow` returns true for exactly one trial call; success → Healthy, failure → Unhealthy with doubled cooldown.

## Background recovery prober

**Where:** `internal/llm/prober.go`, started by `llm.StartRecoveryProber`.

**Interval:** 15 seconds (hardcoded at startup in `main.go`).

**What it does:** Every 15 seconds, queries the provider breaker for all currently-open circuits and sends each a 1-token "ping" request. The probe uses a 5-second timeout and no retries.

**Why this matters:** Production requests fail fast when a provider is open — they never hit an open circuit. Without the prober, a tripped circuit would stay open forever. The prober is the **only mechanism** that closes a provider circuit; the fallback walk never does.

**Probe outcome:**
- Any HTTP 2xx or 4xx (including 429) → `RecordSuccess(provider)` → circuit closes immediately.
- Network error, 5xx, or timeout → circuit stays open; try again next tick.

The choice to treat 4xx as success is intentional: a 401 means the provider is reachable (auth is wrong, not the server). A 5xx means the server is overloaded; wait.

## How the two layers interact

During a prediction, the fallback walk checks both layers:

```
for each model in chain:
    1. Layer 2: Gate.Allow(task, model)?
       └─ No → skip (no call). Advance chain.

    2. Layer 1 (inside CallModel): provider circuit open?
       └─ Yes → fail fast (no call). Eligible → advance chain.

    3. Make live call.
```

A model can be blocked by either layer independently:
- Layer 1 open, Layer 2 healthy → blocked (provider-wide issue).
- Layer 1 closed, Layer 2 unhealthy → blocked (this task's history with this model is bad).
- Both open → blocked.
- Both closed/healthy → call proceeds.

Layer 2 is checked first because it is cheaper (in-memory map lookup). If Layer 2 skips the model, Layer 1 is never consulted.

## Observability

Every Layer 2 state transition (tripped, recovered, manually reset) is written to the `model_health_events` table via the `HealthEventWriter`. This lets you query the history of a model's health for a given task:

```sql
SELECT event, reason, consecutive_failures, cooldown_ms, created_at
FROM model_health_events
WHERE task_id = 'my-task' AND model = 'gpt-4o'
ORDER BY created_at DESC;
```
