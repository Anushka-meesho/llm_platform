package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"llm_platform_go/internal/config"
)

// Provider is the single seam for any LLM backend.
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
	// Images, when set, are sent as OpenAI-compatible multimodal content blocks
	// alongside the text. Each entry is an image reference the provider accepts —
	// a base64 data URL ("data:image/jpeg;base64,…") or an https URL. Empty for
	// text-only messages (the common case), which keep the plain string wire form.
	Images []string `json:"-"`
}

// MarshalJSON emits the OpenAI chat-completions content format. With no images
// it is a plain string (`"content": "…"`) — byte-identical to the old wire form,
// so text-only callers and their tests are unaffected. With images, content
// becomes the multimodal array (`[{type:text…},{type:image_url…}]`) understood by
// vision models (gpt-4o, gemini, etc.) over the OpenAI-compatible endpoint.
func (m ChatMessage) MarshalJSON() ([]byte, error) {
	if len(m.Images) == 0 {
		return json.Marshal(struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}{m.Role, m.Content})
	}
	parts := make([]any, 0, len(m.Images)+1)
	if m.Content != "" {
		parts = append(parts, map[string]any{"type": "text", "text": m.Content})
	}
	for _, img := range m.Images {
		parts = append(parts, map[string]any{
			"type":      "image_url",
			"image_url": map[string]string{"url": img},
		})
	}
	return json.Marshal(struct {
		Role    string `json:"role"`
		Content []any  `json:"content"`
	}{m.Role, parts})
}

// UnmarshalJSON handles both wire formats a provider may return for content:
//   - plain string: `"content": "text"` (most models)
//   - array: `"content": [{"type":"text","text":"..."},...]` (Gemini thinking models,
//     which return multi-part responses including thought tokens)
//
// All text parts are concatenated; non-text parts (thought tokens, etc.) are discarded.
func (m *ChatMessage) UnmarshalJSON(data []byte) error {
	var raw struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	m.Role = raw.Role

	var s string
	if err := json.Unmarshal(raw.Content, &s); err == nil {
		m.Content = s
		return nil
	}

	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw.Content, &parts); err != nil {
		return fmt.Errorf("content is neither string nor array: %w", err)
	}
	var sb strings.Builder
	for _, p := range parts {
		if p.Type == "text" && p.Text != "" {
			sb.WriteString(p.Text)
		}
	}
	m.Content = sb.String()
	return nil
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
	// authHeader, when set, carries apiKey verbatim under this header name
	// (e.g. the Meesho gateway's "x-bf-vk" virtual key). When empty the standard
	// "Authorization: Bearer <apiKey>" is used.
	authHeader string
	client     *http.Client
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
	if p.authHeader != "" {
		httpReq.Header.Set(p.authHeader, p.apiKey)
	} else {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

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
			// A security proxy (e.g. Zscaler) returns an HTML block page instead of
			// a JSON error body. Detect this so the user sees a network-policy message
			// rather than the generic HTTP status text.
			if strings.HasPrefix(strings.TrimSpace(string(respBody)), "<") {
				msg = "request blocked by network security policy — check VPN/firewall access to the gateway"
			} else {
				msg = http.StatusText(resp.StatusCode)
			}
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
	Groq   Provider
	Meesho Provider // Meesho internal gateway (OpenAI-compatible, x-bf-vk auth)
}

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
