package llm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"llm_platform_go/internal/types"
)

// providerConfig maps a friendly model name to its actual model ID and which provider to use.
type providerConfig struct {
	modelID  string
	clientFn func(*Clients) Provider
}

// registry is the single source of truth for provider routing.
// To swap Groq for Claude: replace the "llama-groq" entry with a claude providerConfig
// and add an anthropicProvider implementation in client.go.
var registry = map[string]providerConfig{
	"gpt-4o-mini":  {"gpt-4o-mini", func(c *Clients) Provider { return c.OpenAI }},
	"llama-groq":   {"llama-3.3-70b-versatile", func(c *Clients) Provider { return c.Groq }},
	"gemini-flash": {"gemini-2.0-flash", func(c *Clients) Provider { return c.Gemini }},
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

// callSingleModel calls one provider and always returns a ModelResult — never panics.
func callSingleModel(ctx context.Context, clients *Clients, modelName string, req *types.RunRequest) ModelResult {
	start := time.Now()

	cfg, ok := registry[modelName]
	if !ok {
		return errResult(modelName, start, fmt.Sprintf("unknown model: %s", modelName))
	}

	temp := float32(0.7)
	if req.Temperature != nil {
		temp = float32(*req.Temperature)
	}

	messages := buildMessages(modelName, req)
	provider := cfg.clientFn(clients)
	if provider == nil {
		return errResult(modelName, start, "LLM client not configured")
	}

	maxTok := 1000
	if req.MaxTokens != nil {
		maxTok = *req.MaxTokens
	}

	apiReq := chatRequest{
		Model:       cfg.modelID,
		Messages:    messages,
		MaxTokens:   maxTok,
		Temperature: temp,
	}

	var resp *chatResponse
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		resp, err = provider.Call(ctx, &apiReq)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			break // context cancelled or timed out — do not retry
		}
		var apiErr *APIError
		if errors.As(err, &apiErr) && isRetryable(apiErr.HTTPStatusCode) {
			time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
			continue
		}
		break // non-retryable error
	}

	latencyMs := int(time.Since(start).Milliseconds())

	if err != nil {
		return errResult(modelName, start, classifyError(err))
	}
	if len(resp.Choices) == 0 {
		return errResult(modelName, start, "empty response from model")
	}

	text := resp.Choices[0].Message.Content
	if text == "" {
		return errResult(modelName, start, "model returned empty content")
	}
	inTok := resp.Usage.PromptTokens
	outTok := resp.Usage.CompletionTokens
	cost := CalculateCost(modelName, inTok, outTok)

	return ModelResult{
		Model:        modelName,
		Response:     &text,
		LatencyMs:    latencyMs,
		InputTokens:  inTok,
		OutputTokens: outTok,
		TotalTokens:  inTok + outTok,
		CostUSD:      cost,
		Success:      true,
	}
}

// buildMessages constructs the message slice for the API call.
// Priority: model_conversations history > single-turn prompt.
func buildMessages(modelName string, req *types.RunRequest) []chatMessage {
	var msgs []chatMessage

	if req.SystemPrompt != "" {
		msgs = append(msgs, chatMessage{Role: "system", Content: req.SystemPrompt})
	}

	if hist, ok := req.ModelConversations[modelName]; ok && len(hist) > 0 {
		for _, m := range hist {
			content := ""
			switch v := m.Content.(type) {
			case string:
				content = v
			default:
				content = fmt.Sprintf("%v", v)
			}
			msgs = append(msgs, chatMessage{Role: m.Role, Content: content})
		}
	} else {
		msgs = append(msgs, chatMessage{Role: "user", Content: req.Prompt})
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
		LatencyMs: latencyMs,
		Success:   false,
		Error:     &msg,
	}
}
