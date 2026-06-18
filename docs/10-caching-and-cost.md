# Caching and Cost

Sources: [llm_platform_go/internal/cache/cache.go](../llm_platform_go/internal/cache/cache.go), [llm_platform_go/internal/llm/pricing.go](../llm_platform_go/internal/llm/pricing.go), [llm_platform_go/internal/api/budget_cache.go](../llm_platform_go/internal/api/budget_cache.go)

## Prediction cache

**What it does:** Identical predictions — same task, same rendered prompt, same model, same parameters — are served from cache instead of calling the provider. No tokens are consumed, no cost is incurred.

**Opt-in:** `task.CacheEnabled` must be `true`. Off by default.

**Backends:**

| Backend | When active | Use case |
|---------|-------------|----------|
| Redis | `REDIS_ADDR` is set | Production |
| In-process memory | `CACHE_BACKEND=memory` | Local dev / testing |
| Off | Neither of the above | Default (no caching) |

Redis failure at startup is non-fatal — the server continues without cache.

## Cache key

A cache entry is identified by a SHA-256 digest of nine inputs serialised deterministically to JSON:

```go
type KeyInputs struct {
    TaskID         string   // task slug
    PromptVersion  int      // prompt version number
    Model          string   // routing key (e.g. "gpt-4o-mini")
    SystemPrompt   string
    RenderedPrompt string   // fully rendered template with all inputs substituted
    Temperature    float64
    MaxTokens      int
    OutputSchema   string   // raw JSON; "" if no schema
    Images         []string // in submission order
}
```

Key format: `"predict:" + hex(sha256(json(KeyInputs)))`

**Why nine fields?**

Every field that could produce a different output is included in the key. Changing any of them produces a different hash and misses the cache:
- `PromptVersion` — deploying a new prompt version automatically invalidates all cached responses from the old version.
- `Model` — each fallback model has its own entry. Cache is checked per model during the fallback walk, in order.
- `Temperature` / `MaxTokens` — same prompt at different settings may produce different outputs.
- `OutputSchema` — schema-validated and unvalidated responses are different shapes.
- `Images` — multimodal inputs affect the response.

## Cache invalidation

There is no explicit purge. Invalidation is **implicit via key change**:

| Change | Effect on cache |
|--------|----------------|
| Deploy new prompt version | `PromptVersion` increments → new key → old entries expire naturally |
| Change primary model | `Model` differs → new key |
| Edit temperature or max_tokens | Fields differ → new key |
| Edit output schema | `OutputSchema` differs → new key |
| Change inputs | `RenderedPrompt` differs → new key |

Old entries remain in the cache until they expire (TTL) but are never matched again.

## Cache entry

```go
type Entry struct {
    Model        string
    Provider     string
    RawResponse  string          // model's raw text response
    Output       json.RawMessage // parsed + validated JSON (or null)
    OutputValid  *bool           // nil if no schema; true/false if schema present
    InputTokens  int
    OutputTokens int
    TotalTokens  int
    CachedAt     time.Time
}
```

**Usage fields** are stored so you can see the original token consumption even though the cache hit cost zero.

**Serving a cache hit:**
- `cost_usd = 0` (no tokens consumed).
- `cache_hit = 1` in the run row.
- `latency_ms` reflects only the cache lookup latency, not a provider round-trip.

**Default TTL:** 24 hours. Override per task with `cache_ttl_seconds`.

## Token cost calculation

```go
func CalculateCost(model string, inputTokens, outputTokens int) float64 {
    r, ok := pricingTable[model]
    if !ok {
        return 0.0  // unknown model: free, no panic
    }
    cost := (float64(inputTokens)/1_000_000)*r.InputPer1M +
            (float64(outputTokens)/1_000_000)*r.OutputPer1M
    return math.Round(cost*1_000_000) / 1_000_000  // 6 decimal places
}
```

**Pricing table** is loaded from `pricing.json` once at boot. The same file is served to the frontend via `GET /pricing` — one source of truth for rates.

**Rate structure per model:**
```json
{
  "gpt-4o-mini": { "input_per_1m": 0.15, "output_per_1m": 0.60 },
  "gemini-2.5-flash": { "input_per_1m": 0.075, "output_per_1m": 0.30 },
  "llama-groq": { "input_per_1m": 0.05, "output_per_1m": 0.08 }
}
```

**Rounding to 6 decimal places** avoids floating-point drift while preserving sub-cent accuracy (one millionth of a dollar = $0.000001).

## Daily budget gate

Each task can have a `DailyBudgetUSD`. When set, predictions are rejected if they would push the day's spend over the limit.

**How it works:**

1. `currentSpend(taskID)` queries `SUM(cost_usd)` for the task where `created_at >= today UTC`. The result is cached in memory and refreshed at most every 5 seconds (avoids a DB hit on every prediction).
2. Before sending to the provider: if `currentSpend + estimatedCost > DailyBudgetUSD` → reject with **429 Budget Exceeded**.
3. After a successful prediction: `addSpend(taskID, cost)` increments the in-memory value immediately, before the async DB write completes. This prevents a burst of concurrent predictions from all seeing stale spend and all going through.

**UTC day boundary:** The spend window resets at midnight UTC. `TaskSpendToday` filters by `DATE(created_at) = DATE('now')`.

## Observability

**Cache hit rate:**
```sql
SELECT
    COUNT(*) FILTER (WHERE cache_hit = 1) * 1.0 / COUNT(*) AS hit_rate,
    SUM(CASE WHEN cache_hit = 0 THEN cost_usd ELSE 0 END) AS actual_cost,
    SUM(CASE WHEN cache_hit = 1 THEN cost_usd ELSE 0 END) AS saved_cost
FROM runs
WHERE task_id = 'my-task'
  AND created_at >= DATE('now', '-7 days');
```

**Daily spend:**
```sql
SELECT DATE(created_at) AS day, SUM(cost_usd) AS cost, COUNT(*) AS runs
FROM runs
WHERE task_id = 'my-task'
GROUP BY day
ORDER BY day DESC;
```
