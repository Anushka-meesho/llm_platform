package tasks

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strconv"
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

// ValidateOutput turns a model's raw response into the value handed back to the
// API caller, according to the task's output schema — the response contract.
//
// The schema's top-level "type" declares what the client receives (string,
// number, integer, boolean, object, or array). Because models answer in plain
// text, the response is first *coerced* into that declared type — `42` becomes
// the JSON number 42, any text becomes a JSON string — and then validated
// against the full schema so constraints (enum, pattern, minimum/maximum,
// min/maxLength, min/maxItems, …) still apply. The returned JSON is the coerced,
// validated value. Tasks without a schema get the raw text back unparsed.
//
// Coercion or validation failure is returned as an error, which the prediction
// pipeline treats as a schema-invalid output (flagged invalid; advances the
// fallback chain) — never a panic.
func ValidateOutput(t *Task, raw string) (json.RawMessage, error) {
	if len(t.OutputSchema) == 0 {
		return nil, nil
	}
	cleaned := StripCodeFences(raw)
	instance, err := coerceToSchemaType(t.OutputSchema, cleaned)
	if err != nil {
		return nil, err
	}
	if err := validateAgainst(t.OutputSchema, instance); err != nil {
		return nil, err
	}
	return instance, nil
}

// schemaTopType returns an output schema's top-level "type" as a string, or ""
// when the type is absent or expressed as a list (e.g. ["string","null"]) — in
// which case the response is treated as opaque JSON (object/array path).
func schemaTopType(schemaRaw json.RawMessage) string {
	var meta struct {
		Type any `json:"type"`
	}
	if err := json.Unmarshal(schemaRaw, &meta); err != nil {
		return ""
	}
	if s, ok := meta.Type.(string); ok {
		return s
	}
	return ""
}

// coerceToSchemaType converts the model's plain-text response into a JSON value
// of the schema's declared top-level type. Scalars are parsed leniently: a bare
// `42`, a JSON-quoted `"42"`, and surrounding whitespace all coerce to the
// number 42; any text coerces to a string. object/array (and untyped or
// multi-type schemas) keep the original "must be valid JSON" behavior.
func coerceToSchemaType(schemaRaw json.RawMessage, cleaned string) (json.RawMessage, error) {
	switch schemaTopType(schemaRaw) {
	case "string":
		return json.Marshal(unwrapJSONString(cleaned))
	case "number":
		f, err := strconv.ParseFloat(unwrapScalar(cleaned), 64)
		if err != nil {
			return nil, fmt.Errorf("expected a number, got %q", cleaned)
		}
		return json.Marshal(f)
	case "integer":
		i, err := strconv.ParseInt(unwrapScalar(cleaned), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("expected an integer, got %q", cleaned)
		}
		return json.Marshal(i)
	case "boolean":
		b, err := strconv.ParseBool(strings.ToLower(unwrapScalar(cleaned)))
		if err != nil {
			return nil, fmt.Errorf("expected a boolean, got %q", cleaned)
		}
		return json.Marshal(b)
	default: // object, array, multi-type, or untyped → opaque JSON
		if !json.Valid([]byte(cleaned)) {
			return nil, fmt.Errorf("expected JSON output of the declared type")
		}
		return json.RawMessage(cleaned), nil
	}
}

// unwrapJSONString returns the string value the model intended: if the text is
// itself a JSON string (`"positive"`) it is decoded to its content; otherwise
// the whole text is the value. This lets enum/pattern checks match either form.
func unwrapJSONString(s string) string {
	var v string
	if err := json.Unmarshal([]byte(strings.TrimSpace(s)), &v); err == nil {
		return v
	}
	return s
}

// unwrapScalar trims whitespace and unwraps one layer of JSON-string quoting so
// both `42` and `"42"` parse as the scalar 42.
func unwrapScalar(s string) string {
	s = strings.TrimSpace(s)
	var v string
	if err := json.Unmarshal([]byte(s), &v); err == nil {
		return strings.TrimSpace(v)
	}
	return s
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
