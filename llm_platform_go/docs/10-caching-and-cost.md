# 10 — Caching and Cost Tracking

## Why cache predictions?

LLM API calls are:
- **Expensive:** A GPT-4o call costs ~$0.002. If 1,000 users ask the same thing, that's $2.00. Cached: $0.
- **Slow:** A typical response takes 500ms–3s. Cached: <5ms.
- **Deterministic for structured tasks:** The same ticket body, same prompt, same model almost always produces the same classification. There's no benefit in calling the LLM again.

The prediction cache stores responses so identical requests return the stored answer instantly.

---

## The Cache interface

```go
type Cache interface {
    Get(ctx context.Context, key string) ([]byte, bool)
    Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
    Close() error
}
```

> **🔤 Go concept: interfaces for swappable backends**
> The `Cache` interface means the rest of the platform doesn't know or care whether it's talking to Redis or an in-memory map. Both `redis.Cache` and `memory.Cache` implement the same two methods. In production: Redis. In local dev: in-memory. In tests: in-memory.
>
> This is the **strategy pattern**: define a contract (interface), then swap implementations without changing calling code.

Two backends:
- **`cache.NewMemory()`** — an in-process map with TTL expiry. Data is lost when the server restarts. Fine for development.
- **`cache.NewRedis(addr, password, db)`** — a Redis server. Persistent across restarts, shared across multiple server instances.

> **Why not memory in production?** If you run 5 server instances, each has its own in-memory cache. An identical request to instance 1 doesn't benefit from the cache entry in instance 3. Redis is shared across all instances.

---

## What gets cached

A **cache entry** stores everything needed to serve the response without calling the LLM again:

```go
type Entry struct {
    Model        string    // "gpt-4o"
    Provider     string    // "openai"
    RawResponse  string    // the model's raw text output
    Output       json.RawMessage  // parsed JSON if schema-validated
    OutputValid  *bool     // nil if no output schema; true/false if validated
    InputTokens  int       // preserved for observability (reported as 0 cost)
    OutputTokens int
    TotalTokens  int
    CachedAt     time.Time
}
```

When a cache hit is served, `cost_usd = 0.0` in the response — no tokens were consumed. But `input_tokens` and `output_tokens` are preserved so dashboards show actual token counts (useful for sizing).

---

## The cache key: making identical requests unique

Two requests are "identical" and should share a cache entry if and only if every factor that could influence the output is the same. The cache key is a SHA-256 hash of all those factors concatenated:

```go
func cacheKey(task *tasks.Task, model, renderedPrompt, systemPrompt string,
              temperature float64, maxTokens int, outputSchema json.RawMessage,
              images []string) string {

    h := sha256.New()
    fmt.Fprintf(h, "%s\x00", task.ID)
    fmt.Fprintf(h, "%d\x00", task.PromptVersion)  // version number
    fmt.Fprintf(h, "%s\x00", renderedPrompt)       // AFTER template rendering
    fmt.Fprintf(h, "%s\x00", systemPrompt)
    fmt.Fprintf(h, "%s\x00", model)
    fmt.Fprintf(h, "%f\x00", temperature)
    fmt.Fprintf(h, "%d\x00", maxTokens)
    fmt.Fprintf(h, "%s\x00", string(outputSchema))
    for _, img := range images {
        fmt.Fprintf(h, "%s\x00", img)
    }
    return hex.EncodeToString(h.Sum(nil))
}
```

### Why each field is in the key:

| Field | Why it's in the key |
|-------|---------------------|
| `task.ID` | Different tasks have different prompts — never share entries |
| `task.PromptVersion` | Deploying a new prompt version means old answers may be wrong — auto-invalidate |
| `renderedPrompt` | Different inputs → different rendered prompt → different expected output |
| `systemPrompt` | Part of what shapes the model's response |
| `model` | gpt-4o and gemini give different answers |
| `temperature` | Same prompt at 0.1 vs. 0.9 → different outputs |
| `maxTokens` | A different length limit could truncate the response differently |
| `outputSchema` | Different schema = different validation criteria = potentially different fallback behaviour |
| `images` | Multimodal inputs are part of the input |

> **Why hash?** The raw concatenation of all these fields could be thousands of characters long (the prompt might be 2,000 tokens). Redis keys have a maximum length, and long keys are slow to compare. A SHA-256 hash is always exactly 64 characters.

---

## Per-model caching (not per-chain)

The cache is consulted **as the fallback walk reaches each model**, not before the walk starts. The lookup function is passed as a hook:

```go
opts.Lookup = func(model string) (ModelResult, bool) {
    entry, ok := cache.Get(ctx, cacheKeyForModel(task, model, ...))
    // ...
    return result, ok
}
```

During the walk, when the chain reaches "gpt-4o", it calls `Lookup("gpt-4o")`. If cached → return immediately. If not → call live. The same applies to each fallback model.

**Why per-model, not per-chain?** The chain configuration (which model is primary, which is fallback) is live config that can change. If you cached the whole chain result under "this task's current chain", then adding a new primary model wouldn't invalidate the old cache entry — you'd keep serving the old model's answer. Per-model caching is stable: a gpt-4o answer for a given input is gpt-4o's answer regardless of what the chain looks like.

**Also:** if the primary was unhealthy and the fallback served the request, the result is cached under the *fallback's* key (gemini-flash), not the primary's (gpt-4o). When gpt-4o recovers and becomes primary again, its cache is cold — it'll be called fresh. You'll always get the primary model's answer once it's healthy, never a stale fallback answer pretending to be the primary.

---

## When caching is skipped

- **Studio test runs (`is_test=true`):** When a product builder is testing a draft prompt in the Studio panel, they always want to see the live model response — not a cached answer from the old deployed prompt.
- **Cache disabled (`cache_enabled=false`):** Each task can individually opt out of caching.
- **No cache configured:** If `CACHE_BACKEND=off` (the default when Redis isn't configured), the cache lookup function always returns "miss".

---

## Token counting and pricing

LLM providers report token counts in every response:

```json
{
  "choices": [...],
  "usage": {
    "prompt_tokens": 127,
    "completion_tokens": 48
  }
}
```

The platform maps this to dollar cost using `pricing.json`:

```json
{
  "gpt-4o":      { "input_per_1m": 2.500, "output_per_1m": 10.000 },
  "gpt-4o-mini": { "input_per_1m": 0.150, "output_per_1m":  0.600 },
  "llama-groq":  { "input_per_1m": 0.050, "output_per_1m":  0.080 }
}
```

The calculation:

```go
func CalculateCost(model string, inputTokens, outputTokens int) float64 {
    rate, ok := pricingTable[model]
    if !ok {
        return 0  // unknown model → $0 (this is a money-path regression!)
    }
    cost := float64(inputTokens)/1_000_000 * rate.InputPer1M +
            float64(outputTokens)/1_000_000 * rate.OutputPer1M
    return math.Round(cost*1_000_000) / 1_000_000  // round to 6 decimal places
}
```

**Example:** 127 input tokens + 48 output tokens with gpt-4o:
```
(127 / 1,000,000) × $2.50 + (48 / 1,000,000) × $10.00
= $0.000318 + $0.000480
= $0.000798
```

> **Important:** If a model is in the registry but not in `pricing.json`, its cost is reported as `$0.00`. This is why the test `TestRegistryModelsArePricedAndAttributed` exists — it catches models missing from the pricing file before they silently zero out costs.

---

## Daily budget tracking

Each task can have a `daily_budget_usd`. When set, predictions for that task are blocked once the daily spend reaches the limit.

### The spend cache

Querying `SUM(cost_usd) WHERE task_id=? AND created_at > midnight` on every prediction would be expensive. Instead, there's a per-task in-memory spend cache:

```go
type spendCache struct {
    mu       sync.Mutex
    entries  map[string]*spendEntry
}

type spendEntry struct {
    total      float64    // DB-refreshed base + local increments
    refreshedAt time.Time // when we last queried the DB
}
```

**How it works:**
1. First prediction for a task today: query DB for today's total, cache it with a timestamp.
2. Add this prediction's cost to the local cache.
3. Next prediction: return cache (if < 5 seconds old) OR refresh from DB.
4. The async RunWriter hasn't written this row yet — but the local increment covers it.

**Why 5-second refresh?** If two server instances are running, they each have their own spend cache. Instance A might have spent $4.98, instance B doesn't know about it. With a 5-second refresh and each prediction adding its cost locally, the worst-case overspend is: (5s worth of predictions) × (max cost per prediction). For most tasks this is pennies. Truly strict budget enforcement would require a distributed atomic counter (Redis `INCRBYFLOAT`).

---

## A quick summary of what costs what

| Event | `cost_usd` in DB | `input_tokens` / `output_tokens` |
|-------|-----------------|----------------------------------|
| Successful LLM call | Calculated | From provider response |
| Cache hit | `0.00` | Preserved from original call |
| Failed call (5xx) | `0.00` | `0` |
| Fallback call | Calculated (the fallback model's rate) | From provider response |
| Test panel run | Calculated | From provider response |
