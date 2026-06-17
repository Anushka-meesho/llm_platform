// Package cache implements the semantic prediction cache (design doc §3.3 /
// roadmap Phase 3.3): identical predictions are served from a store instead of
// re-calling the provider.
//
// "Identical" is strict by construction — the key is a SHA-256 over every
// input that determines a model's output:
//
//   - task id + deployed prompt version (a deploy predictably invalidates,
//     even if the rendered text happens to match an older version)
//   - the fully rendered prompt (which already encodes the template AND every
//     caller input / context value rendered into it)
//   - the system prompt
//   - the model routing key (per model: each model in a task's fallback chain
//     caches under its own key, consulted as the fallback walk reaches that
//     model — never as a pre-call shortcut past a higher-priority model)
//   - temperature and max_tokens (sampling parameters change outputs)
//   - the output schema (it determines how the response is parsed/validated)
//
// If any of these differ, the key differs and the entry simply doesn't match.
// Invalidation therefore needs no explicit purging: prompt deploys, model or
// parameter changes, and schema edits all produce new keys, and stale entries
// age out via TTL.
package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// Cache is the storage seam: Redis in production, memory for dev/tests.
// Implementations must treat errors as misses — caching is an optimization
// and must never fail a prediction.
type Cache interface {
	// Get returns the stored value and whether it was present.
	Get(ctx context.Context, key string) ([]byte, bool)
	// Set stores val under key for ttl.
	Set(ctx context.Context, key string, val []byte, ttl time.Duration)
}

// KeyInputs is everything that must be identical for two predictions to share
// a cache entry. The cache is keyed per model — the single routing key that
// actually produced the answer — never on the fallback chain. The chain is
// live configuration (read fresh from tasks.Store on every prediction), so a
// routing edit changes which model runs, not which cache entry is consulted.
type KeyInputs struct {
	TaskID         string  `json:"task_id"`
	PromptVersion  int     `json:"prompt_version"`
	Model          string  `json:"model"` // the routing key that produced the answer
	SystemPrompt   string  `json:"system_prompt"`
	RenderedPrompt string  `json:"rendered_prompt"` // template + all inputs/context
	Temperature    float64 `json:"temperature"`
	MaxTokens      int     `json:"max_tokens"`
	OutputSchema   string  `json:"output_schema"`  // raw schema JSON; "" when absent
	Image          string  `json:"image,omitempty"` // multimodal input (data URL); "" for text-only tasks
}

// Key returns the deterministic cache key for one prediction. Field order is
// fixed by the struct, so equal inputs always hash equally.
func Key(in KeyInputs) string {
	b, _ := json.Marshal(in)
	sum := sha256.Sum256(b)
	return "predict:" + hex.EncodeToString(sum[:])
}

// Entry is the cached outcome of one successful prediction. Usage numbers are
// the original call's, kept for observability; the serving path reports zero
// cost because a hit consumes no provider tokens.
type Entry struct {
	Model        string          `json:"model"`
	Provider     string          `json:"provider"`
	RawResponse  string          `json:"raw_response"`
	Output       json.RawMessage `json:"output,omitempty"`       // parsed JSON when schema-validated
	OutputValid  *bool           `json:"output_valid,omitempty"` // nil when no output schema
	InputTokens  int             `json:"input_tokens"`
	OutputTokens int             `json:"output_tokens"`
	TotalTokens  int             `json:"total_tokens"`
	CachedAt     time.Time       `json:"cached_at"`
}

// DefaultTTL applies when a task enables caching without an explicit TTL —
// kept long, since "model X said Y for prompt P" is a stable fact.
const DefaultTTL = 24 * time.Hour
