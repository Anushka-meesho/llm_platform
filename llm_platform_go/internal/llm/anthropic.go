package llm

import (
	"context"
	"errors"
	"net/http"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// anthropicProvider is the native Provider for the Anthropic Messages API —
// Anthropic does not speak the OpenAI-compatible wire format, so this is the
// one backend that doesn't go through openAICompatProvider. It adapts the
// platform's internal chatRequest/chatResponse contract onto the official
// anthropic-sdk-go.
//
// Intentional mappings:
//   - temperature is never forwarded: current Anthropic models (Fable 5,
//     Opus 4.7+) reject sampling parameters with a 400; behavior is steered
//     by prompting. Tasks' temperature setting applies to other providers.
//   - thinking is left at each model's default (Fable 5: always on; Opus &
//     Sonnet: off) — predictions want the final answer, and thinking blocks
//     are filtered out of the response text.
//   - a safety-classifier refusal (HTTP 200 + stop_reason "refusal") is
//     surfaced as a 400-class APIError: definitive for this content, so no
//     retry, no breaker trip, no fallback spend on the same input.
type anthropicProvider struct {
	client anthropic.Client
}

// NewAnthropicProvider returns the native Anthropic Provider. baseURL is for
// tests/self-hosted gateways; "" uses the production API.
func NewAnthropicProvider(apiKey, baseURL string) Provider {
	opts := []option.RequestOption{
		option.WithAPIKey(apiKey),
		// The platform owns retry policy (CallModel) and circuit breaking —
		// disable the SDK's internal retries so the two don't compound.
		option.WithMaxRetries(0),
	}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	return &anthropicProvider{client: anthropic.NewClient(opts...)}
}

func (p *anthropicProvider) Call(ctx context.Context, req *chatRequest) (*chatResponse, error) {
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(req.Model),
		MaxTokens: int64(req.MaxTokens),
	}
	if params.MaxTokens <= 0 {
		params.MaxTokens = 1024
	}

	for _, m := range req.Messages {
		switch m.Role {
		case "system":
			params.System = append(params.System, anthropic.TextBlockParam{Text: m.Content})
		case "assistant":
			params.Messages = append(params.Messages, anthropic.NewAssistantMessage(anthropic.NewTextBlock(m.Content)))
		default:
			params.Messages = append(params.Messages, anthropic.NewUserMessage(anthropic.NewTextBlock(m.Content)))
		}
	}

	resp, err := p.client.Messages.New(ctx, params)
	if err != nil {
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) {
			// Normalize onto the platform error type so retry, breaker, and
			// fallback classification work identically across providers.
			return nil, &APIError{HTTPStatusCode: apiErr.StatusCode, Message: apiErr.Error()}
		}
		return nil, err
	}

	if resp.StopReason == anthropic.StopReasonRefusal {
		return nil, &APIError{
			HTTPStatusCode: http.StatusBadRequest,
			Message:        "request declined by the model's safety classifiers",
		}
	}

	// Concatenate text blocks; thinking blocks (Fable 5) are not output.
	var text string
	for _, block := range resp.Content {
		if t, ok := block.AsAny().(anthropic.TextBlock); ok {
			text += t.Text
		}
	}

	return &chatResponse{
		Choices: []chatChoice{{Message: ChatMessage{Role: "assistant", Content: text}}},
		Usage: chatUsage{
			PromptTokens:     int(resp.Usage.InputTokens),
			CompletionTokens: int(resp.Usage.OutputTokens),
		},
	}, nil
}
