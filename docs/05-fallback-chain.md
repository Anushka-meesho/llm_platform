# Fallback Chain

Source: [llm_platform_go/internal/llm/fallback.go](../llm_platform_go/internal/llm/fallback.go)

## Entry point

```go
CallWithFallbackOpts(
    ctx      context.Context,
    clients  *Clients,
    models   []string,          // ordered: primary first, fallbacks after
    messages []ChatMessage,
    temperature float64,
    maxTokens   int,
    opts     FallbackOptions,
) (FallbackResult, error)
```

## Hooks

The caller injects three optional hooks through `FallbackOptions`:

```go
type FallbackOptions struct {
    Lookup   ModelCacheLookup  // check cache before calling
    Gate     HealthGate        // skip unhealthy (task, model) pairs
    Validate OutputValidator   // check response passes output schema
}
```

Any hook can be `nil`; the walk skips that check.

## Walk logic

For each model in `models`, in order:

```
1. Health gate
   └─ Gate.Allow(model) == false?  → skip model (no call)
                                      mark FallbackUsed=true if past index 0

2. Cache lookup
   └─ Lookup(model) hit?  → return cached result immediately
                             mark FallbackUsed if past index 0

3. Live call
   └─ CallModel(ctx, clients, model, messages, temperature, maxTokens)
         └─ retries up to 3x on 429 / 5xx with exponential backoff

4. Success path
   ├─ Validate != nil?
   │     └─ Validate(response) == false?
   │           → Gate.RecordFailure(model, "schema invalid")
   │           → continue to next model (this counts as fallback)
   └─ Gate.RecordSuccess(model)
      return result

5. Failure path
   ├─ fallbackEligible(err)?
   │     → Gate.RecordFailure(model, reason)
   │     → context cancelled?  stop walk
   │     → continue to next model
   └─ NOT eligible (content error)
         → return error immediately (retrying won't help)
```

After the loop:
- All models skipped by health gate → return `"all models unhealthy"` error.
- Loop exhausted → return last error with `Degraded = true`.

## Fallback eligibility

The key decision: should this error advance the chain, or stop immediately?

| Error type | Fallback eligible? | Reason |
|---|:---:|---|
| HTTP 429 rate limit | Yes | Infrastructure; try next provider |
| HTTP 5xx server error | Yes | Infrastructure; try next provider |
| Network error / timeout | Yes | Infrastructure; try next provider |
| Malformed / unparseable response | Yes | Infrastructure; try next provider |
| HTTP 401 Unauthorized | Yes | Provider config error; different provider may work |
| HTTP 403 Forbidden | Yes | Provider config error; different provider may work |
| HTTP 404 Not Found (model/endpoint) | Yes | Provider config error; different provider may work |
| Schema validation failure | Yes | This model produced wrong output; try next |
| HTTP 400 Bad Request | **No** | Content error — the request is malformed; retrying is futile |
| HTTP 422 Unprocessable | **No** | Content error — same as 400 |

**Why stop on content errors?** A 400 means the provider rejected the request content itself (unsafe prompt, unsupported feature). Sending the same content to the next model will produce the same rejection. Advancing the chain would waste latency and tokens.

## Result

```go
type FallbackResult struct {
    Model        string
    Provider     string
    RawResponse  string
    Output       json.RawMessage
    OutputValid  *bool
    InputTokens  int
    OutputTokens int
    TotalTokens  int
    FallbackUsed bool   // true if served by any model other than models[0]
    Degraded     bool   // true if FallbackUsed OR chain failed
    CacheHit     bool
}
```

## Worked example

Task model chain: `["gpt-4o", "gpt-4o-mini", "llama-groq"]`

```
→ gpt-4o
  live call → HTTP 429 (rate limit)
  eligible → RecordFailure("gpt-4o"), continue

→ gpt-4o-mini
  live call → HTTP 401 (Meesho gateway auth error)
  eligible → RecordFailure("gpt-4o-mini"), continue

→ llama-groq
  health gate → Allow = true
  cache lookup → miss
  live call → 200 OK
  validate → output schema valid
  RecordSuccess("llama-groq")
  return result (FallbackUsed=true, Degraded=true, Model="llama-groq", Provider="groq")
```

The caller sees a successful response. `FallbackUsed=true` is recorded in the `runs` table for observability — you can query what fraction of predictions fell back.

## Retry behaviour inside CallModel

Before advancing the chain, `CallModel` itself retries the same model up to 3 times on transient 429 and 5xx errors, with exponential backoff. The fallback chain only advances when retries are exhausted or the error is not retryable.
