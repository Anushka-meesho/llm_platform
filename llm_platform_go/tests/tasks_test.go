package tests

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appdb "llm_platform_go/internal/db"
	"llm_platform_go/internal/tasks"

	_ "modernc.org/sqlite"
)

func newTaskStore(t *testing.T) *tasks.Store {
	t.Helper()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	database.SetMaxOpenConns(1)
	if err := appdb.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return tasks.NewStore(database)
}

func sampleTask() *tasks.Task {
	return &tasks.Task{
		ID:             "test-task",
		Name:           "Test Task",
		PromptTemplate: "Classify: {{.text}}",
		Model:          "gpt-4o-mini",
		InputSchema:    json.RawMessage(`{"type":"object","required":["text"],"properties":{"text":{"type":"string"}}}`),
		OutputSchema:   json.RawMessage(`{"type":"object","required":["label"],"properties":{"label":{"type":"string"}}}`),
	}
}

func TestTaskCRUDAndVersionBump(t *testing.T) {
	store := newTaskStore(t)

	task := sampleTask()
	if err := store.Create(task); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if task.PromptVersion != 1 {
		t.Errorf("new task prompt_version: got %d, want 1", task.PromptVersion)
	}
	if task.Temperature != 0.2 {
		t.Errorf("default temperature: got %v, want 0.2", task.Temperature)
	}

	got, err := store.Get("test-task")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "Test Task" || !got.Active {
		t.Errorf("round-trip mismatch: %+v", got)
	}

	// Non-prompt update → version unchanged.
	got.Description = "updated"
	if err := store.Update(got); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.PromptVersion != 1 {
		t.Errorf("version after non-prompt update: got %d, want 1", got.PromptVersion)
	}

	// Prompt change → version bump.
	got.PromptTemplate = "Classify better: {{.text}}"
	if err := store.Update(got); err != nil {
		t.Fatalf("Update prompt: %v", err)
	}
	if got.PromptVersion != 2 {
		t.Errorf("version after prompt update: got %d, want 2", got.PromptVersion)
	}

	list, err := store.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("List: %v (len %d, want 1)", err, len(list))
	}

	if _, err := store.Get("nope"); err != tasks.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestTaskValidation(t *testing.T) {
	store := newTaskStore(t)

	bad := sampleTask()
	bad.Model = "unknown-model"
	if err := store.Create(bad); err == nil {
		t.Error("expected error for unknown model")
	}

	bad2 := sampleTask()
	bad2.ID = "Bad ID!"
	if err := store.Create(bad2); err == nil {
		t.Error("expected error for invalid slug")
	}

	bad3 := sampleTask()
	bad3.InputSchema = json.RawMessage(`{"type": 42}`)
	if err := store.Create(bad3); err == nil {
		t.Error("expected error for invalid schema")
	}
}

func TestValidateInputOutput(t *testing.T) {
	task := sampleTask()

	if err := tasks.ValidateInput(task, json.RawMessage(`{"text":"hello"}`)); err != nil {
		t.Errorf("valid input rejected: %v", err)
	}
	if err := tasks.ValidateInput(task, json.RawMessage(`{}`)); err == nil {
		t.Error("missing required field accepted")
	}
	if err := tasks.ValidateInput(task, json.RawMessage(`{"text": 42}`)); err == nil {
		t.Error("wrong type accepted")
	}

	// Output: valid, with code fences stripped.
	out, err := tasks.ValidateOutput(task, "```json\n{\"label\":\"positive\"}\n```")
	if err != nil {
		t.Errorf("valid fenced output rejected: %v", err)
	}
	var parsed map[string]string
	if err := json.Unmarshal(out, &parsed); err != nil || parsed["label"] != "positive" {
		t.Errorf("parsed output wrong: %s", out)
	}

	if _, err := tasks.ValidateOutput(task, `{"wrong":"shape"}`); err == nil {
		t.Error("schema-violating output accepted")
	}
	if _, err := tasks.ValidateOutput(task, "not json at all"); err == nil {
		t.Error("non-JSON output accepted")
	}
}

func TestRenderPrompt(t *testing.T) {
	task := &tasks.Task{
		ID:             "render-test",
		Name:           "Render",
		PromptTemplate: "Title: {{.title}}{{if .notes}} Notes: {{.notes}}{{end}}",
		Model:          "gpt-4o-mini",
		InputSchema: json.RawMessage(`{"type":"object","required":["title"],
			"properties":{"title":{"type":"string"},"notes":{"type":"string"}}}`),
	}

	// Optional declared field omitted → renders without it.
	got, err := tasks.RenderPrompt(task, map[string]any{"title": "Shirt"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if got != "Title: Shirt" {
		t.Errorf("got %q", got)
	}

	// Optional field present → included.
	got2, _ := tasks.RenderPrompt(task, map[string]any{"title": "Shirt", "notes": "blue"})
	if got2 != "Title: Shirt Notes: blue" {
		t.Errorf("got %q", got2)
	}

	// Undeclared key referenced by template → hard error.
	bad := &tasks.Task{PromptTemplate: "{{.missing}}"}
	if _, err := tasks.RenderPrompt(bad, map[string]any{}); err == nil {
		t.Error("expected error for undeclared template key")
	}
}

func TestLoadYAMLDir(t *testing.T) {
	store := newTaskStore(t)
	dir := t.TempDir()

	yaml := `
id: yaml-task
name: YAML Task
model: gpt-4o-mini
prompt_template: "Extract from {{.text}}"
input_schema:
  type: object
  required: [text]
  properties:
    text: {type: string}
`
	if err := os.WriteFile(filepath.Join(dir, "task.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	n, err := tasks.LoadYAMLDir(store, dir)
	if err != nil {
		t.Fatalf("LoadYAMLDir: %v", err)
	}
	if n != 1 {
		t.Errorf("loaded count: got %d, want 1", n)
	}

	got, err := store.Get("yaml-task")
	if err != nil {
		t.Fatalf("Get yaml-task: %v", err)
	}
	if got.PromptVersion != 1 {
		t.Errorf("version: got %d, want 1", got.PromptVersion)
	}

	// Re-seed with a changed prompt → upsert bumps version.
	changed := strings.Replace(yaml, "Extract from {{.text}}", "Pull from {{.text}}", 1)
	if err := os.WriteFile(filepath.Join(dir, "task.yaml"), []byte(changed), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.LoadYAMLDir(store, dir); err != nil {
		t.Fatalf("re-seed: %v", err)
	}
	got2, _ := store.Get("yaml-task")
	if got2.PromptVersion != 2 {
		t.Errorf("version after prompt change: got %d, want 2", got2.PromptVersion)
	}
	if !got2.Active {
		t.Error("re-seeding must not deactivate a task (regression)")
	}

	// Missing dir is not an error.
	if n, err := tasks.LoadYAMLDir(store, filepath.Join(dir, "nope")); err != nil || n != 0 {
		t.Errorf("missing dir: n=%d err=%v", n, err)
	}
}

// TestReseedPreservesLiveRouting pins the fix: a YAML re-seed at startup must
// not clobber a model chain that was changed at runtime. Routing is seeded only
// at first creation and then persists until changed via the API.
func TestReseedPreservesLiveRouting(t *testing.T) {
	store := newTaskStore(t)
	dir := t.TempDir()

	yaml := `
id: routed-task
name: Routed Task
model: gpt-4o-mini
fallback_models: [llama-groq]
prompt_template: "Extract from {{.text}}"
input_schema:
  type: object
  required: [text]
  properties:
    text: {type: string}
`
	path := filepath.Join(dir, "task.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.LoadYAMLDir(store, dir); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Live routing edit (what the UI's "save routing" does).
	got, err := store.Get("routed-task")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	edited := *got
	edited.Model = "gemini-2.5-flash"
	edited.FallbackModels = []string{"gpt-4o-mini"}
	if err := store.Update(&edited); err != nil {
		t.Fatalf("Update routing: %v", err)
	}

	// Re-seed the unchanged YAML (a server restart). Routing must NOT revert.
	if _, err := tasks.LoadYAMLDir(store, dir); err != nil {
		t.Fatalf("re-seed: %v", err)
	}
	after, err := store.Get("routed-task")
	if err != nil {
		t.Fatalf("Get after re-seed: %v", err)
	}
	if after.Model != "gemini-2.5-flash" {
		t.Errorf("re-seed clobbered the live primary: got %q, want gemini-2.5-flash", after.Model)
	}
	if len(after.FallbackModels) != 1 || after.FallbackModels[0] != "gpt-4o-mini" {
		t.Errorf("re-seed clobbered the live fallback chain: got %v, want [gpt-4o-mini]", after.FallbackModels)
	}
}
