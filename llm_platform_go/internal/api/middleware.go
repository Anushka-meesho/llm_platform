package api

import (
	"encoding/json"
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
