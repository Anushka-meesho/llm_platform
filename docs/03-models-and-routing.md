# Models and Routing

Sources: [llm_platform_go/internal/llm/client.go](../llm_platform_go/internal/llm/client.go), [llm_platform_go/internal/llm/runner.go](../llm_platform_go/internal/llm/runner.go)

## The Provider interface

Every LLM backend implements one interface:

```go
type Provider interface {
    Call(ctx context.Context, req *chatRequest) (*chatResponse, error)
}
```

`chatRequest` carries messages, temperature, max tokens, and an optional JSON schema for structured output. `chatResponse` carries the raw text, token counts, and finish reason. The wire format is OpenAI chat completions — Groq, Gemini, and Anthropic all speak it through Meesho's gateway.

## Two clients, not one per provider

The platform has exactly two HTTP clients:

| Client | Auth | Covers |
|--------|------|--------|
| **Groq** | `Authorization: Bearer <GROQ_API_KEY>` | Groq models directly |
| **Meesho gateway** | `x-bf-vk: <MEESHO_GATEWAY_VK>` | OpenAI, Gemini, Anthropic — all through one internal endpoint |

Both share a 120-second HTTP timeout. Request cancellation (user timeout, context deadline) is handled via the `ctx` passed to `Call` — the HTTP client respects it automatically.

## The registry map

`runner.go` holds a package-level map from **friendly routing key** to **provider config**:

```go
var registry = map[string]providerConfig{
    // OpenAI via Meesho bifrost
    "gpt-4o":             {modelID: "openai/gpt-4o",          provider: "openai",    clientFn: meeshoC},
    "gpt-4o-mini":        {modelID: "openai/gpt-4o-mini",     provider: "openai",    clientFn: meeshoC},

    // Groq direct
    "llama-groq":         {modelID: "llama-3.3-70b-versatile", provider: "groq",     clientFn: groqC},

    // Gemini via Meesho
    "gemini-2.5-pro":     {modelID: "vertex/gemini-2.5-pro",  provider: "gemini",    clientFn: meeshoC},
    "gemini-2.5-flash":   {modelID: "vertex/gemini-2.5-flash", provider: "gemini",   clientFn: meeshoC},

    // Anthropic via Meesho
    "claude-sonnet-4-6":  {modelID: "anthropic/claude-sonnet-4-6", provider: "anthropic", clientFn: meeshoC},
}
```

**Why friendly keys vs. model IDs?**

- The task config (`model: "gpt-4o-mini"`) is decoupled from the provider's actual versioning scheme. When OpenAI releases `gpt-4o-mini-2025-06`, you update the registry in one place — no task configs change.
- The `provider` field records which backend served the prediction. This appears in the `runs` table for observability (e.g., how many predictions went through Groq vs. the gateway).
- `clientFn` is a function pointer into the `Clients` struct, which means routing rules (A/B test, shadow routing) can be added by wrapping a `clientFn` without touching calling code.

**`KnownModel(key)`** returns true if the key is in this map. Tasks are validated against it at registration time, so a typo in a task config is caught immediately rather than at prediction time.

## Default models

The playground `/run` endpoint fans out to these three by default when no model override is given:

```go
var DefaultModels = []string{"gpt-4o-mini", "gemini-2.5-flash", "llama-groq"}
```

This covers all three provider paths (gateway-OpenAI, gateway-Gemini, Groq direct) so a single playground run exercises the full routing surface.

## Multimodal content

`ChatMessage` supports images alongside text:

```go
type ChatMessage struct {
    Role    string
    Content string
    Images  []string  // base64 data URLs or https:// URLs
}
```

When marshalling to JSON for the wire:
- **No images** → `content` is a plain string (backwards-compatible with all providers).
- **With images** → `content` becomes an array: `[{type: "text", text: "..."}, {type: "image_url", image_url: {url: "..."}}, ...]`. This format is understood by GPT-4o, Gemini, and Claude through the gateway.

Images are extracted from the task input fields named `image` (single) or `images` (array) in `predict_core.go` before the message is built.

## Adding a new model

1. Add an entry to `registry` in `runner.go` with a friendly key, the provider's actual model ID, the provider name, and the right `clientFn`.
2. Add the model's token pricing to `pricing.json`.
3. The model is now available as `model` or in `fallback_models` in any task config.

No other code changes are needed.
