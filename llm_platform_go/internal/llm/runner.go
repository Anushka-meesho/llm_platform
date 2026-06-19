package llm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"time"

	"llm_platform_go/internal/types"
)

// providerConfig maps a friendly model name to its actual model ID and which provider to use.
type providerConfig struct {
	modelID  string
	provider string // attribution name recorded on runs (openai/groq/gemini/anthropic)
	clientFn func(*Clients) Provider
	// reasoning marks OpenAI reasoning-family models (gpt-5, o-series) whose
	// chat-completions wire rejects max_tokens and non-default temperature —
	// CallModel sends max_completion_tokens and omits temperature instead.
	reasoning bool
}

var (
	groqC   = func(c *Clients) Provider { return c.Groq }
	meeshoC = func(c *Clients) Provider { return c.Meesho }
)

// registry is the single source of truth for provider routing. All non-Groq
// models route through the Meesho bifrost gateway (OpenAI-compatible,
// x-bf-vk auth) using provider-prefixed model IDs. Only llama-groq uses the
// Groq API directly.
var registry = map[string]providerConfig{
	// ── OpenAI via Meesho bifrost ────────────────────────────────────────────
	// "gpt-5.1":      {modelID: "openai/gpt-5.1", provider: "openai", clientFn: meeshoC},
	// "gpt-5":        {modelID: "openai/gpt-5", provider: "openai", clientFn: meeshoC},
	// "gpt-5-mini":   {modelID: "openai/gpt-5-mini", provider: "openai", clientFn: meeshoC},
	// "gpt-5-nano":   {modelID: "openai/gpt-5-nano", provider: "openai", clientFn: meeshoC},
	// "gpt-4.1":      {modelID: "openai/gpt-4.1", provider: "openai", clientFn: meeshoC},
	// "gpt-4.1-mini": {modelID: "openai/gpt-4.1-mini", provider: "openai", clientFn: meeshoC},
	// "gpt-4.1-nano": {modelID: "openai/gpt-4.1-nano", provider: "openai", clientFn: meeshoC},
	"gpt-4o":      {modelID: "openai/gpt-4o", provider: "openai", clientFn: meeshoC},
	"gpt-4o-mini": {modelID: "openai/gpt-4o-mini", provider: "openai", clientFn: meeshoC},

	// ── Groq (direct API — own key) ──────────────────────────────────────────
	"llama-groq": {modelID: "llama-3.3-70b-versatile", provider: "groq", clientFn: groqC},

	// ── Gemini via Meesho bifrost ────────────────────────────────────────────
	// "gemini-3-pro":          {modelID: "vertex/gemini-3-pro-preview", provider: "gemini", clientFn: meeshoC},
	"gemini-2.5-pro":   {modelID: "vertex/gemini-2.5-pro", provider: "gemini", clientFn: meeshoC},
	"gemini-2.5-flash": {modelID: "vertex/gemini-2.5-flash", provider: "gemini", clientFn: meeshoC},
	// "gemini-2.5-flash-lite": {modelID: "vertex/gemini-2.5-flash-lite", provider: "gemini", clientFn: meeshoC},
	// "gemini-flash":          {modelID: "vertex/gemini-2.0-flash", provider: "gemini", clientFn: meeshoC},

	// ── Anthropic via Meesho bifrost (OpenAI-compatible, not native SDK) ─────
	// "claude-fable-5":    {modelID: "anthropic/claude-fable-5", provider: "anthropic", clientFn: meeshoC},
	// "claude-opus-4-8":   {modelID: "anthropic/claude-opus-4-8", provider: "anthropic", clientFn: meeshoC},
	"claude-sonnet-4-6": {modelID: "anthropic/claude-sonnet-4-6", provider: "anthropic", clientFn: meeshoC},
	// "claude-haiku-4-5":  {modelID: "anthropic/claude-haiku-4-5", provider: "anthropic", clientFn: meeshoC},
}

// ProviderName returns the attribution name for a model key ("" if unknown).
func ProviderName(model string) string {
	return registry[model].provider
}

// KnownModel reports whether the model key exists in the routing registry.
func KnownModel(model string) bool {
	_, ok := registry[model]
	return ok
}

// AllModels returns every routing key in the registry, sorted. Backs the
// health endpoint's models_available (DefaultModels stays the small /run
// fan-out default — fanning out to the whole registry would be expensive).
func AllModels() []string {
	keys := make([]string, 0, len(registry))
	for k := range registry {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// RunAll fans out to all requested models concurrently and returns results in
// first-come-first-served order (fastest model appears first).
func RunAll(ctx context.Context, clients *Clients, req *types.RunRequest) *RunResult {
	models := req.Models
	if len(models) == 0 {
		models = DefaultModels
	}

	// Buffered channel — goroutines never block on send.
	ch := make(chan ModelResult, len(models))

	for _, name := range models {
		go func(modelName string) {
			ch <- callSingleModel(ctx, clients, modelName, req)
		}(name)
	}

	results := make([]ModelResult, 0, len(models))
	for range models {
		results = append(results, <-ch) // arrival order = fastest first
	}

	return &RunResult{Prompt: req.Prompt, Results: results}
}

// callSingleModel adapts a playground /run request into a CallModel invocation.
func callSingleModel(ctx context.Context, clients *Clients, modelName string, req *types.RunRequest) ModelResult {
	temp := float32(0.7)
	if req.Temperature != nil {
		temp = float32(*req.Temperature)
	}
	maxTok := 1000
	if req.MaxTokens != nil {
		maxTok = *req.MaxTokens
	}
	return CallModel(ctx, clients, modelName, buildMessages(modelName, req), temp, maxTok)
}

// CallModel calls one model with the given messages and always returns a
// ModelResult — never panics. This is the single execution path shared by the
// playground fan-out (/run) and the task prediction endpoint (/v1/tasks/.../predict).
func CallModel(ctx context.Context, clients *Clients, modelName string, messages []ChatMessage, temperature float32, maxTokens int) ModelResult {
	start := time.Now()

	cfg, ok := registry[modelName]
	if !ok {
		return errResult(modelName, start, fmt.Sprintf("unknown model: %s", modelName))
	}

	provider := cfg.clientFn(clients)
	if provider == nil {
		r := errResult(modelName, start, "LLM client not configured")
		r.fallbackEligible = true // unconfigured provider → try the next model
		return r
	}

	apiReq := chatRequest{
		Model:       cfg.modelID,
		Messages:    messages,
		MaxTokens:   maxTokens,
		Temperature: temperature,
	}
	if cfg.reasoning {
		// gpt-5 family / o-series: max_tokens and temperature are rejected.
		apiReq.MaxTokens = 0
		apiReq.MaxCompletionTokens = maxTokens
		apiReq.Temperature = 0 // omitted via omitempty → provider default
	}

	var resp *chatResponse
	var err error
	tries := 0           // upstream HTTP attempts actually made (incl. retries)
	lastStatus := 0      // last upstream HTTP status seen (0 = no response reached)
	for attempt := 0; attempt < 3; attempt++ {
		tries++
		resp, err = provider.Call(ctx, &apiReq)
		if err == nil {
			break
		}
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			lastStatus = apiErr.HTTPStatusCode
		}
		if ctx.Err() != nil {
			break // context cancelled or timed out — do not retry
		}
		if apiErr != nil && isRetryable(apiErr.HTTPStatusCode) {
			time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
			continue
		}
		break // non-retryable error
	}

	latencyMs := int(time.Since(start).Milliseconds())

	if err != nil {
		r := errResult(modelName, start, classifyError(err))
		r.infraFailure = isInfraFailure(err)
		r.fallbackEligible = shouldFallback(err)
		r.httpStatus = lastStatus
		r.retryCount = tries
		return r
	}
	if len(resp.Choices) == 0 {
		r := errResult(modelName, start, "empty response from model")
		r.httpStatus = 200
		r.retryCount = tries
		return r
	}

	text := resp.Choices[0].Message.Content
	if text == "" {
		r := errResult(modelName, start, "model returned empty content")
		r.httpStatus = 200
		r.retryCount = tries
		return r
	}
	inTok := resp.Usage.PromptTokens
	outTok := resp.Usage.CompletionTokens
	cost := CalculateCost(modelName, inTok, outTok)

	return ModelResult{
		Model:        modelName,
		Provider:     cfg.provider,
		Response:     &text,
		LatencyMs:    latencyMs,
		InputTokens:  inTok,
		OutputTokens: outTok,
		TotalTokens:  inTok + outTok,
		CostUSD:      cost,
		Success:      true,
		httpStatus:   200,
		retryCount:   tries,
	}
}

// buildMessages constructs the message slice for the API call.
// Priority: model_conversations history > single-turn prompt.
func buildMessages(modelName string, req *types.RunRequest) []ChatMessage {
	var msgs []ChatMessage

	if req.SystemPrompt != "" {
		msgs = append(msgs, ChatMessage{Role: "system", Content: req.SystemPrompt})
	}

	if hist, ok := req.ModelConversations[modelName]; ok && len(hist) > 0 {
		for _, m := range hist {
			var textContent string
			var imgURLs []string
			switch v := m.Content.(type) {
			case string:
				textContent = v
			case []interface{}:
				for _, part := range v {
					pm, ok := part.(map[string]interface{})
					if !ok {
						continue
					}
					switch pm["type"] {
					case "text":
						if t, ok := pm["text"].(string); ok {
							textContent = t
						}
					case "image_url":
						if iu, ok := pm["image_url"].(map[string]interface{}); ok {
							if url, ok := iu["url"].(string); ok && url != "" {
								imgURLs = append(imgURLs, url)
							}
						}
					}
				}
			default:
				textContent = fmt.Sprintf("%v", v)
			}
			msg := ChatMessage{Role: m.Role, Content: textContent}
			if len(imgURLs) > 0 {
				msg.Images = imgURLs
			}
			msgs = append(msgs, msg)
		}
	} else {
		msgs = append(msgs, ChatMessage{Role: "user", Content: req.Prompt})
	}

	return msgs
}

// classifyError maps API and network errors to human-readable messages.
func classifyError(err error) string {
	// Context errors — check before API errors
	if errors.Is(err, context.DeadlineExceeded) {
		return "Request timed out"
	}
	if errors.Is(err, context.Canceled) {
		return "Request cancelled"
	}

	// Network errors
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return "Request timed out"
		}
		return "Network error — check connectivity"
	}

	// API errors
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		switch apiErr.HTTPStatusCode {
		case 400:
			return fmt.Sprintf("Bad request: %s", apiErr.Message)
		case 401:
			return "Auth failed — check API key"
		case 404:
			return fmt.Sprintf("Model or endpoint not found: %s", apiErr.Message)
		case 429:
			return "Rate limit hit — all retries exhausted"
		case 500:
			return "Provider internal error — all retries exhausted"
		case 503:
			return "Service unavailable — all retries exhausted"
		}
		return fmt.Sprintf("API error %d: %s", apiErr.HTTPStatusCode, apiErr.Message)
	}

	return fmt.Sprintf("Unexpected error: %v", err)
}

// isRetryable returns true for transient HTTP status codes worth retrying.
func isRetryable(status int) bool {
	return status == 429 || status == 500 || status == 503
}

func errResult(model string, start time.Time, msg string) ModelResult {
	latencyMs := int(time.Since(start).Milliseconds())
	return ModelResult{
		Model:     model,
		Provider:  ProviderName(model), // "" for unknown models
		LatencyMs: latencyMs,
		Success:   false,
		Error:     &msg,
	}
}
