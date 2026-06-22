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
//
// Image fields are special: their raw value (a base64 data URL, often tens of KB)
// must never land in the prompt text — images travel to the model as image_url
// attachments, and inlining them would bloat the prompt and blow the input-size
// estimate. So an image field is exposed to the template as its image *count*
// instead: {{if .image}} still gates on presence, and {{.image}} renders a small
// number rather than dumping the bytes.
func RenderPrompt(t *Task, inputs map[string]any) (string, error) {
	tmpl, err := parseTemplate(t.PromptTemplate)
	if err != nil {
		return "", fmt.Errorf("parse prompt template: %w", err)
	}

	imageFields := imageInputFields(t)
	data := make(map[string]any, len(inputs))
	for _, k := range declaredInputFields(t) {
		data[k] = ""
	}
	for k, v := range inputs {
		if imageFields[k] {
			data[k] = imageValueCount(v) // presence as a count, never the bytes
		} else {
			data[k] = v
		}
	}

	var sb strings.Builder
	if err := tmpl.Execute(&sb, data); err != nil {
		return "", fmt.Errorf("render prompt: %w", err)
	}
	return sb.String(), nil
}

// imageInputFields returns the set of input field names whose values are images:
// schema properties typed as images (a string, or an array's items, tagged
// format:"image") plus the implicit "image"/"images" names. Their values are
// never rendered into the prompt text (see RenderPrompt).
func imageInputFields(t *Task) map[string]bool {
	out := map[string]bool{"image": true, "images": true}
	if len(t.InputSchema) == 0 {
		return out
	}
	var s struct {
		Properties map[string]struct {
			Type   string `json:"type"`
			Format string `json:"format"`
			Items  struct {
				Format string `json:"format"`
			} `json:"items"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(t.InputSchema, &s); err != nil {
		return out
	}
	for name, p := range s.Properties {
		if (p.Type == "string" && p.Format == "image") || (p.Type == "array" && p.Items.Format == "image") {
			out[name] = true
		}
	}
	return out
}

// imageValueCount counts the non-empty images in an input value, which may be a
// single string or an array of strings.
func imageValueCount(v any) int {
	switch x := v.(type) {
	case string:
		if x != "" {
			return 1
		}
		return 0
	case []any:
		n := 0
		for _, e := range x {
			if s, ok := e.(string); ok && s != "" {
				n++
			}
		}
		return n
	default:
		return 0
	}
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
