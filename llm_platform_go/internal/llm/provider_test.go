package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ── helpers ──────────────────────────────────────────────────────────────────

func okResponse(content string, promptTok, completionTok int) chatResponse {
	return chatResponse{
		Choices: []chatChoice{{Message: chatMessage{Role: "assistant", Content: content}}},
		Usage:   chatUsage{PromptTokens: promptTok, CompletionTokens: completionTok},
	}
}

func newProvider(srv *httptest.Server, key string) *openAICompatProvider {
	return &openAICompatProvider{
		baseURL: srv.URL,
		apiKey:  key,
		client:  srv.Client(),
	}
}

func minimalReq() *chatRequest {
	return &chatRequest{
		Model:       "gpt-4o-mini",
		Messages:    []chatMessage{{Role: "user", Content: "hello"}},
		MaxTokens:   100,
		Temperature: 0.7,
	}
}

// ── openAICompatProvider.Call ─────────────────────────────────────────────────

func TestProvider_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(okResponse("world", 5, 10))
	}))
	defer srv.Close()

	resp, err := newProvider(srv, "test-key").Call(context.Background(), minimalReq())
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

func TestProvider_CorrectHeaders(t *testing.T) {
	var gotAuth, gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		json.NewEncoder(w).Encode(okResponse("ok", 1, 1))
	}))
	defer srv.Close()

	newProvider(srv, "sk-secret").Call(context.Background(), minimalReq()) //nolint:errcheck

	if gotAuth != "Bearer sk-secret" {
		t.Errorf("Authorization: got %q, want %q", gotAuth, "Bearer sk-secret")
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type: got %q, want %q", gotContentType, "application/json")
	}
}

func TestProvider_CorrectEndpointPath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		json.NewEncoder(w).Encode(okResponse("ok", 1, 1))
	}))
	defer srv.Close()

	newProvider(srv, "key").Call(context.Background(), minimalReq()) //nolint:errcheck

	if gotPath != "/chat/completions" {
		t.Errorf("path: got %q, want /chat/completions", gotPath)
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
		Model:       "llama-3.3-70b-versatile",
		Messages:    []chatMessage{{Role: "user", Content: "test"}},
		MaxTokens:   512,
		Temperature: 0.5,
	}
	newProvider(srv, "key").Call(context.Background(), req) //nolint:errcheck

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

func TestProvider_APIErrorWithJSONBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"rate limit exceeded","type":"rate_limit_error"}}`))
	}))
	defer srv.Close()

	_, err := newProvider(srv, "key").Call(context.Background(), minimalReq())
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
		// no body
	}))
	defer srv.Close()

	_, err := newProvider(srv, "key").Call(context.Background(), minimalReq())

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T", err)
	}
	if apiErr.HTTPStatusCode != 500 {
		t.Errorf("status: got %d, want 500", apiErr.HTTPStatusCode)
	}
	// falls back to http.StatusText
	if apiErr.Message == "" {
		t.Error("expected non-empty fallback message")
	}
}

func TestProvider_APIError401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	defer srv.Close()

	_, err := newProvider(srv, "bad-key").Call(context.Background(), minimalReq())

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T", err)
	}
	if apiErr.HTTPStatusCode != 401 {
		t.Errorf("status: got %d, want 401", apiErr.HTTPStatusCode)
	}
}

func TestProvider_MalformedResponseBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`not valid json`))
	}))
	defer srv.Close()

	_, err := newProvider(srv, "key").Call(context.Background(), minimalReq())
	if err == nil {
		t.Fatal("expected error for malformed JSON response, got nil")
	}
}

func TestProvider_ContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// block until client disconnects
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := newProvider(srv, "key").Call(ctx, minimalReq())
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

	_, err := newProvider(srv, "key").Call(ctx, minimalReq())
	if err == nil {
		t.Fatal("expected error for timed-out context, got nil")
	}
}

// ── classifyError ─────────────────────────────────────────────────────────────

func TestClassifyError_DeadlineExceeded(t *testing.T) {
	got := classifyError(context.DeadlineExceeded)
	if got != "Request timed out" {
		t.Errorf("got %q", got)
	}
}

func TestClassifyError_Canceled(t *testing.T) {
	got := classifyError(context.Canceled)
	if got != "Request cancelled" {
		t.Errorf("got %q", got)
	}
}

func TestClassifyError_NetworkTimeout(t *testing.T) {
	got := classifyError(&net.OpError{Op: "dial", Err: &timeoutError{}})
	if got != "Request timed out" {
		t.Errorf("got %q", got)
	}
}

func TestClassifyError_NetworkNonTimeout(t *testing.T) {
	got := classifyError(&net.OpError{Op: "dial", Err: errors.New("connection refused")})
	if got != "Network error — check connectivity" {
		t.Errorf("got %q", got)
	}
}

func TestClassifyError_APIError400(t *testing.T) {
	got := classifyError(&APIError{HTTPStatusCode: 400, Message: "bad input"})
	if got != "Bad request: bad input" {
		t.Errorf("got %q", got)
	}
}

func TestClassifyError_APIError401(t *testing.T) {
	got := classifyError(&APIError{HTTPStatusCode: 401, Message: "unauthorized"})
	if got != "Auth failed — check API key" {
		t.Errorf("got %q", got)
	}
}

func TestClassifyError_APIError404(t *testing.T) {
	got := classifyError(&APIError{HTTPStatusCode: 404, Message: "no such model"})
	if got != "Model or endpoint not found: no such model" {
		t.Errorf("got %q", got)
	}
}

func TestClassifyError_APIError429(t *testing.T) {
	got := classifyError(&APIError{HTTPStatusCode: 429, Message: "rate limit"})
	if got != "Rate limit hit — all retries exhausted" {
		t.Errorf("got %q", got)
	}
}

func TestClassifyError_APIError500(t *testing.T) {
	got := classifyError(&APIError{HTTPStatusCode: 500, Message: "internal"})
	if got != "Provider internal error — all retries exhausted" {
		t.Errorf("got %q", got)
	}
}

func TestClassifyError_APIError503(t *testing.T) {
	got := classifyError(&APIError{HTTPStatusCode: 503, Message: "down"})
	if got != "Service unavailable — all retries exhausted" {
		t.Errorf("got %q", got)
	}
}

func TestClassifyError_APIErrorOther(t *testing.T) {
	got := classifyError(&APIError{HTTPStatusCode: 422, Message: "unprocessable"})
	if got != "API error 422: unprocessable" {
		t.Errorf("got %q", got)
	}
}

func TestClassifyError_Unexpected(t *testing.T) {
	got := classifyError(errors.New("something weird"))
	if got != "Unexpected error: something weird" {
		t.Errorf("got %q", got)
	}
}

// timeoutError is a minimal net.Error that reports Timeout() == true.
type timeoutError struct{}

func (e *timeoutError) Error() string   { return "timeout" }
func (e *timeoutError) Timeout() bool   { return true }
func (e *timeoutError) Temporary() bool { return true }
