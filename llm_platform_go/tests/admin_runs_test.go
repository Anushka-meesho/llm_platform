package tests

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// TestAdminRunsListAndDetail exercises the admin prompt-history endpoints:
// a prediction creates a run, the admin list surfaces it with a truncated
// preview, and the detail endpoint returns the full prompt + per-model results.
func TestAdminRunsListAndDetail(t *testing.T) {
	srv, _ := newPredictServer(t, `{"label":"positive"}`)

	// Produce a run as the admin caller.
	predictBody := `{"inputs":{"text":"I love this"}}`
	resp, err := http.DefaultClient.Do(authReq(t, http.MethodPost,
		srv.URL+"/v1/tasks/sentiment/predict", predictBody))
	if err != nil {
		t.Fatalf("predict: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("predict status: %d", resp.StatusCode)
	}
	var predicted struct {
		TaskRunID string `json:"task_run_id"`
	}
	json.NewDecoder(resp.Body).Decode(&predicted)
	resp.Body.Close()
	if predicted.TaskRunID == "" {
		t.Fatal("predict returned no task_run_id")
	}

	// List: the run must appear, with a prompt preview and zero images.
	resp, err = http.DefaultClient.Do(authReq(t, http.MethodGet, srv.URL+"/v1/admin/runs", ""))
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("list status: %d (%s)", resp.StatusCode, body)
	}
	var list struct {
		TotalRuns int `json:"total_runs"`
		Runs      []struct {
			RunID         string `json:"run_id"`
			TaskID        string `json:"task_id"`
			Model         string `json:"model"`
			PromptPreview string `json:"prompt_preview"`
			HasImage      bool   `json:"has_image"`
			Success       bool   `json:"success"`
		} `json:"runs"`
	}
	json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if list.TotalRuns < 1 || len(list.Runs) < 1 {
		t.Fatalf("expected at least one run, got total=%d len=%d", list.TotalRuns, len(list.Runs))
	}
	first := list.Runs[0]
	if first.PromptPreview == "" || first.HasImage {
		t.Errorf("unexpected list row: %+v", first)
	}

	// Filter by a non-matching task → empty page.
	resp, _ = http.DefaultClient.Do(authReq(t, http.MethodGet, srv.URL+"/v1/admin/runs?task_id=does-not-exist", ""))
	var filtered struct {
		TotalRuns int `json:"total_runs"`
	}
	json.NewDecoder(resp.Body).Decode(&filtered)
	resp.Body.Close()
	if filtered.TotalRuns != 0 {
		t.Errorf("filter on missing task should be empty, got %d", filtered.TotalRuns)
	}

	// Detail: full prompt + at least one result.
	resp, err = http.DefaultClient.Do(authReq(t, http.MethodGet,
		srv.URL+"/v1/admin/runs/"+predicted.TaskRunID, ""))
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("detail status: %d", resp.StatusCode)
	}
	var detail struct {
		RunID   string `json:"run_id"`
		Prompt  string `json:"prompt"`
		Images  []string
		Results []struct {
			Model   string `json:"model"`
			Success bool   `json:"success"`
		} `json:"results"`
	}
	json.NewDecoder(resp.Body).Decode(&detail)
	resp.Body.Close()
	if detail.RunID != predicted.TaskRunID || detail.Prompt == "" || len(detail.Results) < 1 {
		t.Errorf("unexpected detail: %+v", detail)
	}

	// Unknown run → 404.
	resp, _ = http.DefaultClient.Do(authReq(t, http.MethodGet, srv.URL+"/v1/admin/runs/nope", ""))
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown run: got %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
}

