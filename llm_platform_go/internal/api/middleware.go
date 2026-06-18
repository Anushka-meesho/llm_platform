package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"

	chimw "github.com/go-chi/chi/v5/middleware"

	"llm_platform_go/internal/auth"
)

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// writeErr writes any error as the standard JSON envelope, sets the X-Request-ID
// response header, and logs it server-side so a failed request is always
// traceable. The client gets {detail, code, request_id}; the full wrapped cause
// (e.g. the raw DB error behind a 500) goes only to the log, correlated by the
// same request_id the caller sees.
func writeErr(w http.ResponseWriter, r *http.Request, err error) {
	ae := asAppError(err)
	reqID := chimw.GetReqID(r.Context())
	if reqID != "" {
		w.Header().Set("X-Request-ID", reqID)
	}

	attrs := []any{
		"request_id", reqID,
		"method", r.Method,
		"path", r.URL.Path,
		"code", ae.Code,
		"status", ae.Status,
		"error", ae.Error(),
	}
	if ae.Status >= http.StatusInternalServerError {
		slog.Error("request failed", attrs...)
	} else {
		slog.Warn("request rejected", attrs...)
	}

	writeJSON(w, ae.Status, map[string]string{
		"detail":     ae.Message,
		"code":       ae.Code,
		"request_id": reqID,
	})
}

// writeError is the call-site convenience for client-facing errors with no
// underlying cause to log. For 5xx with an underlying error prefer
// writeErr(w, r, Internal(code, msg).WithCause(err)) so the cause is logged.
func writeError(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
	writeErr(w, r, &AppError{Status: status, Code: code, Message: msg})
}

// recoverer catches panics, logs them with the request id + stack, and returns
// the standard 500 envelope instead of dropping the connection. Replaces chi's
// middleware.Recoverer so panics are diagnosable like every other error.
func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil && rec != http.ErrAbortHandler {
				slog.Error("panic recovered",
					"request_id", chimw.GetReqID(r.Context()),
					"method", r.Method,
					"path", r.URL.Path,
					"panic", fmt.Sprintf("%v", rec),
					"stack", string(debug.Stack()),
				)
				writeErr(w, r, Internal(CodeInternal, "internal error — see server logs"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// RequireAuth validates the session token (cookie or Bearer header) and puts
// the resolved user on the request context. Returns 401 on any failure.
func RequireAuth(secret []byte, cookieName string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tokenStr, err := auth.TokenFromRequest(r, cookieName)
			if err != nil {
				writeError(w, r, http.StatusUnauthorized, CodeUnauthorized, "authentication required")
				return
			}
			user, err := auth.ParseToken(tokenStr, secret)
			if err != nil {
				writeError(w, r, http.StatusUnauthorized, CodeUnauthorized, "invalid or expired session")
				return
			}
			next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), user)))
		})
	}
}

// RequireAdmin gates a route on the admin role. It is stricter than any single
// capability: the admin prompt-history endpoints expose every user's prompts and
// responses (a privacy-sensitive, cross-tenant view), so they are held to the
// superuser role rather than a task-scoped permission. Layer it after RequireAuth.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := auth.FromContext(r.Context())
		if !ok || user == nil {
			writeError(w, r, http.StatusUnauthorized, CodeUnauthorized, "authentication required")
			return
		}
		if user.Role != auth.RoleAdmin {
			writeError(w, r, http.StatusForbidden, CodeForbidden, "admin role required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequirePermission gates a route on an RBAC capability. It must be layered
// after RequireAuth (it reads the user the latter placed on the context) and
// returns 403 when the authenticated principal's role lacks the permission.
func RequirePermission(perm auth.Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, ok := auth.FromContext(r.Context())
			if !ok || user == nil {
				writeError(w, r, http.StatusUnauthorized, CodeUnauthorized, "authentication required")
				return
			}
			if !user.Can(perm) {
				writeError(w, r, http.StatusForbidden, CodeForbidden,
					fmt.Sprintf("role %q is not permitted to %s", user.Role, perm))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
