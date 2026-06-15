package llm

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"llm_platform_go/internal/types"
)

// ── mock provider ─────────────────────────────────────────────────────────────

type mockProvider struct {
	calls   atomic.Int32
	results []callResult // consumed in order; last one repeated if list exhausted
}

type callResult struct {
	resp *chatResponse
	err  error
}

func (m *mockProvider) Call(ctx context.Context, req *chatRequest) (*chatResponse, error) {
	n := int(m.calls.Add(1)) - 1
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if n >= len(m.results) {
		n = len(m.results) - 1
	}
	r := m.results[n]
	return r.resp, r.err
}

func successMock(content string) *mockProvider {
	return &mockProvider{results: []callResult{{
		resp: &chatResponse{
			Choices: []chatChoice{{Message: chatMessage{Role: "assistant", Content: content}}},
			Usage:   chatUsage{PromptTokens: 10, CompletionTokens: 20},
		},
	}}}
}

func errorMock(err error) *mockProvider {
	return &mockProvider{results: []callResult{{err: err}}}
}

func clientsWith(gateway Provider) *Clients {
	return &Clients{Gateway: gateway}
}

func simpleReq(prompt string) *types.RunRequest {
	return &types.RunRequest{Prompt: prompt}
}

// ── callSingleModel ───────────────────────────────────────────────────────────

func TestCallSingleModel_Success(t *testing.T) {
	mock := successMock("hello back")
	result := callSingleModel(context.Background(), clientsWith(mock), "gpt-4o-mini", simpleReq("hi"))

	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}
	if result.Response == nil || *result.Response != "hello back" {
		t.Errorf("response: got %v, want %q", result.Response, "hello back")
	}
	if result.InputTokens != 10 || result.OutputTokens != 20 {
		t.Errorf("tokens: got in=%d out=%d, want in=10 out=20", result.InputTokens, result.OutputTokens)
	}
	if result.TotalTokens != 30 {
		t.Errorf("total tokens: got %d, want 30", result.TotalTokens)
	}
}

func TestCallSingleModel_UnknownModel(t *testing.T) {
	result := callSingleModel(context.Background(), &Clients{}, "no-such-model", simpleReq("hi"))

	if result.Success {
		t.Fatal("expected failure for unknown model")
	}
	if result.Error == nil || *result.Error != "unknown model: no-such-model" {
		t.Errorf("error: got %v", result.Error)
	}
}

func TestSingleClientNilGateway(t *testing.T) {
	result := callSingleModel(context.Background(), clientsWith(nil), "gpt-4o-mini", simpleReq("hi"))

	if result.Success {
		t.Fatal("expected failure for nil gateway")
	}
	if result.Error == nil || *result.Error != "LLM client not configured" {
		t.Errorf("error: got %v", result.Error)
	}
}

func TestCallSingleModel_EmptyChoices(t *testing.T) {
	mock := &mockProvider{results: []callResult{{
		resp: &chatResponse{Choices: []chatChoice{}},
	}}}

	result := callSingleModel(context.Background(), clientsWith(mock), "gpt-4o-mini", simpleReq("hi"))

	if result.Success {
		t.Fatal("expected failure for empty choices")
	}
	if result.Error == nil || *result.Error != "empty response from model" {
		t.Errorf("error: got %v", result.Error)
	}
}

func TestCallSingleModel_EmptyContent(t *testing.T) {
	mock := &mockProvider{results: []callResult{{
		resp: &chatResponse{
			Choices: []chatChoice{{Message: chatMessage{Content: ""}}},
		},
	}}}

	result := callSingleModel(context.Background(), clientsWith(mock), "gpt-4o-mini", simpleReq("hi"))

	if result.Success {
		t.Fatal("expected failure for empty content")
	}
	if result.Error == nil || *result.Error != "model returned empty content" {
		t.Errorf("error: got %v", result.Error)
	}
}

func TestCallSingleModel_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	mock := successMock("irrelevant")
	result := callSingleModel(ctx, clientsWith(mock), "gpt-4o-mini", simpleReq("hi"))

	if result.Success {
		t.Fatal("expected failure for cancelled context")
	}
}

func TestCallSingleModel_ErrorSurfacedFromGateway(t *testing.T) {
	mock := errorMock(&APIError{HTTPStatusCode: 429, Message: "rate limit"})
	result := callSingleModel(context.Background(), clientsWith(mock), "gpt-4o-mini", simpleReq("hi"))

	if result.Success {
		t.Fatal("expected failure")
	}
	if mock.calls.Load() != 1 {
		t.Errorf("expected exactly 1 call (Bifrost handles retries), got %d", mock.calls.Load())
	}
	if result.Error == nil || *result.Error == "" {
		t.Error("expected non-empty error message from gateway")
	}
}

// ── Bifrost-specific registry tests ──────────────────────────────────────────

func TestCallSingleModel_LlamaGroqRemoved(t *testing.T) {
	result := callSingleModel(context.Background(), clientsWith(successMock("irrelevant")), "llama-groq", simpleReq("hi"))

	if result.Success {
		t.Fatal("expected failure for removed model")
	}
	if result.Error == nil || *result.Error != "unknown model: llama-groq" {
		t.Errorf("error: got %v", result.Error)
	}
}

func TestCallSingleModel_ClaudeSonnetRegistered(t *testing.T) {
	mock := successMock("I am Claude")
	result := callSingleModel(context.Background(), clientsWith(mock), "claude-sonnet-4-6", simpleReq("hello"))

	if !result.Success {
		t.Fatalf("expected success for claude-sonnet-4-6, got: %v", result.Error)
	}
	if result.Response == nil || *result.Response != "I am Claude" {
		t.Errorf("response: got %v", result.Response)
	}
}

func TestCallSingleModel_GeminiFlashRegistered(t *testing.T) {
	mock := successMock("I am Gemini")
	result := callSingleModel(context.Background(), clientsWith(mock), "gemini-2.5-flash", simpleReq("hello"))

	if !result.Success {
		t.Fatalf("expected success for gemini-2.5-flash, got: %v", result.Error)
	}
	if result.Response == nil || *result.Response != "I am Gemini" {
		t.Errorf("response: got %v", result.Response)
	}
}

func TestBifrostModelID_HasProviderPrefix(t *testing.T) {
	for friendlyName, bifrostID := range registry {
		if !strings.Contains(bifrostID, "/") {
			t.Errorf("model %q has Bifrost ID %q without provider prefix (expected format: provider/model-id)", friendlyName, bifrostID)
		}
	}
}

func TestAllModelsUseSameGateway(t *testing.T) {
	for modelName := range registry {
		mock := successMock("ok")
		result := callSingleModel(context.Background(), clientsWith(mock), modelName, simpleReq("hi"))
		if !result.Success {
			t.Errorf("model %q failed with nil gateway error; all models should route through clients.Gateway: %v", modelName, result.Error)
		}
	}
}

func TestCallSingleModel_ZeroTokensFromGateway(t *testing.T) {
	// Bifrost may return 0 token counts for some models — result should still be Success.
	mock := &mockProvider{results: []callResult{{
		resp: &chatResponse{
			Choices: []chatChoice{{Message: chatMessage{Role: "assistant", Content: "hello"}}},
			Usage:   chatUsage{PromptTokens: 0, CompletionTokens: 0},
		},
	}}}

	result := callSingleModel(context.Background(), clientsWith(mock), "gpt-4o-mini", simpleReq("hi"))

	if !result.Success {
		t.Fatalf("expected success, got: %v", result.Error)
	}
	if result.TotalTokens != 0 {
		t.Errorf("total tokens: got %d, want 0", result.TotalTokens)
	}
	if result.CostUSD != 0.0 {
		t.Errorf("cost: got %f, want 0.0", result.CostUSD)
	}
}

// ── buildMessages ─────────────────────────────────────────────────────────────

func TestBuildMessages_SimplePrompt(t *testing.T) {
	req := simpleReq("what is Go?")
	msgs := buildMessages("gpt-4o-mini", req)

	if len(msgs) != 1 {
		t.Fatalf("len: got %d, want 1", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "what is Go?" {
		t.Errorf("message: got %+v", msgs[0])
	}
}

func TestBuildMessages_WithSystemPrompt(t *testing.T) {
	req := &types.RunRequest{Prompt: "hello", SystemPrompt: "you are helpful"}
	msgs := buildMessages("gpt-4o-mini", req)

	if len(msgs) != 2 {
		t.Fatalf("len: got %d, want 2", len(msgs))
	}
	if msgs[0].Role != "system" || msgs[0].Content != "you are helpful" {
		t.Errorf("system message: got %+v", msgs[0])
	}
	if msgs[1].Role != "user" || msgs[1].Content != "hello" {
		t.Errorf("user message: got %+v", msgs[1])
	}
}

func TestBuildMessages_UsesConversationHistory(t *testing.T) {
	req := &types.RunRequest{
		Prompt: "ignored when history present",
		ModelConversations: map[string][]types.Message{
			"gpt-4o-mini": {
				{Role: "user", Content: "first turn"},
				{Role: "assistant", Content: "first reply"},
				{Role: "user", Content: "second turn"},
			},
		},
	}
	msgs := buildMessages("gpt-4o-mini", req)

	if len(msgs) != 3 {
		t.Fatalf("len: got %d, want 3", len(msgs))
	}
	if msgs[0].Content != "first turn" || msgs[1].Content != "first reply" || msgs[2].Content != "second turn" {
		t.Errorf("history not preserved: %+v", msgs)
	}
}

func TestBuildMessages_HistoryOnlyForCorrectModel(t *testing.T) {
	req := &types.RunRequest{
		Prompt: "fallback prompt",
		ModelConversations: map[string][]types.Message{
			"claude-sonnet-4-6": {{Role: "user", Content: "claude history"}},
		},
	}
	msgs := buildMessages("gpt-4o-mini", req)

	if len(msgs) != 1 || msgs[0].Content != "fallback prompt" {
		t.Errorf("should fall back to prompt for model with no history: %+v", msgs)
	}
}

func TestBuildMessages_SystemPromptWithHistory(t *testing.T) {
	req := &types.RunRequest{
		SystemPrompt: "be concise",
		ModelConversations: map[string][]types.Message{
			"gpt-4o-mini": {
				{Role: "user", Content: "hi"},
				{Role: "assistant", Content: "hello"},
			},
		},
	}
	msgs := buildMessages("gpt-4o-mini", req)

	if len(msgs) != 3 {
		t.Fatalf("len: got %d, want 3 (system + 2 history)", len(msgs))
	}
	if msgs[0].Role != "system" {
		t.Errorf("first message should be system, got %q", msgs[0].Role)
	}
}

func TestBuildMessages_NonStringContentStringified(t *testing.T) {
	req := &types.RunRequest{
		ModelConversations: map[string][]types.Message{
			"gpt-4o-mini": {
				{Role: "user", Content: []interface{}{"part1", "part2"}},
			},
		},
	}
	msgs := buildMessages("gpt-4o-mini", req)

	if len(msgs) == 0 {
		t.Fatal("expected at least one message")
	}
	if msgs[0].Content == "" {
		t.Error("non-string content should be stringified, not empty")
	}
}
