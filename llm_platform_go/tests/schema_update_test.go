package tests

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestUpdateSchemaAndClear covers the schema-editor backend contract: a schema
// can be replaced via PUT, an invalid schema is rejected (422), and an explicit
// JSON null clears the schema (merge semantics otherwise can't express
// "remove").
func TestUpdateSchemaAndClear(t *testing.T) {
	srv, _ := newPredictServer(t, `{"label":"positive"}`)

	get := func() map[string]json.RawMessage {
		resp, err := http.DefaultClient.Do(authReq(t, http.MethodGet, srv.URL+"/v1/tasks/sentiment", ""))
		if err != nil {
			t.Fatalf("get task: %v", err)
		}
		defer resp.Body.Close()
		var m map[string]json.RawMessage
		if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
			t.Fatalf("decode task: %v", err)
		}
		return m
	}

	// Baseline: the seeded task has both schemas.
	base := get()
	if _, ok := base["output_schema"]; !ok {
		t.Fatalf("expected seeded task to have output_schema")
	}

	// Replace the input schema with a new shape.
	newInput := `{"input_schema":{"type":"object","required":["text","lang"],"properties":{"text":{"type":"string"},"lang":{"type":"string"}}}}`
	resp, err := http.DefaultClient.Do(authReq(t, http.MethodPut, srv.URL+"/v1/tasks/sentiment", newInput))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update input schema: got %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	after := get()
	var inSchema struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(after["input_schema"], &inSchema); err != nil {
		t.Fatalf("decode input_schema: %v", err)
	}
	if len(inSchema.Required) != 2 {
		t.Errorf("input schema required: got %v, want [text lang]", inSchema.Required)
	}

	// An invalid schema is rejected (not a compilable JSON Schema).
	bad := `{"output_schema":{"type":"not-a-real-type"}}`
	resp, err = http.DefaultClient.Do(authReq(t, http.MethodPut, srv.URL+"/v1/tasks/sentiment", bad))
	if err != nil {
		t.Fatalf("put bad: %v", err)
	}
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("invalid schema: got %d, want 422", resp.StatusCode)
	}
	resp.Body.Close()

	// Explicit null clears the output schema.
	resp, err = http.DefaultClient.Do(authReq(t, http.MethodPut, srv.URL+"/v1/tasks/sentiment", `{"output_schema":null}`))
	if err != nil {
		t.Fatalf("put null: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clear output schema: got %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	cleared := get()
	if raw, ok := cleared["output_schema"]; ok && string(raw) != "null" {
		t.Errorf("output_schema should be cleared, got %s", raw)
	}

	// A field omitted from the PUT body is preserved (merge semantics intact):
	// the input schema we set earlier must still be there.
	if _, ok := cleared["input_schema"]; !ok {
		t.Errorf("input_schema should be preserved after clearing output_schema")
	}
}
