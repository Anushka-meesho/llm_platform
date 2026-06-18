package api

import (
	"errors"
	"fmt"
	"net/http"
)

// AppError is the typed error handlers return (or hand to writeErr). It carries
// the HTTP status, a stable machine-readable Code clients can switch on, a
// human-readable Message that is safe to show the caller, and an optional
// wrapped cause. The cause is logged server-side but never sent to the client —
// so a 500's real database/internal error is diagnosable via the logs (keyed by
// request_id) without leaking internals over the wire.
type AppError struct {
	Status  int
	Code    string
	Message string
	cause   error
}

func (e *AppError) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s [%s]: %v", e.Message, e.Code, e.cause)
	}
	return fmt.Sprintf("%s [%s]", e.Message, e.Code)
}

func (e *AppError) Unwrap() error { return e.cause }

// WithCause attaches the underlying error for the server log. Returns the
// receiver so it chains: Internal(CodeDBError, "load task %q", id).WithCause(err).
func (e *AppError) WithCause(err error) *AppError {
	e.cause = err
	return e
}

// Error codes — stable, snake_case, safe for clients to branch on.
const (
	CodeInvalidBody         = "invalid_request_body"
	CodeValidationFailed    = "validation_failed"
	CodeInputValidation     = "input_validation_failed"
	CodeUnauthorized        = "unauthorized"
	CodeForbidden           = "forbidden"
	CodeTaskNotFound        = "task_not_found"
	CodeTaskInactive        = "task_inactive"
	CodeVersionNotFound     = "version_not_found"
	CodeVersionActive       = "version_active"
	CodePlaygroundProtected = "playground_protected"
	CodeRunNotFound         = "run_not_found"
	CodeSessionNotFound     = "session_not_found"
	CodeHealthNotTracked    = "health_not_tracked"
	CodeNotFound            = "not_found"
	CodeBudgetExhausted     = "budget_exhausted"
	CodeNoModelAvailable    = "no_model_available"
	CodeDBError             = "db_error"
	CodeInternal            = "internal"
)

// newAppError builds an AppError; format/args produce the client-facing message.
func newAppError(status int, code, format string, args ...any) *AppError {
	return &AppError{Status: status, Code: code, Message: fmt.Sprintf(format, args...)}
}

// Status-named constructors. The client sees Message; pair with WithCause to log
// the underlying error for 5xx.
func BadRequest(code, format string, args ...any) *AppError {
	return newAppError(http.StatusBadRequest, code, format, args...)
}
func Unauthorized(code, format string, args ...any) *AppError {
	return newAppError(http.StatusUnauthorized, code, format, args...)
}
func Forbidden(code, format string, args ...any) *AppError {
	return newAppError(http.StatusForbidden, code, format, args...)
}
func NotFound(code, format string, args ...any) *AppError {
	return newAppError(http.StatusNotFound, code, format, args...)
}
func Conflict(code, format string, args ...any) *AppError {
	return newAppError(http.StatusConflict, code, format, args...)
}
func Unprocessable(code, format string, args ...any) *AppError {
	return newAppError(http.StatusUnprocessableEntity, code, format, args...)
}
func TooMany(code, format string, args ...any) *AppError {
	return newAppError(http.StatusTooManyRequests, code, format, args...)
}
func BadGateway(code, format string, args ...any) *AppError {
	return newAppError(http.StatusBadGateway, code, format, args...)
}
func Internal(code, format string, args ...any) *AppError {
	return newAppError(http.StatusInternalServerError, code, format, args...)
}

// asAppError coerces any error into an *AppError. A non-AppError becomes a 500
// "internal" whose true cause is preserved for logging but hidden from the client.
func asAppError(err error) *AppError {
	var ae *AppError
	if errors.As(err, &ae) {
		return ae
	}
	return Internal(CodeInternal, "internal error — see server logs").WithCause(err)
}
