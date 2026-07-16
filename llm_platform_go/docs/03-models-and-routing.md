# 03 — Models and Routing

## The core question: how does "gpt-4o" become an HTTP call?

When a handler says "call gpt-4o with this message", several things need to happen:
1. Translate the friendly name `"gpt-4o"` into the real model ID the API expects.
2. Figure out *which* HTTP endpoint to send the request to (Meesho gateway? Groq?).
3. Set the correct authentication header for that endpoint.
4. Format the request body.
5. Parse the response.

The `internal/llm/` package handles all of this. Here's how.

---

## The Provider interface

The single most important design decision in this package is the `Provider` interface:

```go
type Provider interface {
    Call(ctx context.Context, req *chatRequest) (*chatResponse, error)
}
```

> **🔤 Go concept: interfaces**
> An **interface** in Go is a contract. It says: "any type that has these methods qualifies as this interface." Here, anything that has a `Call(...)` method is a `Provider`.
>
> The key insight: Go's interfaces are *implicit*. You don't write `implements Provider` anywhere. If your type has the right method, it automatically satisfies the interface. This makes adding new providers trivial — you write the implementation, and it just works.

**Why an interface?** The rest of the platform calls `provider.Call(...)` without knowing or caring whether the HTTP call is going to Groq, Meesho, or a test fake. This pattern is called "dependency inversion" — the caller depends on a contract (interface), not a concrete implementation.

> **Why not?** You could hard-code separate functions: `callOpenAI(...)`, `callGroq(...)`, `callGemini(...)`. That works until you have 10 providers and every call site has a switch statement. The interface removes that duplication.

---

## The `openAICompatProvider`

The only concrete `Provider` implementation right now is `openAICompatProvider`:

```go
type openAICompatProvider struct {
    baseURL    string       // e.g. "http://llm-gateway.prd.meesho.int/v1"
    apiKey     string       // the API key or virtual key
    authHeader string       // "" = "Authorization: Bearer", "x-bf-vk" = Meesho header
    client     *http.Client // the shared HTTP client (120s timeout)
}
```

Its `Call` method:
1. Marshals the `chatRequest` struct to JSON.
2. Sends `POST {baseURL}/chat/completions`.
3. Sets the auth header (either `Authorization: Bearer {key}` or a custom header for Meesho).
4. Reads and parses the response JSON.
5. On non-200 responses, parses the error body and returns an `*APIError`.

> **🔤 Go concept: `*http.Client`**
> `*http.Client` is a pointer to an HTTP client. The `*` means "pointer" — instead of copying the whole struct, we pass a reference to the one shared instance. A single shared HTTP client reuses TCP connections across requests (connection pooling), which is much faster than opening a new connection for every API call.

The Meesho gateway uses a custom auth header (`x-bf-vk`) instead of the standard `Authorization: Bearer`. Setting `authHeader: "x-bf-vk"` handles this:

```go
if p.authHeader != "" {
    httpReq.Header.Set(p.authHeader, p.apiKey)  // x-bf-vk: sk-bf-...
} else {
    httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
}
```

---

## The `Clients` struct

```go
type Clients struct {
    Groq   Provider  // Groq API — used for llama-groq
    Meesho Provider  // Meesho bifrost gateway — used for gpt-4o, gemini-*, claude-*
}
```

> **Why a struct instead of a map?** A `map[string]Provider` would also work, but a struct has named fields. Named fields mean typos are caught at compile time (`clients.Groke` doesn't compile; `clients["groke"]` silently returns nil). For a small, fixed set of providers, the struct is safer.

`BuildClients(cfg)` creates one `openAICompatProvider` for each field:

```go
func BuildClients(cfg *config.Config) *Clients {
    return &Clients{
        Groq: &openAICompatProvider{
            baseURL: cfg.GroqBaseURL,
            apiKey:  cfg.GroqKey,
            client:  sharedHTTPClient,
        },
        Meesho: &openAICompatProvider{
            baseURL:    cfg.MeeshoGatewayBaseURL,
            apiKey:     cfg.MeeshoGatewayVK,
            authHeader: "x-bf-vk",
            client:     sharedHTTPClient,
        },
    }
}
```

---

## The registry: single source of truth

```go
var registry = map[string]providerConfig{
    // ── OpenAI via Meesho bifrost ──────────────────────────
    "gpt-4o":      {modelID: "openai/gpt-4o",      provider: "openai",    clientFn: meeshoC},
    "gpt-4o-mini": {modelID: "openai/gpt-4o-mini",  provider: "openai",    clientFn: meeshoC},

    // ── Groq (direct API) ──────────────────────────────────
    "llama-groq":  {modelID: "llama-3.3-70b-versatile", provider: "groq", clientFn: groqC},

    // ── Gemini via Meesho bifrost ──────────────────────────
    "gemini-2.5-pro":   {modelID: "vertex/gemini-2.5-pro",   provider: "gemini", clientFn: meeshoC},
    "gemini-2.5-flash": {modelID: "vertex/gemini-2.5-flash",  provider: "gemini", clientFn: meeshoC},

    // ── Anthropic via Meesho bifrost ──────────────────────
    "claude-sonnet-4-6": {modelID: "anthropic/claude-sonnet-4-6", provider: "anthropic", clientFn: meeshoC},
}
```

> **🔤 Go concept: map literals**
> `map[string]providerConfig{...}` creates a map where each key is a `string` (the friendly model name) and each value is a `providerConfig` struct. The curly-brace content initializes the map at the point of declaration — this is called a "map literal".

Each entry has three fields:

| Field | Type | Purpose |
|-------|------|---------|
| `modelID` | string | The actual model identifier sent in the API request |
| `provider` | string | Attribution name for logging/cost tracking ("openai", "groq", "gemini", "anthropic") |
| `clientFn` | `func(*Clients) Provider` | A function that picks the right provider from the Clients struct |
| `reasoning` | bool | For OpenAI reasoning-family models that need `max_completion_tokens` and default temperature |
| `minOutputTokens` | int | For thinking-heavy models (Gemini 2.5) where too-low `max_tokens` leaves no room for the answer |

The `clientFn` field is the clever part:

```go
var (
    groqC   = func(c *Clients) Provider { return c.Groq }
    meeshoC = func(c *Clients) Provider { return c.Meesho }
)
```

> **🔤 Go concept: functions as values**
> In Go, functions are first-class values — you can store them in variables, pass them as arguments, and put them in structs. `groqC` is not a function call; it's a variable that *holds* a function. Later, when we need to call Groq, we say `cfg.clientFn(clients)` which calls the stored function with our clients struct and gets back the Groq provider.

This is why all Groq models point to `clientFn: groqC` and all Meesho-gateway models point to `clientFn: meeshoC`. Adding a new provider means:
1. Add a field to `Clients`.
2. Add a `xxxC` variable.
3. Point new registry entries to it.

---

## How `CallModel` uses the registry

The full call flow:

```go
func CallModel(ctx context.Context, clients *Clients, modelName string,
               messages []ChatMessage, temperature float32, maxTokens int) ModelResult {

    // 1. Look up the registry
    cfg, ok := registry[modelName]
    if !ok {
        return ModelResult{Error: strPtr("unknown model: " + modelName)}
    }

    // 2. Get the right provider
    provider := cfg.clientFn(clients)

    // 3. Build the request (special cases are driven by registry flags)
    req := &chatRequest{
        Model:       cfg.modelID,
        Messages:    messages,
        Temperature: temperature,
        MaxTokens:   maxTokens,
    }
    if cfg.minOutputTokens > 0 && maxTokens < cfg.minOutputTokens {
        maxTokens = cfg.minOutputTokens
    }
    if cfg.reasoning {
        req.MaxCompletionTokens = maxTokens
        req.MaxTokens = 0
        req.Temperature = 0
    }

    // 4. Call with retries (429, 5xx only)
    // 5. Calculate cost
    // 6. Return ModelResult
}
```

**The reasoning model special case:** OpenAI's o-series and gpt-5 models (marked `reasoning: true`) use a different API field (`max_completion_tokens` instead of `max_tokens`) and reject any temperature other than the default. The registry flag handles this transparently — callers don't need to know.

---

## How to add a new model

Adding a model that routes through the Meesho gateway is one line:

```go
"gpt-5-mini": {modelID: "openai/gpt-5-mini", provider: "openai", clientFn: meeshoC},
```

Then add a pricing entry to `pricing.json`:
```json
"gpt-5-mini": { "input_per_1m": 0.40, "output_per_1m": 1.60 }
```

Also update:
- `pricing.json`, otherwise cost reports as `$0.00`.
- Any frontend model picker/constants that should expose the new key.
- Registry/pricing tests so missing attribution or pricing is caught before deploy.

This does require a backend rebuild/redeploy because the routing registry lives in Go code. The deliberate choice is: provider routing is code-reviewed, typed, and tested instead of being silently changed by runtime config.

---

## The ChatMessage type and multimodal support

```go
type ChatMessage struct {
    Role    string   `json:"role"`
    Content string   `json:"content"`
    Images  []string `json:"-"`  // base64 data URLs or https:// URLs
}
```

> **🔤 Go concept: struct tags (`json:"..."`)  **
> The backtick strings after each field are "struct tags" — metadata that libraries read. `json:"role"` tells Go's JSON library "when serializing this field to JSON, use the key `role` (not `Role`)". `json:"-"` means "skip this field entirely when serializing" — `Images` is handled separately by a custom `MarshalJSON` method.

For text-only messages, the JSON is a plain string:
```json
{"role": "user", "content": "What is 2+2?"}
```

For vision messages (images attached), the JSON becomes a content array per the OpenAI spec:
```json
{"role": "user", "content": [
    {"type": "text", "text": "What's in this image?"},
    {"type": "image_url", "image_url": {"url": "data:image/jpeg;base64,..."}}
]}
```

The `MarshalJSON()` method on `ChatMessage` handles this switch automatically — callers just set `msg.Images = [...]` and the JSON serialization does the right thing.
