package tasks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// yamlTask is the on-disk task contract (the "plug-and-play YAML" a team
// submits to onboard). Schemas are written as plain YAML maps and converted
// to JSON for storage.
type yamlTask struct {
	ID             string         `yaml:"id"`
	Name           string         `yaml:"name"`
	Description    string         `yaml:"description"`
	InputSchema    map[string]any `yaml:"input_schema"`
	OutputSchema   map[string]any `yaml:"output_schema"`
	PromptTemplate string         `yaml:"prompt_template"`
	SystemPrompt   string         `yaml:"system_prompt"`
	Model          string         `yaml:"model"`
	FallbackModels []string       `yaml:"fallback_models"`
	Temperature    float64        `yaml:"temperature"`
	MaxTokens      int            `yaml:"max_tokens"`
	DailyBudgetUSD float64        `yaml:"daily_budget_usd"`
	Cache          *yamlCache     `yaml:"cache"`
}

// yamlCache is the per-task cache block of the YAML contract:
//
//	cache:
//	  enabled: true
//	  ttl: 24h   # optional; backend default when omitted
type yamlCache struct {
	Enabled bool   `yaml:"enabled"`
	TTL     string `yaml:"ttl"`
}

func (y *yamlTask) toTask() (*Task, error) {
	t := &Task{
		ID:             y.ID,
		Name:           y.Name,
		Description:    y.Description,
		PromptTemplate: y.PromptTemplate,
		SystemPrompt:   y.SystemPrompt,
		Model:          y.Model,
		FallbackModels: y.FallbackModels,
		Temperature:    y.Temperature,
		MaxTokens:      y.MaxTokens,
		DailyBudgetUSD: y.DailyBudgetUSD,
	}
	if y.InputSchema != nil {
		b, err := json.Marshal(y.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("input_schema: %w", err)
		}
		t.InputSchema = b
	}
	if y.OutputSchema != nil {
		b, err := json.Marshal(y.OutputSchema)
		if err != nil {
			return nil, fmt.Errorf("output_schema: %w", err)
		}
		t.OutputSchema = b
	}
	if y.Cache != nil {
		t.CacheEnabled = y.Cache.Enabled
		if y.Cache.TTL != "" {
			d, err := time.ParseDuration(y.Cache.TTL)
			if err != nil {
				return nil, fmt.Errorf("cache.ttl: %w", err)
			}
			t.CacheTTLSeconds = int(d.Seconds())
		}
	}
	return t, nil
}

// LoadYAMLDir upserts every *.yaml/*.yml task config in dir. The files are the
// source of truth for seeded tasks: re-running on a changed file updates the
// stored task (bumping prompt_version if the prompt changed). A missing dir is
// not an error — it just means no seeded tasks.
func LoadYAMLDir(store *Store, dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read tasks dir %q: %w", dir, err)
	}

	count := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || (!strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml")) {
			continue
		}
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return count, fmt.Errorf("read %s: %w", path, err)
		}
		var y yamlTask
		if err := yaml.Unmarshal(data, &y); err != nil {
			return count, fmt.Errorf("parse %s: %w", path, err)
		}
		t, err := y.toTask()
		if err != nil {
			return count, fmt.Errorf("convert %s: %w", path, err)
		}
		if err := store.Upsert(t); err != nil {
			return count, fmt.Errorf("upsert %s (task %q): %w", path, t.ID, err)
		}
		count++
	}
	return count, nil
}

// SeedPlayground registers the built-in playground task (free-form prompt, no
// schemas) that the Compare UI's /run endpoint attributes its usage to.
// Idempotent: an existing playground task is left untouched so operators can
// tweak it (e.g. budget) without the seed reverting their changes.
func SeedPlayground(store *Store) error {
	if _, err := store.Get(PlaygroundTaskID); err == nil {
		return nil
	} else if err != ErrNotFound {
		return err
	}
	return store.Create(&Task{
		ID:             PlaygroundTaskID,
		Name:           "Playground",
		Description:    "Built-in multi-model comparison playground (the Compare UI). Free-form prompts, no schemas.",
		PromptTemplate: "{{.prompt}}", // not used by /run, present to satisfy validation
		Model:          "gpt-4o-mini",
		Temperature:    0.7,
		MaxTokens:      1000,
	})
}
