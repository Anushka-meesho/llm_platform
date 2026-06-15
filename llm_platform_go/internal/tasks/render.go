package tasks

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"text/template"
)

// Parsed templates are cached by content hash (same strategy as schemas).
var (
	tmplMu    sync.Mutex
	tmplCache = map[[32]byte]*template.Template{}
)

func parseTemplate(text string) (*template.Template, error) {
	key := sha256.Sum256([]byte(text))

	tmplMu.Lock()
	defer tmplMu.Unlock()
	if t, ok := tmplCache[key]; ok {
		return t, nil
	}

	// missingkey=error: referencing an input the caller didn't send is a hard
	// error, not a silently empty substitution.
	t, err := template.New("prompt").Option("missingkey=error").Parse(text)
	if err != nil {
		return nil, err
	}
	tmplCache[key] = t
	return t, nil
}

// RenderPrompt fills the task's prompt template with the request inputs.
// Template syntax is Go text/template: {{.title}}, {{.category}}, ….
//
// Fields declared in the input schema but absent from the request are filled
// with "" so optional fields work with {{if .field}} guards, while referencing
// a completely undeclared key still fails loudly (missingkey=error).
func RenderPrompt(t *Task, inputs map[string]any) (string, error) {
	tmpl, err := parseTemplate(t.PromptTemplate)
	if err != nil {
		return "", fmt.Errorf("parse prompt template: %w", err)
	}

	data := make(map[string]any, len(inputs))
	for _, k := range declaredInputFields(t) {
		data[k] = ""
	}
	for k, v := range inputs {
		data[k] = v
	}

	var sb strings.Builder
	if err := tmpl.Execute(&sb, data); err != nil {
		return "", fmt.Errorf("render prompt: %w", err)
	}
	return sb.String(), nil
}

// declaredInputFields lists the property names of the task's input schema.
func declaredInputFields(t *Task) []string {
	if len(t.InputSchema) == 0 {
		return nil
	}
	var s struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(t.InputSchema, &s); err != nil {
		return nil
	}
	keys := make([]string, 0, len(s.Properties))
	for k := range s.Properties {
		keys = append(keys, k)
	}
	return keys
}
