# Tasks

Source: [llm_platform_go/internal/tasks/task.go](../llm_platform_go/internal/tasks/task.go) (and `validate.go`, `render.go`, `store.go` siblings)

A **Task** is the central configuration unit. It captures everything needed to run a prediction: the contract (input/output schemas), the prompt, the model chain, and the cost controls.

## Task struct

```go
type Task struct {
    ID              string          // routing key: slug [a-z0-9][a-z0-9-]{1,63}
    Name            string          // human-readable display name
    Description     string
    InputSchema     json.RawMessage // JSON Schema; nil = accept any inputs
    OutputSchema    json.RawMessage // JSON Schema; nil = return raw text
    PromptTemplate  string          // Go text/template
    SystemPrompt    string
    PromptVersion   int             // increments on each deploy
    Model           string          // primary model (registry key)
    FallbackModels  []string        // tried in order if primary fails
    Temperature     float64         // [0, 2]; lower = more deterministic
    MaxTokens       int             // max output tokens
    DailyBudgetUSD  float64         // reject predictions once daily spend exceeds this
    CacheEnabled    bool            // opt-in to prediction cache
    CacheTTLSeconds int             // 0 = use backend default (24h)
    Active          bool
    CreatedAt       time.Time
    UpdatedAt       time.Time
}
```

## Validation rules

`Task.Validate()` is called on every create and update. Failures produce a descriptive error before the task enters the registry.

| Field | Rule |
|-------|------|
| `ID` | Must match `^[a-z0-9][a-z0-9-]{1,63}$` |
| `Name` | Non-empty |
| `PromptTemplate` | Non-empty, must parse as valid Go text/template |
| `Model` | Non-empty, must be a known key in the model registry (`KnownModel`) |
| `FallbackModels` | Each entry must be a known registry key |
| `Temperature` | Must be in [0.0, 2.0] |
| `MaxTokens` | Must be > 0 |
| `CacheTTLSeconds` | Must be >= 0 |
| `InputSchema` | If set, must compile as a valid JSON Schema |
| `OutputSchema` | If set, must compile as a valid JSON Schema |

## JSON Schema validation

The platform uses `github.com/santhosh-tekuri/jsonschema/v6` (JSON Schema draft 6).

**Input validation** (`tasks.ValidateInput`):
- Called before prompt rendering with the raw request inputs.
- Returns field-level errors (path + message) on failure.
- If `InputSchema` is nil, any inputs are accepted.

**Output validation** (`tasks.ValidateOutput`):
- Called after the model responds with the raw text.
- First strips code fences if present — models often wrap JSON in ` ```json ``` ` or ` ``` `.
- Parses the stripped text as JSON and validates against `OutputSchema`.
- Returns the parsed JSON on success, an error on failure.
- If `OutputSchema` is nil, returns the raw text unchanged.
- A schema validation failure during the fallback walk causes `RecordFailure` on the health gate and advances the chain to the next model.

**Schema compilation cache:** Schemas are compiled once and cached by SHA-256 of the raw schema bytes. When a task's schema is updated (and thus the bytes change), the cache entry is naturally invalidated.

## Go prompt templates

Task prompts use Go's `text/template` language.

**Basic usage:**
```
Classify this {{.category}} text: {{.content}}
```

**Conditional fields:**
```
Summarize the following article.
{{if .constraints}}Requirements: {{.constraints}}{{end}}
```

**Rendering rules:**
- The template is compiled with `missingkey=error` — referencing a key that isn't in the inputs struct is a hard error. This catches typos in template variable names at development time, not in production.
- All properties declared in `InputSchema` are pre-seeded with `""` before rendering, so `{{if .optionalField}}` guards work without the caller supplying every field.
- Keys that are completely undeclared (not in the schema) still trigger `missingkey=error`.
- Template compilation is cached by SHA-256 of the template text.

**Compilation errors** (bad Go template syntax) return HTTP 400 to the caller. **Rendering errors** (missing required key) also return 400.

## Versioning

Every task has a `PromptVersion` integer. It starts at 1 and increments every time the prompt template, system prompt, or primary model is deployed (saved to production).

**Why this matters:**
- The prediction cache key includes `PromptVersion`. When a new version is deployed, old cache entries are never matched — the key simply changes, so stale cached responses from the previous prompt are never served.
- The `prompt_versions` table stores a full history row for every version with the template text, system prompt, deploying user, and a note. You can see exactly what changed between versions.
- Studio test runs can target a specific version (override mechanism), letting you test a draft without affecting the live `PromptVersion`.

**Version increment trigger:** Saving a draft does NOT increment the version. The version only increments when the task is explicitly deployed (activated). This requires `PermTaskDeploy`, which is separate from `PermTaskWrite` — enabling a two-person review workflow where a creator writes and an approver deploys.

## Example task config (YAML)

```yaml
id: article-classifier
name: Article Classifier
description: Classify a news article into one of three categories
input_schema:
  type: object
  required: [title, body]
  properties:
    title:
      type: string
    body:
      type: string
    hints:
      type: string
output_schema:
  type: object
  required: [category, confidence]
  properties:
    category:
      type: string
      enum: [sports, tech, business]
    confidence:
      type: number
      minimum: 0
      maximum: 1
prompt_template: |
  Classify the following article.
  {{if .hints}}Hints: {{.hints}}{{end}}

  Title: {{.title}}
  Body: {{.body}}

  Respond with JSON.
model: gpt-4o-mini
fallback_models: [gemini-2.5-flash, llama-groq]
temperature: 0.1
max_tokens: 200
cache_enabled: true
cache_ttl_seconds: 86400
daily_budget_usd: 5.00
```
