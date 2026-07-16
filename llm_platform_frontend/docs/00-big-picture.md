# The Big Picture

## What this system does

The LLM Platform Go backend is a unified API for running LLM predictions across multiple providers (Groq, OpenAI, Gemini, Anthropic — the latter three through Meesho's internal bifrost gateway). Instead of each team writing their own OpenAI calls, prompt strings, and retry logic, teams author a **Task**: a named use-case with a typed input/output schema, a versioned prompt template, a primary model, and a fallback chain.

Core capabilities:

- **Task registry** — named prediction use-cases with JSON Schema in/out contracts, versioned prompts, and per-task model routing chains.
- **Fallback walk** — if the primary model fails or is unhealthy, the platform tries the next model automatically; the caller never sees a provider error unless the entire chain exhausts.
- **Per-task/model health tracker** — unhealthy model-task pairs are skipped, and recovery is probed lazily after cooldown.
- **Prediction cache** — semantically identical predictions (same task, prompt version, rendered prompt, model, params, schema) are served from cache; zero provider cost.
- **Cost tracking** — every prediction records input/output tokens and USD cost; daily budget gates are enforced per task.
- **RBAC seam** — the current backend ships admin permissions with JWT tokens issued as HttpOnly session cookies; creator/approver/caller/viewer are the planned role split.
- **Async observability** — DB writes happen off the hot path through buffered channel writers; dropped rows are counted but never fail a prediction.

## System topology

```
React Frontend
     │
     │  REST (JSON)
     ▼
Go Backend  (:8000)
     │
     ├─ /v1/tasks/{id}/predict
     │       │
     │       ├─ request + task schema validation
     │       ├─ rate limiter + daily budget gate
     │       └─ fallback walk
     │               ├─ per-model cache lookup (Redis / in-memory)
     │               ├─ per-task/model health gate
     │               ├─ Groq API  (Bearer token, direct)
     │               └─ Meesho bifrost gateway  (x-bf-vk)
     │                       ├─ OpenAI GPT-4o / GPT-4o-mini
     │                       ├─ Gemini 2.5 Pro / Flash
     │                       └─ Anthropic Claude
     │
     └─ SQLite (WAL mode)
             ├─ runs          (prediction history, tokens, cost)
             ├─ tasks         (registry)
             ├─ prompt_versions
             ├─ gateway_attempts
             ├─ eval_datasets / eval_examples / eval_runs
             ├─ feedback
             ├─ shadow_reports
             └─ model_health_events
```

## Why Go?

- **Goroutines are cheap.** The async DB writers (RunWriter, GatewayAttemptWriter, HealthEventWriter) are goroutines that drain buffered channels. Under Go's M:N scheduler this costs almost nothing; the same pattern in a thread-per-goroutine language would be expensive.
- **Single binary.** `go build` produces one statically-linked executable with no runtime dependency (no JVM, no interpreter). Deploy by copying a file.
- **Standard library covers the stack.** `net/http`, `text/template`, `encoding/json`, `crypto/sha256` — no framework needed for this surface area.
- **Concurrency primitives match the problem.** Channels for the writers and `sync.RWMutex` for task cache / health state keep the implementation direct and testable.

## Why SQLite?

- **No ops.** There is no database server to deploy, monitor, or patch. The DB is a file next to the binary.
- **WAL mode enables the access pattern.** Write-Ahead Logging lets multiple readers proceed concurrently with a single writer. The platform pairs this with `MaxOpenConns(1)` — one writer goroutine, unlimited readers — which fits perfectly.
- **Migrations are in-process.** `CREATE TABLE IF NOT EXISTS` and guarded `ALTER TABLE` statements run at boot; no external migration tool needed.
- **Swappable.** All DB access goes through the `Store` interface. Swapping to Postgres means writing a new `Store` implementation and changing one line in `main.go`; calling code is untouched.

## Key vocabulary

| Term | Meaning |
|------|---------|
| **Task** | Named prediction use-case: schema, prompt template, model chain, cost budget |
| **Run** | One execution of a task: inputs → model call → output, recorded in DB |
| **Session** | A sequence of runs from one user in one sitting |
| **Provider** | An LLM backend (openai, groq, gemini, anthropic) |
| **Fallback chain** | Ordered list of models tried in sequence on failure |
| **Health tracker** | Per-task/per-model state machine that skips unhealthy model-task pairs |
| **Prompt version** | Integer that increments every time a task's prompt is deployed |
| **Cache hit** | A prediction served from cache; zero provider cost, zero tokens consumed |
