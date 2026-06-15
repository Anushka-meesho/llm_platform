package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func okResponse(content string, promptTok, completionTok int) chatResponse {
	return chatResponse{
		Choices: []chatChoice{{Message: chatMessage{Role: "assistant", Content: content}}},
		Usage:   chatUsage{PromptTokens: promptTok, CompletionTokens: completionTok},
	}
}

func newProvider(srv *httptest.Server, vk string) *bifrostProvider {
	return &bifrostProvider{
		baseURL:    srv.URL,
		virtualKey: vk,
		client:     srv.Client(),
	}
}

func minimalReq() *chatRequest {
	return &chatRequest{
		Model:       "openai/gpt-4o-mini",
		Messages:    []chatMessage{{Role: "user", Content: "hello"}},
		MaxTokens:   100,
		Temperature: 0.7,
	}
}

// ── basic success and request forwarding ──────────────────────────────────────

func TestProvider_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(okResponse("world", 5, 10))
	}))
	defer srv.Close()

	resp, err := newProvider(srv, "test-vk").Call(context.Background(), minimalReq())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "world" {
		t.Errorf("content: got %q, want %q", resp.Choices[0].Message.Content, "world")
	}
	if resp.Usage.PromptTokens != 5 || resp.Usage.CompletionTokens != 10 {
		t.Errorf("usage: got %+v, want {5 10}", resp.Usage)
	}
}

func TestProvider_CorrectEndpointPath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		json.NewEncoder(w).Encode(okResponse("ok", 1, 1))
	}))
	defer srv.Close()

	newProvider(srv, "vk").Call(context.Background(), minimalReq()) //nolint:errcheck

	if gotPath != "/v1/chat/completions" {
		t.Errorf("path: got %q, want /v1/chat/completions", gotPath)
	}
}

func TestBifrost_TrailingSlashURL(t *testing.T) {
	// Verify that a baseURL without trailing slash never produces double-slash paths.
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		json.NewEncoder(w).Encode(okResponse("ok", 1, 1))
	}))
	defer srv.Close()

	// baseURL has no trailing slash (as trimmed by config.Load).
	p := &bifrostProvider{baseURL: srv.URL, virtualKey: "vk", client: srv.Client()}
	p.Call(context.Background(), minimalReq()) //nolint:errcheck

	if strings.Contains(gotPath, "//") {
		t.Errorf("path contains double slash: %q", gotPath)
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("path: got %q, want /v1/chat/completions", gotPath)
	}
}

func TestBifrost_EmptyVirtualKey(t *testing.T) {
	// An empty virtual key should still send the header (as empty string), not panic.
	var gotVK string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotVK = r.Header.Get("x-bf-vk")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"invalid key"}}`))
	}))
	defer srv.Close()

	p := &bifrostProvider{baseURL: srv.URL, virtualKey: "", client: srv.Client()}
	_, err := p.Call(context.Background(), minimalReq())
	// Should return an APIError (401), not a panic.
	if err == nil {
		t.Fatal("expected error for empty virtual key")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T", err)
	}
	if apiErr.HTTPStatusCode != 401 {
		t.Errorf("status: got %d, want 401", apiErr.HTTPStatusCode)
	}
	if gotVK != "" {
		t.Errorf("x-bf-vk should be empty string, got %q", gotVK)
	}
}

func TestBifrost_ZeroTokenUsage(t *testing.T) {
	// Some gateway responses omit usage or return 0 tokens — should succeed, cost=0.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(chatResponse{
			Choices: []chatChoice{{Message: chatMessage{Role: "assistant", Content: "hi"}}},
			Usage:   chatUsage{PromptTokens: 0, CompletionTokens: 0},
		})
	}))
	defer srv.Close()

	resp, err := newProvider(srv, "vk").Call(context.Background(), minimalReq())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Usage.PromptTokens != 0 || resp.Usage.CompletionTokens != 0 {
		t.Errorf("expected zero usage, got %+v", resp.Usage)
	}
	if len(resp.Choices) == 0 || resp.Choices[0].Message.Content != "hi" {
		t.Errorf("expected content 'hi', got %v", resp.Choices)
	}
}

func TestProvider_RequestBodyForwarded(t *testing.T) {
	var decoded chatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&decoded) //nolint:errcheck
		json.NewEncoder(w).Encode(okResponse("ok", 1, 1))
	}))
	defer srv.Close()

	req := &chatRequest{
		Model:       "groq/llama-3.3-70b-versatile",
		Messages:    []chatMessage{{Role: "user", Content: "test"}},
		MaxTokens:   512,
		Temperature: 0.5,
	}
	newProvider(srv, "vk").Call(context.Background(), req) //nolint:errcheck

	if decoded.Model != req.Model {
		t.Errorf("model: got %q, want %q", decoded.Model, req.Model)
	}
	if decoded.MaxTokens != 512 {
		t.Errorf("max_tokens: got %d, want 512", decoded.MaxTokens)
	}
	if len(decoded.Messages) != 1 || decoded.Messages[0].Content != "test" {
		t.Errorf("messages not forwarded correctly: %+v", decoded.Messages)
	}
}

// ── Bifrost-specific header contract ──────────────────────────────────────────

func TestBifrost_VirtualKeyHeaderSent(t *testing.T) {
	var gotVK string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotVK = r.Header.Get("x-bf-vk")
		json.NewEncoder(w).Encode(okResponse("ok", 1, 1))
	}))
	defer srv.Close()

	newProvider(srv, "my-virtual-key").Call(context.Background(), minimalReq()) //nolint:errcheck

	if gotVK != "my-virtual-key" {
		t.Errorf("x-bf-vk: got %q, want %q", gotVK, "my-virtual-key")
	}
}

func TestBifrost_NoAuthorizationHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(okResponse("ok", 1, 1))
	}))
	defer srv.Close()

	newProvider(srv, "vk").Call(context.Background(), minimalReq()) //nolint:errcheck

	if gotAuth != "" {
		t.Errorf("Authorization header should be absent, got %q", gotAuth)
	}
}

func TestBifrost_ModelIDForwardedWithPrefix(t *testing.T) {
	var decoded chatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&decoded) //nolint:errcheck
		json.NewEncoder(w).Encode(okResponse("ok", 1, 1))
	}))
	defer srv.Close()

	req := &chatRequest{
		Model:    "openai/gpt-4o-mini",
		Messages: []chatMessage{{Role: "user", Content: "hi"}},
	}
	newProvider(srv, "vk").Call(context.Background(), req) //nolint:errcheck

	if !strings.Contains(decoded.Model, "/") {
		t.Errorf("model ID should contain provider prefix, got %q", decoded.Model)
	}
	if decoded.Model != "openai/gpt-4o-mini" {
		t.Errorf("model: got %q, want openai/gpt-4o-mini", decoded.Model)
	}
}

func TestBifrost_ContentTypeHeader(t *testing.T) {
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		json.NewEncoder(w).Encode(okResponse("ok", 1, 1))
	}))
	defer srv.Close()

	newProvider(srv, "vk").Call(context.Background(), minimalReq()) //nolint:errcheck

	if gotContentType != "application/json" {
		t.Errorf("Content-Type: got %q, want application/json", gotContentType)
	}
}

// ── error handling ────────────────────────────────────────────────────────────

func TestBifrost_GatewayErrorPropagated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"message":"upstream down"}}`))
	}))
	defer srv.Close()

	_, err := newProvider(srv, "vk").Call(context.Background(), minimalReq())
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.HTTPStatusCode != 500 {
		t.Errorf("status: got %d, want 500", apiErr.HTTPStatusCode)
	}
	if apiErr.Message != "upstream down" {
		t.Errorf("message: got %q, want %q", apiErr.Message, "upstream down")
	}
}

func TestProvider_APIErrorWithJSONBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"rate limit exceeded","type":"rate_limit_error"}}`))
	}))
	defer srv.Close()

	_, err := newProvider(srv, "vk").Call(context.Background(), minimalReq())
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.HTTPStatusCode != 429 {
		t.Errorf("status: got %d, want 429", apiErr.HTTPStatusCode)
	}
	if apiErr.Message != "rate limit exceeded" {
		t.Errorf("message: got %q, want %q", apiErr.Message, "rate limit exceeded")
	}
}

func TestProvider_APIErrorEmptyBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := newProvider(srv, "vk").Call(context.Background(), minimalReq())

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T", err)
	}
	if apiErr.HTTPStatusCode != 500 {
		t.Errorf("status: got %d, want 500", apiErr.HTTPStatusCode)
	}
	if apiErr.Message == "" {
		t.Error("expected non-empty fallback message")
	}
}

func TestProvider_MalformedResponseBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`not valid json`))
	}))
	defer srv.Close()

	_, err := newProvider(srv, "vk").Call(context.Background(), minimalReq())
	if err == nil {
		t.Fatal("expected error for malformed JSON response, got nil")
	}
}

func TestProvider_ContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := newProvider(srv, "vk").Call(ctx, minimalReq())
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
}

func TestProvider_ContextTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10)
	defer cancel()

	_, err := newProvider(srv, "vk").Call(ctx, minimalReq())
	if err == nil {
		t.Fatal("expected error for timed-out context, got nil")
	}
}

// timeoutError is a minimal net.Error that reports Timeout() == true.
type timeoutError struct{}

func (e *timeoutError) Error() string   { return "timeout" }
func (e *timeoutError) Timeout() bool   { return true }
func (e *timeoutError) Temporary() bool { return true }

// Ensure timeoutError satisfies net.Error (compile-time check).
var _ net.Error = (*timeoutError)(nil)
