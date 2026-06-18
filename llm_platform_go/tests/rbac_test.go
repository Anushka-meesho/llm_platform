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

// TestRBACMatrix verifies that admin can perform every operation.
func TestRBACMatrix(t *testing.T) {
	srv, _ := newPredictServer(t, `{"label":"positive"}`)

	type check struct {
		name       string
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
		{"admin reads", http.MethodGet, "/v1/tasks/sentiment", "", http.StatusOK},
		{"admin predicts", http.MethodPost, "/v1/tasks/sentiment/predict", predictBody, http.StatusOK},
		{"admin updates", http.MethodPut, "/v1/tasks/sentiment", updateBody, http.StatusOK},
		{"admin saves draft", http.MethodPost, "/v1/tasks/sentiment/versions", draftBody, http.StatusCreated},
		{"admin deploys", http.MethodPost, "/v1/tasks/sentiment/deploy", deployBody, http.StatusOK},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, err := http.DefaultClient.Do(roleReq(t, auth.RoleAdmin, c.method, srv.URL+c.path, c.body))
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
// resolves to admin (DefaultRole), so legacy tokens get full access.
func TestDefaultRoleForTokenWithoutClaim(t *testing.T) {
	srv, _ := newPredictServer(t, `{"label":"positive"}`)

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

	// Predict allowed (default resolves to admin).
	resp, err := http.DefaultClient.Do(mk(http.MethodPost, "/v1/tasks/sentiment/predict", `{"inputs":{"text":"ok"}}`))
	if err != nil {
		t.Fatalf("predict: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("legacy token predict: got %d, want 200", resp.StatusCode)
	}

	// Write allowed (default resolves to admin).
	resp, err = http.DefaultClient.Do(mk(http.MethodPut, "/v1/tasks/sentiment", `{"description":"ok"}`))
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("legacy token update: got %d, want 200", resp.StatusCode)
	}
}
