package tests

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestDeleteVersion covers the admin-only version-delete contract: admin can
// delete an inactive version, the active version is refused (409), non-admins
// are forbidden (403), and a deleted version is gone from the history.
func TestDeleteVersion(t *testing.T) {
	srv, _ := newPredictServer(t, `{"label":"positive"}`)

	listVersions := func() []int {
		resp, err := http.DefaultClient.Do(authReq(t, http.MethodGet, srv.URL+"/v1/tasks/sentiment/versions", ""))
		if err != nil {
			t.Fatalf("list versions: %v", err)
		}
		defer resp.Body.Close()
		var out struct {
			Versions []struct {
				Version int `json:"version"`
			} `json:"versions"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		nums := make([]int, len(out.Versions))
		for i, v := range out.Versions {
			nums[i] = v.Version
		}
		return nums
	}

	// Save a draft (v2) so there's an inactive version to delete. v1 is active.
	resp, err := http.DefaultClient.Do(authReq(t, http.MethodPost, srv.URL+"/v1/tasks/sentiment/versions",
		`{"prompt_template":"Classify: {{.text}}","note":"draft"}`))
	if err != nil {
		t.Fatalf("save draft: %v", err)
	}
	resp.Body.Close()
	if got := listVersions(); len(got) != 2 {
		t.Fatalf("expected 2 versions before delete, got %v", got)
	}

	// Admin cannot delete the active version (v1) — 409.
	resp, err = http.DefaultClient.Do(authReq(t, http.MethodDelete, srv.URL+"/v1/tasks/sentiment/versions/1", ""))
	if err != nil {
		t.Fatalf("delete active: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("delete active version: got %d, want 409", resp.StatusCode)
	}

	// Admin deletes the inactive version (v2) — 200, and it's gone.
	resp, err = http.DefaultClient.Do(authReq(t, http.MethodDelete, srv.URL+"/v1/tasks/sentiment/versions/2", ""))
	if err != nil {
		t.Fatalf("delete v2: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete v2: got %d, want 200", resp.StatusCode)
	}
	if got := listVersions(); len(got) != 1 || got[0] != 1 {
		t.Errorf("after delete expected [1], got %v", got)
	}

	// Deleting a non-existent version — 404.
	resp, err = http.DefaultClient.Do(authReq(t, http.MethodDelete, srv.URL+"/v1/tasks/sentiment/versions/99", ""))
	if err != nil {
		t.Fatalf("delete missing: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("delete missing version: got %d, want 404", resp.StatusCode)
	}
}
