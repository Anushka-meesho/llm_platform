package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"llm_platform_go/internal/config"
)

// Provider is the single seam for any LLM backend.
// To add Claude: implement anthropicProvider satisfying this interface, wire it in BuildClients.
type Provider interface {
	Call(ctx context.Context, req *chatRequest) (*chatResponse, error)
}

// chatRequest is the OpenAI-compatible chat completions request body.
// OpenAI's reasoning models (gpt-5 family, o-series) reject max_tokens and
// non-default temperature — for those, CallModel sets MaxCompletionTokens
// instead and omits Temperature (registry flag `reasoning`).
type chatRequest struct {
	Model               string        `json:"model"`
	Messages            []ChatMessage `json:"messages"`
	MaxTokens           int           `json:"max_tokens,omitempty"`
	MaxCompletionTokens int           `json:"max_completion_tokens,omitempty"`
	Temperature         float32       `json:"temperature,omitempty"`
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatResponse is the OpenAI-compatible chat completions response body.
type chatResponse struct {
	Choices []chatChoice `json:"choices"`
	Usage   chatUsage    `json:"usage"`
}

type chatChoice struct {
	Message ChatMessage `json:"message"`
}

type chatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// APIError represents a non-2xx HTTP response from a provider.
type APIError struct {
	HTTPStatusCode int
	Message        string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("API error %d: %s", e.HTTPStatusCode, e.Message)
}

// errorBody parses the error message from OpenAI-compatible error responses.
type errorBody struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

// openAICompatProvider makes direct HTTP calls to any OpenAI-compatible endpoint.
// Covers OpenAI, Groq, and Gemini — all share the same wire format.
type openAICompatProvider struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

func (p *openAICompatProvider) Call(ctx context.Context, req *chatRequest) (*chatResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var eb errorBody
		_ = json.Unmarshal(respBody, &eb)
		msg := eb.Error.Message
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return nil, &APIError{HTTPStatusCode: resp.StatusCode, Message: msg}
	}

	var chatResp chatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &chatResp, nil
}

// sharedHTTPClient is reused across all providers.
// No hard timeout — request context (set by the handler) handles cancellation.
var sharedHTTPClient = &http.Client{Timeout: 120 * time.Second}

// NewOpenAICompatProvider returns a Provider for any OpenAI-compatible chat
// completions endpoint — self-hosted vLLM, gateways, or test fakes.
func NewOpenAICompatProvider(baseURL, apiKey string) Provider {
	return &openAICompatProvider{baseURL: baseURL, apiKey: apiKey, client: sharedHTTPClient}
}

// Clients holds one configured Provider per LLM backend.
type Clients struct {
	OpenAI    Provider
	Groq      Provider
	Gemini    Provider
	Anthropic Provider // native Messages API (anthropic.go), not OpenAI-compatible
}

func BuildClients(cfg *config.Config) *Clients {
	// Anthropic's SDK rejects a missing key client-side (a plain error, which
	// would misclassify as an infra failure and trip the breaker). Leave the
	// provider nil when unconfigured — calls then get the standard
	// "LLM client not configured" result, same as any unconfigured backend.
	var anthropicProvider Provider
	if cfg.AnthropicKey != "" {
		anthropicProvider = NewAnthropicProvider(cfg.AnthropicKey, cfg.AnthropicBaseURL)
	}

	return &Clients{
		OpenAI: &openAICompatProvider{
			baseURL: cfg.OpenAIBaseURL,
			apiKey:  cfg.OpenAIKey,
			client:  sharedHTTPClient,
		},
		Groq: &openAICompatProvider{
			baseURL: cfg.GroqBaseURL,
			apiKey:  cfg.GroqKey,
			client:  sharedHTTPClient,
		},
		Gemini: &openAICompatProvider{
			baseURL: cfg.GeminiBaseURL,
			apiKey:  cfg.GeminiKey,
			client:  sharedHTTPClient,
		},
		Anthropic: anthropicProvider,
	}
}
