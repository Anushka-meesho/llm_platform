# Prediction Flow

Sources: [llm_platform_go/internal/api/predict_core.go](../llm_platform_go/internal/api/predict_core.go), [llm_platform_go/internal/api/task_handlers.go](../llm_platform_go/internal/api/task_handlers.go)

## Endpoint

```
POST /v1/tasks/{task_id}/predict
Content-Type: application/json

{
  "inputs": { "field": "value", ... },
  "session_id": "optional-client-session-id"
}
```

## Full lifecycle

```
HTTP request
    │
    ├─ 1. Auth middleware (RequireAuth)
    ├─ 2. RBAC middleware (RequirePermission: PermTaskPredict)
    ├─ 3. Load task from registry
    │
    └─ executePrediction(task, inputs, session_id, user)
            │
            ├─ 4.  Input validation (JSON Schema)
            ├─ 5.  Prompt template rendering
            ├─ 6.  Multimodal image extraction
            ├─ 7.  Message array construction
            ├─ 8.  Model chain resolution
            ├─ 9.  Cache lookup (per primary model)
            │
            └─ 10. Fallback walk (llm.CallWithFallbackOpts)
                    │
                    ├─ (health gate) → skip if unhealthy
                    ├─ (cache lookup) → return if hit
                    ├─ (live call) → CallModel with retries
                    ├─ (schema validation) → validate or advance chain
                    └─ (success) → RecordSuccess on health tracker
            │
            ├─ 11. Cost calculation
            ├─ 12. Cache fill (on success)
            ├─ 13. Budget gate check
            └─ 14. Async DB write (RunWriter)
                    │
                    └─ HTTP 200 response
```

### Step 1-2: Auth and RBAC

The `RequireAuth` middleware validates the JWT from the HttpOnly cookie or `Authorization: Bearer` header and places a `User` on the request context. `RequirePermission(PermTaskPredict)` then checks that the user's role grants predict access. Callers, creators, approvers, and admins all have it; viewers do not.

### Step 3: Load task

The task is loaded from the in-memory registry (backed by the DB). Returns 404 if not found or the task is inactive.

### Step 4: Input validation

`tasks.ValidateInput(task, inputs)` compiles the task's `InputSchema` (JSON Schema draft 6) and validates the request inputs against it. Returns **422** on validation failure with field-level error details. If the task has no `InputSchema`, any inputs are accepted.

### Step 5: Prompt template rendering

`tasks.RenderPrompt(task, inputs)` executes the Go `text/template` with inputs as the data object. All declared input schema properties are pre-seeded with `""` so `{{if .field}}` guards work correctly. Template compilation errors return **400**.

### Step 6: Multimodal extraction

The fields `image` (single string) and `images` (array of strings) are extracted from the inputs and attached to the user message as a `[]string` of base64 data URLs or HTTPS URLs. Both base64 and URL forms are forwarded as-is to the provider.

### Step 7: Message construction

```
[
  {role: "system", content: task.SystemPrompt},   // omitted if empty
  {role: "user", content: renderedPrompt, images: [...]}
]
```

### Step 8: Model chain

The chain is `[task.Model] + task.FallbackModels` unless the call is a Studio test run, in which case the model can be overridden directly.

### Step 9 + fallback walk inner cache

Cache is checked twice: once before the walk for the primary model (shortcut that skips the whole walk) and again inside the walk for each fallback model before making a live call. This ensures a cached fallback result is served without re-calling the primary. See [10-caching-and-cost.md](10-caching-and-cost.md) for cache key details.

### Step 10: Fallback walk

Described fully in [05-fallback-chain.md](05-fallback-chain.md). The walk returns with:
- A successful response from whichever model in the chain answered first.
- `FallbackUsed = true` if answered by a non-primary model.
- An error if the whole chain exhausted.

### Step 11: Cost calculation

```
cost = (inputTokens / 1_000_000) * inputRate
     + (outputTokens / 1_000_000) * outputRate
```

Rates are loaded from `pricing.json` once at boot, keyed by the same friendly model name. Unknown models return cost `0.0` (never panics).

### Step 12: Cache fill

If the prediction succeeded and the output is schema-valid (or there is no output schema), a cache entry is stored with the task's configured TTL (default 24 hours). Cache writes are best-effort; a Redis failure does not fail the prediction.

### Step 13: Budget gate

If `task.DailyBudgetUSD > 0`, the accumulated spend for today is fetched from an in-memory cache (refreshed from DB at most every 5 seconds). If `currentSpend + thisCost > dailyBudget`, the prediction is rejected with **429 Budget Exceeded**. On success, `addSpend` increments the cached value immediately to cover async write lag.

### Step 14: Async DB write

A `RunRow` is enqueued to the `RunWriter` channel (non-blocking). If the channel is full the row is dropped and a counter incremented. The HTTP response is sent **before** the row is written to disk.

## RunRow fields

| Field | Source |
|-------|--------|
| `run_id` | UUID generated at prediction start |
| `session_id` | From request body or auto-generated |
| `prompt` | Rendered prompt text |
| `system_prompt` | Task system prompt |
| `image` | Images array (JSON-encoded) |
| `model` | Model that answered |
| `response` | Raw response text (null on error) |
| `latency_ms` | Wall-clock duration of the live call |
| `input_tokens` / `output_tokens` / `total_tokens` | From provider response |
| `cost_usd` | Calculated cost |
| `success` | 1 if succeeded, 0 if error |
| `error` | Error message (null on success) |
| `user_id` / `user_email` | From JWT claims |
| `task_id` | Task slug |
| `prompt_version` | Task's current prompt version |
| `provider` | Backend that served it (openai, groq, etc.) |
| `fallback_used` | 1 if served by non-primary model |
| `cache_hit` | 1 if served from cache |
| `is_test` | 1 if Studio test run |
| `created_at` | UTC timestamp |

## Error responses

| Code | Cause |
|------|-------|
| 400 | Template compile or render error; bad request body |
| 401 | Missing or invalid JWT |
| 403 | Role lacks PermTaskPredict |
| 404 | Task not found or inactive |
| 422 | Input schema validation failed |
| 429 | Daily budget exceeded |
| 5xx | All models in chain failed (last provider error forwarded) |
