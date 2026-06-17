package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"llm_platform_go/internal/auth"
)

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"detail": msg})
}

// RequireAuth validates the session token (cookie or Bearer header) and puts
// the resolved user on the request context. Returns 401 on any failure.
func RequireAuth(secret []byte, cookieName string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tokenStr, err := auth.TokenFromRequest(r, cookieName)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "authentication required")
				return
			}
			user, err := auth.ParseToken(tokenStr, secret)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "invalid or expired session")
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
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		if user.Role != auth.RoleAdmin {
			writeError(w, http.StatusForbidden, "admin role required")
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
				writeError(w, http.StatusUnauthorized, "authentication required")
				return
			}
			if !user.Can(perm) {
				writeError(w, http.StatusForbidden,
					fmt.Sprintf("role %q is not permitted to %s", user.Role, perm))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
