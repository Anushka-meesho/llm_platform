package tasks

import (
	"encoding/json"
	"strings"
	"testing"
)

// Image inputs must gate the template (presence) without their raw base64 bytes
// ever landing in the rendered prompt — images travel to the model as
// attachments, and inlining them would bloat the prompt and blow the input-size
// estimate (the sscat-prediction failure). This covers both a typed image field
// (format:"image", any name) and the implicit "image"/"images" names.
func TestRenderPromptDoesNotInlineImageBytes(t *testing.T) {
	const dataURL = "data:image/png;base64,VERYLONGBASE64PAYLOAD=="

	cases := []struct {
		name     string
		schema   string
		template string
		inputs   string
	}{
		{
			name:     "typed image field with a custom name",
			schema:   `{"type":"object","properties":{"Image":{"type":"array","items":{"type":"string","format":"image"},"maxItems":1}}}`,
			template: "{{if .Image}}- Image: {{.Image}}{{end}}",
			inputs:   `{"Image":["` + dataURL + `"]}`,
		},
		{
			name:     "implicit images array, no schema",
			template: "{{if .images}}has {{.images}} image(s){{end}}",
			inputs:   `{"images":["` + dataURL + `"]}`,
		},
		{
			name:     "implicit single image, no schema",
			template: "{{if .image}}img={{.image}}{{end}}",
			inputs:   `{"image":"` + dataURL + `"}`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			task := &Task{ID: "t", Name: "t", Model: "m", PromptTemplate: c.template}
			if c.schema != "" {
				task.InputSchema = json.RawMessage(c.schema)
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(c.inputs), &m); err != nil {
				t.Fatalf("inputs: %v", err)
			}
			out, err := RenderPrompt(task, m)
			if err != nil {
				t.Fatalf("RenderPrompt: %v", err)
			}
			if strings.Contains(out, dataURL) {
				t.Errorf("rendered prompt inlined the image data URL: %q", out)
			}
			// The {{if}} guard must still fire (image present), so the block renders.
			if strings.TrimSpace(out) == "" {
				t.Errorf("image-present guard did not render anything: %q", out)
			}
		})
	}
}

// An image field that is declared/absent must keep its {{if}} guard falsy and
// never error, even with missingkey=error.
func TestRenderPromptImageAbsentGuardFalsy(t *testing.T) {
	task := &Task{
		ID: "t", Name: "t", Model: "m",
		PromptTemplate: "start{{if .Image}} IMG{{end}} end",
		InputSchema:    json.RawMessage(`{"type":"object","properties":{"Image":{"type":"array","items":{"type":"string","format":"image"}}}}`),
	}
	out, err := RenderPrompt(task, map[string]any{})
	if err != nil {
		t.Fatalf("RenderPrompt: %v", err)
	}
	if strings.Contains(out, "IMG") {
		t.Errorf("absent image guard rendered as present: %q", out)
	}
}
