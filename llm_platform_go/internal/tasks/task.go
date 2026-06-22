// Package tasks implements the Task registry — the platform's core abstraction.
// A Task is a named configuration (input/output schemas, prompt template, model
// preference) that defines one prediction use case. Tasks are the unit of cost
// tracking, versioning, and (in later phases) RBAC, budgets, and eval.
package tasks

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"llm_platform_go/internal/llm"
)

// PlaygroundTaskID is the built-in task that the multi-model Compare UI runs
// under, so playground usage is cost-attributed like everything else.
const PlaygroundTaskID = "playground"

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,63}$`)

// Task is one registered prediction use case.
type Task struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	Description    string          `json:"description,omitempty"`
	InputSchema    json.RawMessage `json:"input_schema,omitempty"`  // JSON Schema; nil = free-form
	OutputSchema   json.RawMessage `json:"output_schema,omitempty"` // JSON Schema; nil = raw text output
	PromptTemplate string          `json:"prompt_template"`
	SystemPrompt   string          `json:"system_prompt,omitempty"`
	PromptVersion  int             `json:"prompt_version"`
	Model          string          `json:"model"`
	FallbackModels []string        `json:"fallback_models,omitempty"` // stored now, executed in Phase 1
	Temperature    float64         `json:"temperature"`
	MaxTokens      int             `json:"max_tokens"`
	DailyBudgetUSD float64         `json:"daily_budget_usd,omitempty"` // stored now, enforced in Phase 1
	// Prediction cache (design doc §3.3): identical predictions (same prompt
	// version, rendered prompt, model, params, schema) served without a
	// provider call. Off by default; TTL 0 = backend default (24h).
	CacheEnabled    bool `json:"cache_enabled"`
	CacheTTLSeconds int  `json:"cache_ttl_seconds,omitempty"`
	// Per-task input size limits (UI-configurable guardrails, independent of the
	// global rate limiter). Each is a hard ceiling enforced on production
	// predicts; 0 means "no limit". Text and image limits are set separately.
	MaxPromptChars int `json:"max_prompt_chars,omitempty"` // max characters of the text (system + user) sent to the model
	MaxImageKB     int `json:"max_image_kb,omitempty"`     // max size of each uploaded image, in KB
	MaxImages      int `json:"max_images,omitempty"`       // max number of images per prediction
	Active         bool            `json:"active"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

// Validate checks structural correctness of a task config: required fields,
// known model routing keys, and compilable JSON Schemas.
func (t *Task) Validate() error {
	if !slugRe.MatchString(t.ID) {
		return fmt.Errorf("id must be a slug (lowercase letters, digits, hyphens, 2-64 chars), got %q", t.ID)
	}
	if t.Name == "" {
		return errors.New("name is required")
	}
	if t.PromptTemplate == "" {
		return errors.New("prompt_template is required")
	}
	if t.Model == "" {
		return errors.New("model is required")
	}
	if !llm.KnownModel(t.Model) {
		return fmt.Errorf("unknown model %q", t.Model)
	}
	for _, m := range t.FallbackModels {
		if !llm.KnownModel(m) {
			return fmt.Errorf("unknown fallback model %q", m)
		}
	}
	if t.Temperature < 0 || t.Temperature > 2 {
		return fmt.Errorf("temperature must be in [0,2], got %v", t.Temperature)
	}
	if t.MaxTokens <= 0 {
		return errors.New("max_tokens must be positive")
	}
	if t.CacheTTLSeconds < 0 {
		return errors.New("cache_ttl_seconds must not be negative")
	}
	if t.MaxPromptChars < 0 {
		return errors.New("max_prompt_chars must not be negative")
	}
	if t.MaxImageKB < 0 {
		return errors.New("max_image_kb must not be negative")
	}
	if t.MaxImages < 0 {
		return errors.New("max_images must not be negative")
	}
	if len(t.InputSchema) > 0 {
		if _, err := compileSchema(t.InputSchema); err != nil {
			return fmt.Errorf("input_schema: %w", err)
		}
	}
	if len(t.OutputSchema) > 0 {
		if _, err := compileSchema(t.OutputSchema); err != nil {
			return fmt.Errorf("output_schema: %w", err)
		}
	}
	if _, err := parseTemplate(t.PromptTemplate); err != nil {
		return fmt.Errorf("prompt_template: %w", err)
	}
	return nil
}

// applyDefaults fills zero values with sensible defaults before persisting.
func (t *Task) applyDefaults() {
	if t.Temperature == 0 {
		t.Temperature = 0.2 // predictions want determinism, not creativity
	}
	if t.MaxTokens == 0 {
		t.MaxTokens = 1000
	}
	if t.PromptVersion == 0 {
		t.PromptVersion = 1
	}
	if t.FallbackModels == nil {
		t.FallbackModels = []string{}
	}
}
