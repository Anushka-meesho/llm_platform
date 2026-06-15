package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"llm_platform_go/internal/config"
)

// Provider is the single seam for any LLM backend.
type Provider interface {
	Call(ctx context.Context, req *chatRequest) (*chatResponse, error)
}

// chatRequest is the OpenAI-compatible chat completions request body.
type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Temperature float32       `json:"temperature,omitempty"`
}

type chatMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

// chatResponse is the OpenAI-compatible chat completions response body.
type chatResponse struct {
	Choices []chatChoice `json:"choices"`
	Usage   chatUsage    `json:"usage"`
}

type chatChoice struct {
	Message chatMessage `json:"message"`
}

type chatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// APIError represents a non-2xx HTTP response from the gateway.
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

// bifrostProvider sends requests to the Bifrost LLM gateway using the
// x-bf-vk virtual key header.
type bifrostProvider struct {
	baseURL    string
	virtualKey string
	client     *http.Client
}

func (p *bifrostProvider) Call(ctx context.Context, req *chatRequest) (*chatResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-bf-vk", p.virtualKey)

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
			raw := string(respBody)
			if len(raw) > 200 {
				raw = raw[:200] + "..."
			}
			msg = fmt.Sprintf("%s — raw: %s", http.StatusText(resp.StatusCode), raw)
		}
		return nil, &APIError{HTTPStatusCode: resp.StatusCode, Message: msg}
	}

	var chatResp chatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &chatResp, nil
}

var sharedHTTPClient = &http.Client{
	Transport: &http.Transport{
		DialContext: (&net.Dialer{
			Timeout: 5 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout: 5 * time.Second,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	},
}

// Clients holds the single gateway provider. All models route through Gateway.
type Clients struct {
	Gateway Provider
}

func BuildClients(cfg *config.Config) *Clients {
	return &Clients{
		Gateway: &bifrostProvider{
			baseURL:    cfg.BifrostURL,
			virtualKey: cfg.BifrostVirtualKey,
			client:     sharedHTTPClient,
		},
	}
}
