package llm

import (
	"context"
	"fmt"
	"time"

	"llm_platform_go/internal/types"
)

// registry maps friendly model names to their Bifrost model IDs (provider/model-id format).
var registry = map[string]string{
	"gpt-4o-mini":       "openai/gpt-4o-mini",
	"gpt-4o":            "openai/gpt-4o",
	"gemini-2.5-flash":  "vertex/gemini-2.5-flash",
	"gemini-2.5-pro":    "vertex/gemini-2.5-pro-preview-06-05",
	"claude-sonnet-4-6": "anthropic/claude-sonnet-4-6",
}

// RegisteredModels returns a copy of the model registry for logging/inspection.
func RegisteredModels() map[string]string {
	out := make(map[string]string, len(registry))
	for k, v := range registry {
		out[k] = v
	}
	return out
}

// StreamAll fans out to all requested models concurrently. The caller must
// read exactly count results from the returned channel (arrival order = fastest first).
func StreamAll(ctx context.Context, clients *Clients, req *types.RunRequest) (<-chan ModelResult, int) {
	models := req.Models
	if len(models) == 0 {
		models = DefaultModels
	}

	ch := make(chan ModelResult, len(models))
	for _, name := range models {
		go func(modelName string) {
			ch <- callSingleModel(ctx, clients, modelName, req)
		}(name)
	}
	return ch, len(models)
}

// callSingleModel calls the Bifrost gateway for one model and always returns a ModelResult.
func callSingleModel(ctx context.Context, clients *Clients, modelName string, req *types.RunRequest) ModelResult {
	start := time.Now()

	bifrostModelID, ok := registry[modelName]
	if !ok {
		return errResult(modelName, start, fmt.Sprintf("unknown model: %s", modelName))
	}

	if clients.Gateway == nil {
		return errResult(modelName, start, "LLM client not configured")
	}

	temp := float32(0.7)
	if req.Temperature != nil {
		temp = float32(*req.Temperature)
	}

	maxTok := 1000
	if req.MaxTokens != nil {
		maxTok = *req.MaxTokens
	}
	if modelMax := GetMaxOutputTokens(modelName); modelMax > 0 && maxTok > modelMax {
		maxTok = modelMax
	}

	apiReq := chatRequest{
		Model:       bifrostModelID,
		Messages:    buildMessages(modelName, req),
		MaxTokens:   maxTok,
		Temperature: temp,
	}

	const attemptTimeout = 10 * time.Second
	attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
	resp, err := clients.Gateway.Call(attemptCtx, &apiReq)
	cancel()

	latencyMs := int(time.Since(start).Milliseconds())

	if err != nil {
		return errResult(modelName, start, err.Error())
	}
	if len(resp.Choices) == 0 {
		return errResult(modelName, start, "empty response from model")
	}

	text, ok := resp.Choices[0].Message.Content.(string)
	if !ok || text == "" {
		return errResult(modelName, start, "model returned empty content")
	}
	inTok := resp.Usage.PromptTokens
	outTok := resp.Usage.CompletionTokens
	cost := CalculateCost(modelName, inTok, outTok)

	return ModelResult{
		Model:           modelName,
		Response:        &text,
		LatencyMs:       latencyMs,
		InputTokens:     inTok,
		OutputTokens:    outTok,
		TotalTokens:     inTok + outTok,
		CostUSD:         cost,
		Success:         true,
		ContextWindow:   GetContextWindow(modelName),
		MaxOutputTokens: GetMaxOutputTokens(modelName),
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
			msgs = append(msgs, chatMessage{Role: m.Role, Content: m.Content})
		}
	} else {
		msgs = append(msgs, chatMessage{Role: "user", Content: req.Prompt})
	}

	return msgs
}

// ModelCheckResult is the per-model result returned by CheckModels.
type ModelCheckResult struct {
	Name    string `json:"name"`
	ModelID string `json:"model_id"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
}

// CheckModels probes every registered model with a minimal 1-token request
// and returns which ones the gateway accepts.
func CheckModels(ctx context.Context, clients *Clients) []ModelCheckResult {
	ch := make(chan ModelCheckResult, len(registry))
	for name := range registry {
		go func(name string) {
			maxTok := 1
			res := callSingleModel(ctx, clients, name, &types.RunRequest{
				Prompt:    "hi",
				MaxTokens: &maxTok,
			})
			mcr := ModelCheckResult{Name: name, ModelID: registry[name], OK: res.Success}
			if !res.Success && res.Error != nil {
				mcr.Error = *res.Error
			}
			ch <- mcr
		}(name)
	}

	results := make([]ModelCheckResult, 0, len(registry))
	for range registry {
		results = append(results, <-ch)
	}
	return results
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
