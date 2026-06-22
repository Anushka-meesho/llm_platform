package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"llm_platform_go/internal/auth"
	"llm_platform_go/internal/users"
)

// GET /auth/demo-users
// Lists the seeded demo users so the login screen can render one-click buttons.
// This endpoint exists only for the demo SSO: a real SSO-backed deployment would
// redirect to the IdP instead of exposing the user directory.
func (h *Handler) DemoUsers(w http.ResponseWriter, r *http.Request) {
	list, err := h.Users.List(r.Context())
	if err != nil {
		writeErr(w, r, Internal(CodeInternal, "list users").WithCause(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"users": list})
}

// POST /auth/login  {"user_id": "..."}
// Demo stand-in for an SSO callback: looks the user up in the store, mints a
// signed session token, and sets it as an HttpOnly cookie.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID string `json:"user_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.UserID == "" {
		writeErr(w, r, Unprocessable(CodeValidationFailed, "user_id is required"))
		return
	}

	u, err := h.Users.GetByID(r.Context(), req.UserID)
	if err != nil {
		if errors.Is(err, users.ErrNotFound) {
			writeErr(w, r, Unauthorized(CodeUnauthorized, "unknown user"))
			return
		}
		writeErr(w, r, Internal(CodeInternal, "user lookup").WithCause(err))
		return
	}

	if err := h.issueSession(w, r, &auth.User{Subject: u.ID, Email: u.Email, Name: u.Name, Role: u.Role}); err != nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"user": u})
}

// issueSession mints a signed session token for the resolved principal and sets
// it as the HttpOnly cookie. Shared by the demo Login and the SSO callback so
// both deliver an identical session — only how identity is *established* differs
// (passwordless pick-a-user in demo, IdP handshake in sso). On error it writes
// the response envelope and returns the error so the caller can stop.
func (h *Handler) issueSession(w http.ResponseWriter, r *http.Request, u *auth.User) error {
	token, err := auth.IssueToken(u, h.Auth.Secret, h.Auth.Issuer, h.Auth.TokenExpiry)
	if err != nil {
		writeErr(w, r, Internal(CodeInternal, "issue token").WithCause(err))
		return err
	}
	auth.SetAuthCookie(w, h.Auth.CookieName, token, h.Auth.Domain, h.Auth.Secure,
		int(h.Auth.TokenExpiry.Seconds()))
	return nil
}

// POST /auth/logout — clears the session cookie.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	auth.ClearAuthCookie(w, h.Auth.CookieName, h.Auth.Domain)
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged out"})
}

// GET /auth/me — returns the current user (behind RequireAuth). Used by the
// frontend to bootstrap its auth state on load.
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	u, ok := requireUser(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"user": map[string]string{
			"id":    u.Subject,
			"email": u.Email,
			"name":  u.Name,
			"role":  u.Role,
		},
	})
}
