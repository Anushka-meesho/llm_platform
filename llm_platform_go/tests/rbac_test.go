package tests

import (
	"bytes"
	"net/http"
	"testing"
	"time"

	"llm_platform_go/internal/auth"
)

// tokenForRole mints a session token for a principal carrying the given role,
// signed with the test secret.
func tokenForRole(t *testing.T, role string) string {
	t.Helper()
	u := &auth.User{Subject: "u-" + role, Email: role + "@demo.local", Name: role, Role: role}
	tok, err := auth.IssueToken(u, []byte(testSecret), testIssuer, time.Hour)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	return tok
}

// roleReq builds a request authenticated as the given role.
func roleReq(t *testing.T, role, method, url, body string) *http.Request {
	t.Helper()
	var r *http.Request
	var err error
	if body == "" {
		r, err = http.NewRequest(method, url, nil)
	} else {
		r, err = http.NewRequest(method, url, bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
	}
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	r.Header.Set("Authorization", "Bearer "+tokenForRole(t, role))
	return r
}

// TestRBACMatrix verifies the gateway enforces the PFS role matrix:
// authoring (write) is split from publishing (deploy), and read-only/predict
// principals cannot mutate task config.
func TestRBACMatrix(t *testing.T) {
	// Predict succeeds against a fake model; a "sentiment" task is registered
	// (by the admin token) at setup.
	srv, _ := newPredictServer(t, `{"label":"positive"}`)

	type check struct {
		name       string
		role       string
		method     string
		path       string
		body       string
		wantStatus int
	}

	updateBody := `{"description":"edited by test"}`
	draftBody := `{"prompt_template":"Classify: {{.text}}","note":"draft"}`
	deployBody := `{"version":1}`
	predictBody := `{"inputs":{"text":"great"}}`

	cases := []check{
		// read: every role can read task config.
		{"viewer reads", auth.RoleViewer, http.MethodGet, "/v1/tasks/sentiment", "", http.StatusOK},
		{"caller reads", auth.RoleCaller, http.MethodGet, "/v1/tasks/sentiment", "", http.StatusOK},
		{"creator reads", auth.RoleCreator, http.MethodGet, "/v1/tasks/sentiment", "", http.StatusOK},

		// predict: caller/creator/approver/admin yes, viewer no.
		{"caller predicts", auth.RoleCaller, http.MethodPost, "/v1/tasks/sentiment/predict", predictBody, http.StatusOK},
		{"creator predicts", auth.RoleCreator, http.MethodPost, "/v1/tasks/sentiment/predict", predictBody, http.StatusOK},
		{"viewer cannot predict", auth.RoleViewer, http.MethodPost, "/v1/tasks/sentiment/predict", predictBody, http.StatusForbidden},

		// write (update / save draft): creator yes, approver/caller/viewer no.
		{"creator updates", auth.RoleCreator, http.MethodPut, "/v1/tasks/sentiment", updateBody, http.StatusOK},
		{"approver cannot update", auth.RoleApprover, http.MethodPut, "/v1/tasks/sentiment", updateBody, http.StatusForbidden},
		{"caller cannot update", auth.RoleCaller, http.MethodPut, "/v1/tasks/sentiment", updateBody, http.StatusForbidden},
		{"viewer cannot save draft", auth.RoleViewer, http.MethodPost, "/v1/tasks/sentiment/versions", draftBody, http.StatusForbidden},
		{"creator saves draft", auth.RoleCreator, http.MethodPost, "/v1/tasks/sentiment/versions", draftBody, http.StatusCreated},

		// deploy (publish gate): approver yes, creator no.
		{"creator cannot deploy", auth.RoleCreator, http.MethodPost, "/v1/tasks/sentiment/deploy", deployBody, http.StatusForbidden},
		{"approver deploys", auth.RoleApprover, http.MethodPost, "/v1/tasks/sentiment/deploy", deployBody, http.StatusOK},

		// create new task: write capability.
		{"caller cannot create", auth.RoleCaller, http.MethodPost, "/v1/tasks", `{"id":"x","name":"X","model":"gpt-4o-mini","prompt_template":"{{.t}}","input_schema":{"type":"object","properties":{"t":{"type":"string"}}}}`, http.StatusForbidden},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, err := http.DefaultClient.Do(roleReq(t, c.role, c.method, srv.URL+c.path, c.body))
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != c.wantStatus {
				t.Errorf("%s: got %d, want %d", c.name, resp.StatusCode, c.wantStatus)
			}
		})
	}
}

// TestDefaultRoleForTokenWithoutClaim confirms a token carrying no role claim
// resolves to the least-privilege caller role: predict + read allowed, write
// denied. This keeps pre-RBAC service tokens (e.g. the client portal's baked
// svc:demo-client token) working without re-minting.
func TestDefaultRoleForTokenWithoutClaim(t *testing.T) {
	srv, _ := newPredictServer(t, `{"label":"positive"}`)

	// Mint a token with an empty role to simulate a legacy/pre-RBAC token.
	u := &auth.User{Subject: "svc:legacy", Email: "legacy@svc.local", Name: "Legacy"}
	tok, err := auth.IssueToken(u, []byte(testSecret), testIssuer, time.Hour)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	mk := func(method, path, body string) *http.Request {
		var r *http.Request
		if body == "" {
			r, _ = http.NewRequest(method, srv.URL+path, nil)
		} else {
			r, _ = http.NewRequest(method, srv.URL+path, bytes.NewBufferString(body))
			r.Header.Set("Content-Type", "application/json")
		}
		r.Header.Set("Authorization", "Bearer "+tok)
		return r
	}

	// Predict allowed.
	resp, err := http.DefaultClient.Do(mk(http.MethodPost, "/v1/tasks/sentiment/predict", `{"inputs":{"text":"ok"}}`))
	if err != nil {
		t.Fatalf("predict: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("legacy token predict: got %d, want 200", resp.StatusCode)
	}

	// Write denied.
	resp, err = http.DefaultClient.Do(mk(http.MethodPut, "/v1/tasks/sentiment", `{"description":"nope"}`))
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("legacy token update: got %d, want 403", resp.StatusCode)
	}
}
