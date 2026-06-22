package api

import (
	"net/http"
	"os"
)

// SSO login flow (AUTH_MODE=sso). This is the integration seam for a real
// identity provider (Meesho's IdP / any OIDC provider). It is wired into the
// router in sso mode in place of the demo pick-a-user login, and the
// session-minting tail (issueSession) is shared with the demo path — so once the
// IdP handshake below is filled in, an SSO login produces exactly the same
// signed session cookie the rest of the platform already understands.
//
// What remains to make this production-complete (needs the IdP's specifics):
//  1. Build the authorize URL from OIDC_* env (issuer, client id, redirect URI,
//     scopes) and redirect with a signed/stored `state` (CSRF) in SSOLogin.
//  2. In SSOCallback: verify `state`, exchange `code` for tokens at the IdP,
//     validate the ID token, and read identity (sub/email/name) + group claims.
//  3. Map the IdP groups to an RBAC role (see internal/auth/rbac.go) and resolve
//     or provision the user via a real users.Store, then call issueSession and
//     redirect to OIDC_POST_LOGIN_URL (the Studio origin).
//
// A standard implementation uses golang.org/x/oauth2 + github.com/coreos/go-oidc.
// Until the IdP config is supplied these handlers return 501 so the route exists
// and is documented but cannot mint a session from unvalidated input.

// oidcConfigured reports whether the minimum OIDC env is present.
func oidcConfigured() bool {
	return os.Getenv("OIDC_ISSUER") != "" &&
		os.Getenv("OIDC_CLIENT_ID") != "" &&
		os.Getenv("OIDC_REDIRECT_URL") != ""
}

// GET /auth/sso/login — begin the IdP login (redirect to the provider).
func (h *Handler) SSOLogin(w http.ResponseWriter, r *http.Request) {
	if !oidcConfigured() {
		writeErr(w, r, newAppError(http.StatusNotImplemented, CodeInternal,
			"SSO is not configured: set OIDC_ISSUER, OIDC_CLIENT_ID, OIDC_CLIENT_SECRET, OIDC_REDIRECT_URL and complete the IdP handshake in auth_sso_handlers.go"))
		return
	}
	// TODO(sso): build authorize URL + state and http.Redirect to the IdP.
	writeErr(w, r, newAppError(http.StatusNotImplemented, CodeInternal,
		"SSO login handshake not yet implemented — see auth_sso_handlers.go"))
}

// GET /auth/sso/callback — IdP redirects back here with ?code&state.
func (h *Handler) SSOCallback(w http.ResponseWriter, r *http.Request) {
	if !oidcConfigured() {
		writeErr(w, r, newAppError(http.StatusNotImplemented, CodeInternal,
			"SSO is not configured — see auth_sso_handlers.go"))
		return
	}
	// TODO(sso): verify state, exchange code, validate ID token, extract identity
	// + groups, map to an RBAC role, resolve the user via users.Store, then:
	//
	//	if err := h.issueSession(w, r, &auth.User{Subject: sub, Email: email, Name: name, Role: role}); err != nil {
	//		return
	//	}
	//	http.Redirect(w, r, os.Getenv("OIDC_POST_LOGIN_URL"), http.StatusFound)
	writeErr(w, r, newAppError(http.StatusNotImplemented, CodeInternal,
		"SSO callback handshake not yet implemented — see auth_sso_handlers.go"))
}
