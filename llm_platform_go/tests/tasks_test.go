package tests

import (
	"database/sql"
	"encoding/json"
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
