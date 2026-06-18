package tasks

import (
	"encoding/json"
	"testing"
)

// TestValidateOutputCoercion covers the response-contract coercion: the model
// answers in plain text and ValidateOutput turns it into the schema's declared
// type, applying constraints (enum, pattern, range), or errors so the
// prediction is flagged invalid.
func TestValidateOutputCoercion(t *testing.T) {
	cases := []struct {
		name    string
		schema  string
		raw     string
		want    string // expected coerced JSON (when ok)
		wantErr bool
	}{
		{"no schema → raw passthrough", "", "anything", "", false},

		{"string: plain text", `{"type":"string"}`, "positive", `"positive"`, false},
		{"string: already JSON-quoted", `{"type":"string"}`, `"positive"`, `"positive"`, false},
		{"string: code-fenced", "{\"type\":\"string\"}", "```\nhello\n```", `"hello"`, false},
		{"string enum: allowed", `{"type":"string","enum":["a","b"]}`, "b", `"b"`, false},
		{"string enum: rejected", `{"type":"string","enum":["a","b"]}`, "c", "", true},
		{"string pattern: match", `{"type":"string","pattern":"^[A-Z]{3}$"}`, "ABC", `"ABC"`, false},
		{"string pattern: no match", `{"type":"string","pattern":"^[A-Z]{3}$"}`, "abc", "", true},

		{"number: bare", `{"type":"number"}`, "3.14", "3.14", false},
		{"number: quoted", `{"type":"number"}`, `"42"`, "42", false},
		{"number: not numeric", `{"type":"number"}`, "lots", "", true},
		{"number range: below min", `{"type":"number","minimum":10}`, "5", "", true},
		{"number range: in range", `{"type":"number","minimum":0,"maximum":100}`, "50", "50", false},

		{"integer: ok", `{"type":"integer"}`, "42", "42", false},
		{"integer: rejects float", `{"type":"integer"}`, "3.14", "", true},

		{"boolean: true", `{"type":"boolean"}`, "true", "true", false},
		{"boolean: mixed case", `{"type":"boolean"}`, "True", "true", false},
		{"boolean: garbage", `{"type":"boolean"}`, "maybe", "", true},

		{"object: passthrough", `{"type":"object","properties":{"label":{"type":"string"}}}`, `{"label":"x"}`, `{"label":"x"}`, false},
		{"object: wrong shape", `{"type":"object","required":["label"],"properties":{"label":{"type":"string"}}}`, `{"other":1}`, "", true},

		{"array of strings: ok", `{"type":"array","items":{"type":"string"}}`, `["a","b"]`, `["a","b"]`, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := &Task{}
			if tc.schema != "" {
				task.OutputSchema = json.RawMessage(tc.schema)
			}
			got, err := ValidateOutput(task, tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got output %q", string(got))
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.schema == "" {
				if got != nil {
					t.Fatalf("no-schema case should return nil output, got %q", string(got))
				}
				return
			}
			if string(got) != tc.want {
				t.Fatalf("coerced output = %q, want %q", string(got), tc.want)
			}
		})
	}
}
