package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeAnthropicServer mimics POST /v1/messages with the given stop reason.
func fakeAnthropicServer(t *testing.T, text, stopReason string, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"overloaded"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "msg_test",
			"type":    "message",
			"role":    "assistant",
			"model":   "claude-opus-4-8",
			"content": []map[string]any{{"type": "text", "text": text}},
			"stop_reason": stopReason,
			"usage":       map[string]any{"input_tokens": 21, "output_tokens": 7},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func anthropicTestRequest() *chatRequest {
	return &chatRequest{
		Model: "claude-opus-4-8",
		Messages: []ChatMessage{
			{Role: "system", Content: "You are terse."},
			{Role: "user", Content: "hi"},
		},
		MaxTokens:   64,
		Temperature: 0.2, // must NOT be forwarded — Anthropic models reject it
	}
}

func TestAnthropicProviderHappyPath(t *testing.T) {
	srv := fakeAnthropicServer(t, "hello", "end_turn", http.StatusOK)
	p := NewAnthropicProvider("test-key", srv.URL)

	resp, err := p.Call(context.Background(), anthropicTestRequest())
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "hello" {
		t.Fatalf("unexpected choices: %+v", resp.Choices)
	}
	if resp.Usage.PromptTokens != 21 || resp.Usage.CompletionTokens != 7 {
		t.Fatalf("usage mapping: %+v", resp.Usage)
	}
}

func TestAnthropicProviderMapsAPIErrors(t *testing.T) {
	srv := fakeAnthropicServer(t, "", "", http.StatusInternalServerError)
	p := NewAnthropicProvider("test-key", srv.URL)

	_, err := p.Call(context.Background(), anthropicTestRequest())
	if err == nil {
		t.Fatal("expected error from 500 response")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("error must normalize to *llm.APIError (breaker/fallback classification), got %T: %v", err, err)
	}
	if apiErr.HTTPStatusCode != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want 500", apiErr.HTTPStatusCode)
	}
	if !isInfraFailure(apiErr) {
		t.Fatal("a 5xx from Anthropic must classify as an infra failure")
	}
}

func TestAnthropicProviderRefusalIsContentLevel(t *testing.T) {
	srv := fakeAnthropicServer(t, "", "refusal", http.StatusOK)
	p := NewAnthropicProvider("test-key", srv.URL)

	_, err := p.Call(context.Background(), anthropicTestRequest())
	if err == nil {
		t.Fatal("a refusal must surface as an error, not empty success")
	}
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.HTTPStatusCode != http.StatusBadRequest {
		t.Fatalf("refusal must be a 400-class APIError, got %T: %v", err, err)
	}
	if isInfraFailure(apiErr) {
		t.Fatal("a refusal is content-level — it must not trip the breaker or advance the fallback chain")
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
