package schema

import (
	"strings"
	"testing"
)

// expectedSchemas is the set of request contracts the router relies on. If a
// route gains or loses a body, update this list — it pins coverage so a missing
// or stray schema file is caught here rather than in production.
var expectedSchemas = []string{
	"auth_login",
	"create_prism_eval_dataset",
	"create_task",
	"delete_sessions",
	"deploy_version",
	"feedback",
	"model_health_reset",
	"predict",
	"run",
	"run_eval",
	"save_draft_version",
	"shadow_compare",
	"test_task",
	"update_task",
}

func TestLoadRequests(t *testing.T) {
	reg, err := LoadRequests()
	if err != nil {
		t.Fatalf("LoadRequests: %v", err)
	}
	for _, name := range expectedSchemas {
		if !reg.Has(name) {
			t.Errorf("missing schema %q", name)
		}
	}
	if got, want := len(reg.Names()), len(expectedSchemas); got != want {
		t.Errorf("schema count: got %d (%v), want %d", got, reg.Names(), want)
	}
}

func TestValidate(t *testing.T) {
	reg := MustLoadRequests()

	cases := []struct {
		name    string
		schema  string
		body    string
		wantErr bool
	}{
		{"feedback ok", "feedback", `{"run_id":"r","model":"m","rating":4}`, false},
		{"feedback rating too high", "feedback", `{"run_id":"r","model":"m","rating":9}`, true},
		{"feedback rating wrong type", "feedback", `{"run_id":"r","model":"m","rating":"4"}`, true},
		{"feedback missing model", "feedback", `{"run_id":"r","rating":4}`, true},
		{"feedback unknown field", "feedback", `{"run_id":"r","model":"m","rating":4,"x":1}`, true},
		{"empty body", "feedback", ``, true},
		{"invalid json", "feedback", `{`, true},
		{"create_task missing model", "create_task", `{"id":"t","name":"T","prompt_template":"p"}`, true},
		{"create_task bad id", "create_task", `{"id":"BAD","name":"T","prompt_template":"p","model":"m"}`, true},
		{"create_task ok", "create_task", `{"id":"my-task","name":"T","prompt_template":"p {{.x}}","model":"m"}`, false},
		{"deploy version zero", "deploy_version", `{"version":0}`, true},
		{"deploy ok", "deploy_version", `{"version":2}`, false},
		{"predict needs object inputs", "predict", `{"inputs":"x"}`, true},
		{"predict ok", "predict", `{"inputs":{"a":1}}`, false},
		{"unknown schema", "nope", `{}`, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := reg.Validate(tc.schema, []byte(tc.body))
			if tc.wantErr && err == nil {
				t.Errorf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateRejectsUnknownSchema(t *testing.T) {
	reg := MustLoadRequests()
	err := reg.Validate("does-not-exist", []byte(`{}`))
	if err == nil || !strings.Contains(err.Error(), "no schema registered") {
		t.Errorf("got %v, want 'no schema registered' error", err)
	}
}
