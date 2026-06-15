package tasks

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Compiled schemas are cached by content hash, so task updates (which change
// the schema bytes) naturally miss the cache and recompile.
var (
	schemaMu    sync.Mutex
	schemaCache = map[[32]byte]*jsonschema.Schema{}
)

func compileSchema(raw json.RawMessage) (*jsonschema.Schema, error) {
	key := sha256.Sum256(raw)

	schemaMu.Lock()
	defer schemaMu.Unlock()
	if s, ok := schemaCache[key]; ok {
		return s, nil
	}

	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("parse schema: %w", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("schema.json", doc); err != nil {
		return nil, fmt.Errorf("add schema resource: %w", err)
	}
	s, err := c.Compile("schema.json")
	if err != nil {
		return nil, fmt.Errorf("compile schema: %w", err)
	}
	schemaCache[key] = s
	return s, nil
}

// ValidateInput checks a request's inputs against the task's input schema.
// Tasks without a schema accept any input object.
func ValidateInput(t *Task, inputs json.RawMessage) error {
	if len(t.InputSchema) == 0 {
		return nil
	}
	return validateAgainst(t.InputSchema, inputs)
}

// ValidateOutput checks a model response against the task's output schema.
// Returns the cleaned JSON (code fences stripped) so callers can store/return
// the parsed object. Tasks without a schema get the raw text back unparsed.
func ValidateOutput(t *Task, raw string) (json.RawMessage, error) {
	cleaned := StripCodeFences(raw)
	if len(t.OutputSchema) == 0 {
		return nil, nil
	}
	if err := validateAgainst(t.OutputSchema, json.RawMessage(cleaned)); err != nil {
		return nil, err
	}
	return json.RawMessage(cleaned), nil
}

func validateAgainst(schemaRaw, instanceRaw json.RawMessage) error {
	s, err := compileSchema(schemaRaw)
	if err != nil {
		return err
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(instanceRaw))
	if err != nil {
		return fmt.Errorf("not valid JSON: %w", err)
	}
	if err := s.Validate(inst); err != nil {
		return err
	}
	return nil
}

// StripCodeFences removes a single wrapping markdown code fence (``` or
// ```json) that LLMs commonly add around JSON output.
func StripCodeFences(s string) string {
	trimmed := strings.TrimSpace(s)
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed
	}
	trimmed = strings.TrimPrefix(trimmed, "```")
	// Drop an optional language tag on the fence line.
	if i := strings.IndexByte(trimmed, '\n'); i >= 0 {
		first := strings.TrimSpace(trimmed[:i])
		if len(first) <= 10 { // "json", "yaml", etc. — not actual content
			trimmed = trimmed[i+1:]
		}
	}
	trimmed = strings.TrimSuffix(strings.TrimSpace(trimmed), "```")
	return strings.TrimSpace(trimmed)
}
