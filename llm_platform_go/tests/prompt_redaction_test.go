package tests

import (
	"encoding/json"
	"net/http"
	"testing"

	"llm_platform_go/internal/auth"
)

// TestPromptRedaction verifies that service callers (no task:view_prompt) get
// task metadata and schemas but NOT the prompt template / system prompt, while
// roles that work on tasks (creator, viewer, admin) see the full prompt.
func TestPromptRedaction(t *testing.T) {
	srv, _ := newPredictServer(t, `{"label":"positive"}`)

	type taskResp struct {
		PromptTemplate string          `json:"prompt_template"`
		SystemPrompt   string          `json:"system_prompt"`
		InputSchema    json.RawMessage `json:"input_schema"`
	}
	getTask := func(role string) taskResp {
		resp, err := http.DefaultClient.Do(roleReq(t, role, http.MethodGet, srv.URL+"/v1/tasks/sentiment", ""))
		if err != nil {
			t.Fatalf("%s get task: %v", role, err)
		}
		defer resp.Body.Close()
		var tr taskResp
		_ = json.NewDecoder(resp.Body).Decode(&tr)
		return tr
	}

	// Caller: prompt blanked, but the contract (input schema) preserved.
	caller := getTask(auth.RoleCaller)
	if caller.PromptTemplate != "" || caller.SystemPrompt != "" {
		t.Errorf("caller should not see prompt text, got template=%q system=%q", caller.PromptTemplate, caller.SystemPrompt)
	}
	if len(caller.InputSchema) == 0 {
		t.Errorf("caller should still see the input schema (the task contract)")
	}

	// Creator and viewer: full prompt visible.
	for _, role := range []string{auth.RoleCreator, auth.RoleViewer, auth.RoleAdmin} {
		got := getTask(role)
		if got.PromptTemplate == "" {
			t.Errorf("%s should see the prompt template", role)
		}
	}

	// Version history: caller gets metadata but not the prompt bodies.
	resp, err := http.DefaultClient.Do(roleReq(t, auth.RoleCaller, http.MethodGet, srv.URL+"/v1/tasks/sentiment/versions", ""))
	if err != nil {
		t.Fatalf("caller versions: %v", err)
	}
	defer resp.Body.Close()
	var vr struct {
		Versions []struct {
			Version        int    `json:"version"`
			PromptTemplate string `json:"prompt_template"`
		} `json:"versions"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&vr)
	if len(vr.Versions) == 0 {
		t.Fatalf("caller should still see version metadata")
	}
	for _, v := range vr.Versions {
		if v.PromptTemplate != "" {
			t.Errorf("caller should not see prompt body for v%d", v.Version)
		}
	}

	// Creator sees the version prompt body.
	resp2, err := http.DefaultClient.Do(roleReq(t, auth.RoleCreator, http.MethodGet, srv.URL+"/v1/tasks/sentiment/versions", ""))
	if err != nil {
		t.Fatalf("creator versions: %v", err)
	}
	defer resp2.Body.Close()
	var vr2 struct {
		Versions []struct {
			PromptTemplate string `json:"prompt_template"`
		} `json:"versions"`
	}
	_ = json.NewDecoder(resp2.Body).Decode(&vr2)
	if len(vr2.Versions) == 0 || vr2.Versions[0].PromptTemplate == "" {
		t.Errorf("creator should see version prompt bodies")
	}
}
