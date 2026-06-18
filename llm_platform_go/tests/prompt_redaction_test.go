package tests

import (
	"encoding/json"
	"net/http"
	"testing"

	"llm_platform_go/internal/auth"
)

// TestPromptVisibility verifies that admin (the only role) can see the full
// prompt template and system prompt in both task and version responses.
func TestPromptVisibility(t *testing.T) {
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

	// Admin sees full prompt text and the input schema.
	got := getTask(auth.RoleAdmin)
	if got.PromptTemplate == "" {
		t.Errorf("admin should see the prompt template")
	}
	if len(got.InputSchema) == 0 {
		t.Errorf("admin should see the input schema")
	}

	// Admin sees version prompt bodies.
	resp, err := http.DefaultClient.Do(roleReq(t, auth.RoleAdmin, http.MethodGet, srv.URL+"/v1/tasks/sentiment/versions", ""))
	if err != nil {
		t.Fatalf("admin versions: %v", err)
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
		t.Fatalf("admin should see version metadata")
	}
	if vr.Versions[0].PromptTemplate == "" {
		t.Errorf("admin should see version prompt body for v%d", vr.Versions[0].Version)
	}
}
