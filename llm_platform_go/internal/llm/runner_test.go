package llm

import (
	"context"
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
			Choices: []chatChoice{{Message: ChatMessage{Role: "assistant", Content: content}}},
			Usage:   chatUsage{PromptTokens: 10, CompletionTokens: 20},
		},
	}}}
}

func errorMock(err error) *mockProvider {
	return &mockProvider{results: []callResult{{err: err}}}
}

func clientsWith(meesho, groq Provider) *Clients {
	return &Clients{Meesho: meesho, Groq: groq}
}

func simpleReq(prompt string) *types.RunRequest {
	return &types.RunRequest{Prompt: prompt}
}

// ── callSingleModel ───────────────────────────────────────────────────────────

func TestCallSingleModel_Success(t *testing.T) {
	mock := successMock("hello back")
	clients := clientsWith(mock, nil)

	result := callSingleModel(context.Background(), clients, "gpt-4o-mini", simpleReq("hi"))

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

func TestCallSingleModel_NilProvider(t *testing.T) {
	// gpt-4o-mini is in registry but Clients.Meesho is nil
	clients := clientsWith(nil, nil)
	result := callSingleModel(context.Background(), clients, "gpt-4o-mini", simpleReq("hi"))

	if result.Success {
		t.Fatal("expected failure for nil provider")
	}
	if result.Error == nil || *result.Error != "LLM client not configured" {
		t.Errorf("error: got %v", result.Error)
	}
}

func TestCallSingleModel_EmptyChoices(t *testing.T) {
	mock := &mockProvider{results: []callResult{{
		resp: &chatResponse{Choices: []chatChoice{}},
	}}}
	clients := clientsWith(mock, nil)

	result := callSingleModel(context.Background(), clients, "gpt-4o-mini", simpleReq("hi"))

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
			Choices: []chatChoice{{Message: ChatMessage{Content: ""}}},
		},
	}}}
	clients := clientsWith(mock, nil)

	result := callSingleModel(context.Background(), clients, "gpt-4o-mini", simpleReq("hi"))

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
	result := callSingleModel(ctx, clientsWith(mock, nil), "gpt-4o-mini", simpleReq("hi"))

	if result.Success {
		t.Fatal("expected failure for cancelled context")
	}
}

// ── retry logic ───────────────────────────────────────────────────────────────

func TestCallSingleModel_RetriesOnTransientError(t *testing.T) {
	// First two calls return 429, third succeeds.
	mock := &mockProvider{results: []callResult{
		{err: &APIError{HTTPStatusCode: 429, Message: "rate limit"}},
		{err: &APIError{HTTPStatusCode: 429, Message: "rate limit"}},
		{resp: &chatResponse{
			Choices: []chatChoice{{Message: ChatMessage{Content: "ok"}}},
			Usage:   chatUsage{PromptTokens: 5, CompletionTokens: 5},
		}},
	}}
	clients := clientsWith(mock, nil)

	// Patch sleep to avoid 6s delay in tests — we do this by using a cancelable
	// context with a generous deadline (retries sleep but context allows them).
	result := callSingleModel(context.Background(), clients, "gpt-4o-mini", simpleReq("hi"))

	if !result.Success {
		t.Fatalf("expected success after retries, got error: %v", result.Error)
	}
	if mock.calls.Load() != 3 {
		t.Errorf("expected 3 calls (2 failures + 1 success), got %d", mock.calls.Load())
	}
}

func TestCallSingleModel_NoRetryOnPermanentError(t *testing.T) {
	// 401 is not retryable — should stop after first call.
	mock := errorMock(&APIError{HTTPStatusCode: 401, Message: "bad key"})
	clients := clientsWith(mock, nil)

	result := callSingleModel(context.Background(), clients, "gpt-4o-mini", simpleReq("hi"))

	if result.Success {
		t.Fatal("expected failure")
	}
	if mock.calls.Load() != 1 {
		t.Errorf("expected 1 call for non-retryable error, got %d", mock.calls.Load())
	}
}

func TestCallSingleModel_AllRetriesExhausted(t *testing.T) {
	// All 3 attempts return 500.
	mock := errorMock(&APIError{HTTPStatusCode: 500, Message: "server error"})
	clients := clientsWith(mock, nil)

	result := callSingleModel(context.Background(), clients, "gpt-4o-mini", simpleReq("hi"))

	if result.Success {
		t.Fatal("expected failure after all retries")
	}
	if mock.calls.Load() != 3 {
		t.Errorf("expected 3 attempts, got %d", mock.calls.Load())
	}
	if result.Error == nil || *result.Error != "Provider internal error — all retries exhausted" {
		t.Errorf("error message: got %v", result.Error)
	}
}

func TestCallSingleModel_StopsRetryOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	callCount := &atomic.Int32{}
	mock := &mockProvider{results: []callResult{
		{err: &APIError{HTTPStatusCode: 500, Message: "err"}},
	}}
	_ = callCount

	// Cancel after first call.
	originalMock := &cancelOnNthCall{provider: mock, cancelAfter: 1, cancel: cancel}
	clients := clientsWith(originalMock, nil)

	result := callSingleModel(ctx, clients, "gpt-4o-mini", simpleReq("hi"))

	if result.Success {
		t.Fatal("expected failure after context cancel")
	}
	// Should not have made more than 1-2 calls before stopping.
	if originalMock.calls.Load() > 2 {
		t.Errorf("too many calls after context cancel: %d", originalMock.calls.Load())
	}
}

// cancelOnNthCall cancels the context after the Nth call.
type cancelOnNthCall struct {
	provider    Provider
	cancelAfter int32
	cancel      context.CancelFunc
	calls       atomic.Int32
}

func (c *cancelOnNthCall) Call(ctx context.Context, req *chatRequest) (*chatResponse, error) {
	n := c.calls.Add(1)
	resp, err := c.provider.Call(ctx, req)
	if n >= c.cancelAfter {
		c.cancel()
	}
	return resp, err
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
	// History exists for gemini-flash but we're building for gpt-4o-mini.
	req := &types.RunRequest{
		Prompt: "fallback prompt",
		ModelConversations: map[string][]types.Message{
			"gemini-flash": {{Role: "user", Content: "gemini history"}},
		},
	}
	msgs := buildMessages("gpt-4o-mini", req)

	// Should fall back to single-turn prompt, not use gemini's history.
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

func TestBuildMessages_StructuredTextContent(t *testing.T) {
	req := &types.RunRequest{
		ModelConversations: map[string][]types.Message{
			"gpt-4o-mini": {
				{Role: "user", Content: []interface{}{
					map[string]interface{}{"type": "text", "text": "part1"},
					map[string]interface{}{"type": "text", "text": "part2"},
				}},
			},
		},
	}
	msgs := buildMessages("gpt-4o-mini", req)

	if len(msgs) == 0 {
		t.Fatal("expected at least one message")
	}
	if msgs[0].Content == "" {
		t.Error("structured text content blocks should produce non-empty content")
	}
}

// ── isRetryable ───────────────────────────────────────────────────────────────

func TestIsRetryable(t *testing.T) {
	retryable := []int{429, 500, 503}
	for _, code := range retryable {
		if !isRetryable(code) {
			t.Errorf("expected %d to be retryable", code)
		}
	}

	notRetryable := []int{200, 400, 401, 403, 404, 422}
	for _, code := range notRetryable {
		if isRetryable(code) {
			t.Errorf("expected %d to NOT be retryable", code)
		}
	}
}

// Every registry entry must be priced and attributable — an unpriced model
// silently bills $0 (money-path regression).
func TestRegistryModelsArePricedAndAttributed(t *testing.T) {
	if err := LoadPricing("../../pricing.json"); err != nil {
		t.Fatalf("load pricing: %v", err)
	}
	table := PricingTable()
	for key, cfg := range registry {
		if _, ok := table[key]; !ok {
			t.Errorf("model %q has no pricing.json entry", key)
		}
		if cfg.provider == "" {
			t.Errorf("model %q has no provider attribution", key)
		}
		if cfg.clientFn == nil {
			t.Errorf("model %q has no client function", key)
		}
	}
	// And the reverse: no orphaned pricing entries for unknown models.
	for key := range table {
		if !KnownModel(key) {
			t.Errorf("pricing.json entry %q is not in the routing registry", key)
		}
	}
}
