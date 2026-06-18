package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	chimw "github.com/go-chi/chi/v5/middleware"
)

// reqWithID builds a request whose context carries a chi request id, the way the
// RequestID middleware would have set it in production.
func reqWithID(id string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/v1/things", nil)
	return r.WithContext(context.WithValue(r.Context(), chimw.RequestIDKey, id))
}

func decodeEnvelope(t *testing.T, rec *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response body is not JSON: %v (%q)", err, rec.Body.String())
	}
	return body
}

func TestWriteErr_AppErrorEnvelope(t *testing.T) {
	rec := httptest.NewRecorder()
	writeErr(rec, reqWithID("req-123"), NotFound(CodeTaskNotFound, "task %q not found", "sentiment"))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if got := rec.Header().Get("X-Request-ID"); got != "req-123" {
		t.Errorf("X-Request-ID header = %q, want %q", got, "req-123")
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	body := decodeEnvelope(t, rec)
	if body["detail"] != `task "sentiment" not found` {
		t.Errorf("detail = %q", body["detail"])
	}
	if body["code"] != CodeTaskNotFound {
		t.Errorf("code = %q, want %q", body["code"], CodeTaskNotFound)
	}
	if body["request_id"] != "req-123" {
		t.Errorf("request_id = %q, want req-123", body["request_id"])
	}
}

func TestWriteErr_UnknownErrorBecomes500Internal(t *testing.T) {
	// A bare error (not an *AppError) must not leak its message to the client; it
	// becomes a generic 500 "internal", with the real cause available to logs.
	rec := httptest.NewRecorder()
	secret := errors.New("pq: password authentication failed for user 'admin'")
	writeErr(rec, reqWithID("req-9"), secret)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	body := decodeEnvelope(t, rec)
	if body["code"] != CodeInternal {
		t.Errorf("code = %q, want %q", body["code"], CodeInternal)
	}
	if body["detail"] == secret.Error() {
		t.Errorf("client message leaked the internal cause: %q", body["detail"])
	}
	if body["request_id"] != "req-9" {
		t.Errorf("request_id = %q, want req-9", body["request_id"])
	}
}

func TestWriteError_StatusAndCode(t *testing.T) {
	rec := httptest.NewRecorder()
	writeError(rec, reqWithID("r1"), http.StatusForbidden, CodeForbidden, "admin role required")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	body := decodeEnvelope(t, rec)
	if body["code"] != CodeForbidden || body["detail"] != "admin role required" {
		t.Errorf("envelope = %+v", body)
	}
}

func TestAsAppError(t *testing.T) {
	ae := Conflict(CodeTaskInactive, "task is inactive")
	if got := asAppError(ae); got != ae {
		t.Errorf("asAppError should pass *AppError through unchanged")
	}
	// Wrapped AppError is still recovered via errors.As.
	wrapped := fmt.Errorf("while predicting: %w", ae)
	if got := asAppError(wrapped); got != ae {
		t.Errorf("asAppError should unwrap to the inner *AppError")
	}
	// A plain error maps to 500 internal and preserves the cause for logs.
	plain := errors.New("boom")
	got := asAppError(plain)
	if got.Status != http.StatusInternalServerError || got.Code != CodeInternal {
		t.Errorf("plain error mapped to %d/%s, want 500/%s", got.Status, got.Code, CodeInternal)
	}
	if !errors.Is(got, plain) {
		t.Errorf("cause not preserved: Unwrap()=%v", got.Unwrap())
	}
}

func TestWriteErr_NoRequestIDStillWorks(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/x", nil) // no request id in context
	writeErr(rec, r, BadRequest(CodeInvalidBody, "bad body"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if got := rec.Header().Get("X-Request-ID"); got != "" {
		t.Errorf("X-Request-ID should be empty when none is set, got %q", got)
	}
	body := decodeEnvelope(t, rec)
	if body["request_id"] != "" {
		t.Errorf("request_id = %q, want empty", body["request_id"])
	}
}
