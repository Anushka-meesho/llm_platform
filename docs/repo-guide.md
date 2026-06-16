# LLM Platform — Complete Repository Guide

**Last updated:** 2026-06-16
**Scope:** `llm_platform_go` (backend) + `llm_platform_frontend` (operator Studio UI) +
`llm_platform_client` (consumer-facing client portal). The Python `llm_platform_v0` is
superseded and excluded.

---

## 1. What This Is

The repo implements Meesho's internal **LLM Platform** — a "prediction factory": callers
register a **Task** (input/output schemas + prompt template + model preference), then make
one HTTP call (`POST /v1/tasks/{id}/predict`) and get back a schema-validated, structured
prediction with full cost/token/latency attribution. The platform owns prompt execution,
model routing, output validation, cost tracking, and feedback ingestion. It explicitly
does **not** own caller business logic, orchestration, or data preprocessing.

**Architecture decision (final): pure Go, single binary.** No LiteLLM, no Langfuse, no
Python in the hot path. All routing, prompt management, tracing, and eval are built here.

**Current state — Phases 0 + 1 complete:**
- Task registry (YAML + API), task-keyed prediction endpoint with schema enforcement
- **Resilient routing:** fallback chains + per-provider circuit breaker, `X-Platform-Degraded` contract
- **Budget enforcement:** per-task daily caps → 429 + `Retry-After` (0 = exempt)
- **Service auth + RBAC:** `cmd/issue-token` mints long-lived `svc:*` Bearer tokens (CIS-ready) with a role; role-based authorization enforced at the gateway (creator / approver / caller / viewer / admin — see §3.3)
- **Prompt registry:** first-class versions (draft → test → deploy), auto-history, Studio UI (Tasks page)
- **Shadow harness:** `/v1/shadow/compare` measures field-level match rate + latency p50/p95 vs labelled expectations
- **Observability:** async buffered run writer (hot path never blocks on SQLite), per-task stats endpoint
- **Prediction cache (Redis):** per-task opt-in exact-match cache — key pins prompt version + rendered prompt + model + params + schema; hits are zero-cost `cached:true` responses (pulled forward from Phase 3.3)
- Demo SSO + swappable user store, JWT cookie auth
- Multi-model playground (Compare UI), pre-call cost estimation, 1–5★ feedback
- **Per-session model leaderboard:** `GET /sessions/{id}/leaderboard` averages the manual
  1–5★ ratings per model within a Compare session (Leaderboard modal) — the manual
  precursor to the automated eval layer
- Per-task + per-model + daily cost dashboard
- **Client portal** (`llm_platform_client`, :5174): consumer-facing catalog + live Try-it
  predict panel, authenticated as a baked-in `svc:demo-client` service token (no login)

Planning docs: `docs/gap-analysis-roadmap.md` (gap analysis vs. the design doc),
`docs/phase-workflow.md` (execution plan for Phases 1–4),
`docs/deployment-guide.md` (every dev/demo assumption to swap before a real
deployment, mapped to the seam that contains it).

---

## 2. Repo Layout

```
llm_platform/
├── dev.sh / dev.yaml              # local dev orchestrator: runs all 3 servers detached
│                                  #   (./dev.sh restart|start|stop|status|logs); pids+logs in .dev/
├── docs/                          # planning + this guide
├── llm_platform_go/               # Go backend (single binary)
│   ├── cmd/server/main.go         # boot sequence
│   ├── cmd/issue-token/main.go    # mint long-lived svc:* Bearer tokens (CIS etc.)
│   ├── internal/
│   │   ├── api/                   # HTTP layer: router, middleware, handlers
│   │   ├── auth/                  # JWT issue/parse, cookie management
│   │   ├── cache/                 # prediction cache: Redis / memory behind Cache iface
│   │   ├── config/                # env-driven configuration
│   │   ├── db/                    # SQLite open/migrate + all SQL queries
│   │   ├── llm/                   # provider clients, model routing, pricing
│   │   ├── tasks/                 # Task registry: model, store, validate, render, seed
│   │   ├── types/                 # request/response contracts + RunRow
│   │   └── users/                 # identity seam: Store interface + DemoStore
│   ├── tasks.d/                   # YAML task configs, seeded at startup
│   ├── tests/                     # black-box HTTP + DB tests
│   ├── pricing.json               # per-model $/1M token rates
│   └── .env                       # local secrets (gitignored)
├── llm_platform_frontend/         # React 19 + Vite + Tailwind 4 + Meesho merlin-ui
│   └── src/
│       ├── api/client.ts          # typed fetch wrapper (cookie credentials)
│       ├── auth/                  # AuthContext provider + useAuth hook
│       ├── components/            # AppShell, LoginScreen, Sidebar, ChatArea, ModelColumn,
│       │                          #   ChatInput, MessageBubble, StarRating, LeaderboardModal,
│       │                          #   SchemaEditor, VersionHistory, SystemPromptBar
│       ├── hooks/                 # useChat (Compare state), useSessions
│       ├── pages/                 # ComparePage, TasksPage, VersionsPage, EstimatePage, DashboardPage
│       ├── types/index.ts         # API contract mirror
│       └── utils/tokens.ts        # js-tiktoken counting + pricing-fed cost estimation
├── llm_platform_client/           # Client portal (:5174) — consumer-facing, /v1 API only
│   └── src/                       # task catalog, Try-it predict panel, usage, snippets;
│                                  # no login: baked-in svc:demo-client Bearer JWT
│                                  # (src/auth/token.ts, override via VITE_API_TOKEN)
└── llm_platform_v0/               # superseded Python prototype — do not extend
```

---

## 3. Backend Deep Dive

### 3.1 Boot sequence — `cmd/server/main.go`

1. `godotenv.Load()` — `.env` is optional; real env vars also work.
2. `config.Load()` — fails only if **zero** provider keys are set; missing individual
   keys log a warning and that provider fails at call time.
3. `llm.LoadPricing(pricing.json)` — cost table into memory.
4. `db.Open` (SQLite, WAL, single writer) → `db.Migrate` (idempotent).
5. `llm.BuildClients` — one `Provider` per backend (OpenAI/Groq/Gemini/Anthropic).
6. `llm.StartRecoveryProber(ctx, clients, 15s)` — enables probe-only breakers and
   background health-checks unhealthy providers (see §3.6).
7. `tasks.NewStore` → `tasks.SeedPlayground` → `tasks.LoadYAMLDir(TASKS_DIR)` — upserts
   every `tasks.d/*.yaml`; a changed prompt bumps that task's `prompt_version`.
8. `users.NewDemoStore()` — **the identity swap seam** (see §3.4).
9. `db.NewRunWriter` — async observability writer; `Close()` flushes on shutdown.
10. Prediction cache by `CACHE_BACKEND`: Redis (boot fails on bad addr) / in-process
    memory / off.
11. `api.NewRouter(RouterDeps{...})` → `http.ListenAndServe(:PORT)`.

### 3.2 Configuration — `internal/config`

| Env var | Default | Purpose |
|---|---|---|
| `OPENAI_API_KEY` / `GROQ_API_KEY` / `GEMINI_API_KEY` / `ANTHROPIC_API_KEY` | — | Provider keys; at least one of these or `MEESHO_GATEWAY_VK` required |
| `OPENAI_BASE_URL` / `GROQ_BASE_URL` / `GEMINI_BASE_URL` / `ANTHROPIC_BASE_URL` | public APIs | Provider base URLs — override for proxies/gateways/self-hosted (Anthropic empty = SDK default) |
| `MEESHO_GATEWAY_VK` | — | Virtual key for the Meesho internal LLM gateway (sent as the `x-bf-vk` header); powers the `meesho-gemini-2.5-flash` model |
| `MEESHO_GATEWAY_BASE_URL` | `http://llm-gateway.prd.meesho.int/v1` | Meesho gateway base URL (OpenAI-compatible `/chat/completions`) |
| `DB_PATH` | `./llm_platform.db` | SQLite file |
| `PORT` | `8000` | HTTP port |
| `PRICING_PATH` | `./pricing.json` | Cost table |
| `TASKS_DIR` | `./tasks.d` | YAML task configs |
| `JWT_SECRET` | dev placeholder | Signs session tokens — **set a real one outside dev** |
| `AUTH_COOKIE_NAME` | `llm_platform_token` | Session cookie |
| `AUTH_ISSUER` | `llm-platform-demo` | JWT `iss` |
| `TOKEN_EXPIRY` | `12h` | Session lifetime |
| `COOKIE_DOMAIN` / `COOKIE_SECURE` | empty / false | Cookie scoping (set Secure under HTTPS) |
| `ALLOWED_ORIGINS` | `http://localhost:5173` | CORS allowlist (credentials mode) |
| `REDIS_ADDR` / `REDIS_PASSWORD` / `REDIS_DB` | — | Prediction cache backend; addr set → Redis (boot fails on bad addr) |
| `CACHE_BACKEND` | derived | `redis` \| `memory` (in-process, dev only) \| `off` (default when no `REDIS_ADDR`) |

### 3.3 Auth — `internal/auth`

HS256 JWT with claims `{sub, email, name, iss, iat, exp}`. Token is accepted from either
the `Authorization: Bearer` header **or** the HttpOnly session cookie (`TokenFromRequest`).
`RequireAuth` middleware (in `internal/api/middleware.go`) parses/validates and puts an
`auth.User{Subject, Email, Name}` on the request context; handlers read it via
`auth.FromContext` / the `requireUser` helper. Cookie helpers: `SetAuthCookie`,
`ClearAuthCookie`.

The Bearer path means **service-to-service auth already works mechanically** — what's
missing (Phase 1) is a way to mint long-lived service principals distinct from UI sessions.

**Role-based authorization (`internal/auth/rbac.go`).** The PFS User Journey separates a
prompt **creator** (authors/iterates), an **approver** (owns the publish gate — Gate 2),
and a service **caller** (only invokes predict). RBAC encodes that as six capabilities —
`task:read`, `task:predict`, `task:write` (create/update/draft/test/shadow), `task:deploy`
(the publish gate, deliberately split from `task:write`), `task:delete` (destructive, e.g.
pruning prompt versions — admin-only), and `task:view_prompt` (see the prompt text itself —
withheld from callers, who integrate against the task contract and "never touch prompts" per
the PFS) — mapped to five roles:

| Role | read | predict | write | deploy | delete | view_prompt |
|---|---|---|---|---|---|---|
| `admin` | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| `creator` | ✓ | ✓ | ✓ | — | — | ✓ |
| `approver` | ✓ | ✓ | — | ✓ | — | ✓ |
| `caller` | ✓ | ✓ | — | — | — | — |
| `viewer` | ✓ | — | — | — | — | ✓ |

**Prompt redaction:** a caller (anyone without `task:view_prompt`) gets task config, schemas,
and its own run outputs, but `GET /v1/tasks`, `GET /v1/tasks/{id}`, and
`GET /v1/tasks/{id}/versions` blank the `prompt_template` / `system_prompt` fields
(`redactedTask` copies the cached task so the shared config-cache entry is never mutated).

The role rides **inside the signed JWT** (`Claims.Role`), so the gateway authorizes from the
token alone — no per-request identity-store lookup (hot path stays DB-free). A token with no
role claim resolves to `caller` (`DefaultRole`) — least privilege that can still predict +
read, so pre-RBAC service tokens (e.g. the client portal's baked `svc:demo-client`) keep
working. `RequirePermission(perm)` middleware (`internal/api/middleware.go`) gates each `/v1`
route in `router.go` and returns **403** on denial; Studio playground routes (`/run`,
`/sessions`, `/feedback`, `/dashboard`, `/pricing`, `/auth/me`) stay open to any authenticated
user. Login stamps the role from `users.User.Role`; `cmd/issue-token -role` mints service
tokens with a role (validated against `KnownRole`). The demo store seeds one user per role.
Frontend mirrors the matrix in `src/auth/permissions.ts` (UI gating only — the gateway is the
source of truth) and the Studio hides/disables edit/deploy actions per role.

### 3.4 Identity seam — `internal/users`

```go
type Store interface {
    GetByID(ctx, id) (*User, error)  // ErrNotFound when absent
    List(ctx) ([]*User, error)       // demo-SSO login screen only
}
```

`DemoStore` is in-memory, seeded with one user per RBAC role (`u-admin`, `u-creator`,
`u-approver`, `u-viewer` — see §3.3), persists **nothing** (by design — there is no demo
data to migrate). `User.Role` is the field the login handler stamps into the session token.
**Moving to real SSO = implement `Store` against the IdP + change one constructor line in
`main.go`** (the IdP supplies the role). Nothing else in the codebase knows where users
come from.

### 3.5 Task registry — `internal/tasks` (the platform core)

**`Task`** (`task.go`): `ID` (slug), `Name`, `Description`, `InputSchema` /
`OutputSchema` (JSON Schema, both optional), `PromptTemplate`, `SystemPrompt`,
`PromptVersion`, `Model`, `FallbackModels` (executed by `CallWithFallback`), `Temperature`
(default 0.2), `MaxTokens` (default 1000), `DailyBudgetUSD` (enforced by the budget gate),
`CacheEnabled` / `CacheTTLSeconds` (per-task prediction-cache opt-in, default TTL 24h),
`Active`. `Validate()` checks slug shape, required fields, **known model routing keys**,
schema compilability, and template parsability — a bad config is rejected at write time,
not call time.

**`Store`** (`store.go` + `versions.go`): SQLite CRUD. `Get` is served from an
**in-memory config cache** (write-invalidated by Create/Update/Deploy, 5s TTL
for out-of-band convergence) so the prediction hot path never reads the DB for
task config; treat returned `*Task` as immutable. `Update` **auto-bumps
`prompt_version`** when `PromptTemplate` or `SystemPrompt` changed (next number =
`max(prompt_versions)+1` so it never collides with drafts) and appends a history row;
non-prompt updates don't. `Upsert` powers YAML seeding and **preserves the existing
`active` flag** (YAML doesn't model activation — regression-tested). Version methods:
`ListVersions` (active flagged), `GetVersion`, `SaveDraft` (records without
activating), `Deploy` (copies a version into the live config). All SQL is contained
here (Postgres move = this file + `internal/db`).

**Validation** (`validate.go`): `santhosh-tekuri/jsonschema/v6`, compiled schemas cached
by **content hash** (task edits naturally invalidate). `ValidateInput(task, rawJSON)`;
`ValidateOutput(task, modelText)` strips markdown code fences (`StripCodeFences`), parses,
validates, returns the cleaned JSON.

**Rendering** (`render.go`): Go `text/template` with `missingkey=error`. Fields declared
in the input schema but absent from the request are pre-filled with `""` — so
`{{if .description}}…{{end}}` optional-field guards work, while a template referencing an
**undeclared** key fails loudly. Parsed templates cached by content hash.

**Seeding** (`seed.go`): `LoadYAMLDir` upserts `*.yaml|*.yml`; `yamlTask` is the
plug-and-play onboarding contract (schemas written as YAML maps, converted to JSON).
`SeedPlayground` registers the built-in `playground` task once and never overwrites it.

**YAML contract example** — `tasks.d/attribute-extraction.yaml` (live, working):
input `{title*, description, category*}` → output `{attributes: {string: string},
confidence: 0..1}`, model `llama-groq`, fallback `[gpt-4o-mini]`, budget $50/day,
`cache: {enabled: true, ttl: 24h}`.

### 3.6 Model layer — `internal/llm`

**`Provider` interface** (`client.go`): `Call(ctx, *chatRequest) (*chatResponse, error)`
over the OpenAI-compatible chat completions wire format. `openAICompatProvider`
covers OpenAI, Groq, and Gemini (all expose compatible endpoints);
`NewOpenAICompatProvider(baseURL, apiKey)` is exported for vLLM/self-hosted/
test fakes. **`anthropicProvider`** (`anthropic.go`) is the native Messages-API
implementation via the official `anthropic-sdk-go` (SDK retries disabled — the
platform owns retry/breaker policy): system messages → `system`, temperature
never forwarded (current Anthropic models 400 on sampling params), thinking
blocks filtered from output, safety-classifier refusals surfaced as 400-class
`APIError` (definitive: no retry/breaker/fallback), SDK errors normalized to
`APIError{HTTPStatusCode, Message}` so classification is provider-uniform. The
provider is nil when `ANTHROPIC_API_KEY` is unset → standard "LLM client not
configured" per-call error.

**Routing registry** (`runner.go`): friendly key → concrete model ID + provider
attribution + client + flags. **20 models across 5 providers**, registered
whether or not the provider's key is configured (missing key = graceful
per-call error, never a boot failure):
- **OpenAI:** `gpt-5.1`, `gpt-5`, `gpt-5-mini`, `gpt-5-nano` (reasoning wire:
  `max_completion_tokens`, no temperature — `reasoning: true` flag), `gpt-4.1`,
  `gpt-4.1-mini`, `gpt-4.1-nano`, `gpt-4o`, `gpt-4o-mini`
- **Groq:** `llama-groq` (llama-3.3-70b-versatile)
- **Gemini:** `gemini-3-pro` (preview), `gemini-2.5-pro`, `gemini-2.5-flash`,
  `gemini-2.5-flash-lite`, `gemini-flash` (2.0)
- **Anthropic (native):** `claude-fable-5`, `claude-opus-4-8`,
  `claude-sonnet-4-6`, `claude-haiku-4-5`
- **Meesho gateway:** `meesho-gemini-2.5-flash` (model id `vertex/gemini-2.5-flash`)
  — OpenAI-compatible endpoint authenticated with the `x-bf-vk` virtual-key header
  instead of Bearer (`openAICompatProvider.authHeader`)

Helpers: `ProviderName(model)`, `KnownModel(model)`, `AllModels()` (sorted keys —
backs `/health` `models_available`; `DefaultModels` stays the small /run
fan-out default). A registry test enforces every entry has a pricing.json row
and vice versa.

**`CallModel(ctx, clients, model, messages, temp, maxTokens) ModelResult`** — the single
execution path used by both the playground fan-out and `/predict`. Retries up to 3× on
429/500/503 with linear backoff (2s, 4s), respects context cancellation, classifies
errors into human-readable strings (`classifyError`: timeouts, network, auth, rate-limit,
provider-down). Never panics; failures come back as `ModelResult{Success: false, Error}`.

**Circuit breaker** (`breaker.go`): per-provider state machine — closed → open after 3
consecutive *infra* failures (`isInfraFailure`: 429/5xx/network/timeout; 4xx config
errors and caller cancellation don't count) → half-open after 30s (one probe) → closed
on success. Process-global `defaultBreakers`; `ResetBreakers()` for tests; injectable
clock via `NewBreakerSetForTest`. Open circuit = instant errResult, no provider call.
**Probe-only mode** (`SetProbeOnly`, enabled in production by the prober): open
circuits never half-open for production traffic — recovery is owned entirely by
the background prober.

**Recovery prober** (`prober.go`, started in `main.go`, 15s interval): with
probe-only breakers, production requests never pay to discover a recovery —
a sick provider costs latency exactly once (the failures that tripped the
breaker), then every request fails fast (<1ms) down the fallback chain. The
prober sends a 1-token "ping" (5s timeout, no retries) to each provider whose
circuit isn't closed; a completed exchange closes the circuit, and since
`CallWithFallback` walks the chain from the front on every request, the very
next prediction returns to the highest-priority healthy model automatically.

**Fallback chain** (`fallback.go`): `CallWithFallback(models []string, …)` tries
primary then fallbacks, advancing **only on infra failures or open circuits** — a
content-level outcome (success or 4xx) returns immediately. Sets
`ModelResult.FallbackUsed` and `.Degraded` (drives the `X-Platform-Degraded` header).

**`RunAll`** — playground fan-out: goroutine per model, buffered channel, results in
arrival order (fastest first).

**Prediction cache** (`internal/cache`): per-task opt-in exact-match cache
(Redis in production, in-process memory for dev, off when unconfigured — `Cache`
interface is the seam). Every key is a SHA-256 over *what determined the output*:
task id, **deployed prompt version**, the **fully rendered prompt** (template +
every input/context value), system prompt, temperature, max_tokens, and output
schema — so deploys, param changes, and schema edits all invalidate implicitly.
The cache is **two-level**, both written on a clean predict:

1. **Chain-level** — key also pins the **whole fallback structure**
   (`[primary, ...fallbacks]`). Checked **first**: if the chain is identical to
   when it was cached, this hits directly. Short TTL (`cache.ChainTTL`, 60s) —
   it may hold a degraded (fallback-served) answer, so it re-evaluates often.
2. **Per-model** — key pins a single **model routing key**. Looked up only when
   the chain-level entry misses (the fallback structure changed, or it expired):
   the lookup walks the chain and serves the first model with a cached answer.
   Long TTL (the per-task setting or `DefaultTTL` 24h) — "model X said Y for
   prompt P" is stable regardless of chain composition.

Fill writes both: the chain entry (whatever model served — **including a
fallback**) and a per-model entry (under the serving model). A cached answer
from a non-primary model is still reported `fallback_used` / `X-Platform-
Degraded`. Only **clean production predicts** are cached: success + output
schema valid (or no schema), `useCache` set only by `Predict` (Studio test +
shadow always run fresh); failures and schema-invalid outputs are never cached.
A hit returns `cached:true` with **zero usage/cost** (budget gate unaffected),
writes a `cache_hit=1` run row, and skips the provider entirely. Cache errors
are misses — Redis going down degrades performance, never predictions.

**Pricing** (`pricing.go` + `pricing.json`): `{model: {input_per_1m, output_per_1m}}`.
`CalculateCost` rounds to 6 decimals; unknown models cost 0. `PricingTable()` backs
`GET /pricing` so the frontend estimates with identical rates (single source of truth).

### 3.7 Database — `internal/db`

SQLite via `modernc.org/sqlite` (pure Go, no cgo). WAL mode, `busy_timeout=5000`,
`SetMaxOpenConns(1)` (single writer). **Migration strategy:** idempotent
`CREATE TABLE IF NOT EXISTS` + guarded `ALTER TABLE ADD COLUMN` (ignore "duplicate
column") — existing DBs upgrade in place on boot. Follow this pattern for all future
schema changes.

**Timestamp parsing gotcha:** we write `DATETIME` columns as `"2006-01-02 15:04:05"`
(`fmtTime`), but the `modernc.org/sqlite` driver recognizes `DATETIME`/`TIMESTAMP`
columns and round-trips them through `time.Time`, so a value scanned back into a Go
`string` comes out as **RFC3339** (`"2006-01-02T15:04:05Z"`). `parseTime` (in both
`internal/tasks/store.go` and `internal/db/queries.go`) therefore tries RFC3339 layouts
**and** the space-separated form — accepting only one made every version/run timestamp
read back as the zero time (which surfaced in the UI as `0001-01-01`).

**`runs`** — one row per model call (the trace store):

| Column group | Columns |
|---|---|
| Identity | `id`, `run_id` (uuid, groups a fan-out), `session_id` (playground only) |
| Request | `prompt` (rendered), `system_prompt`, `model` |
| Result | `response`, `success`, `error`, `latency_ms` |
| Usage | `input_tokens`, `output_tokens`, `total_tokens`, `cost_usd` |
| Attribution | `user_id`, `user_email`, **`task_id`**, **`prompt_version`**, **`provider`** |
| Observability | `fallback_used` (fallback chain), `cache_hit` (prediction cache), `is_test` (Studio test panel) |
| Time | `created_at` (UTC, `YYYY-MM-DD HH:MM:SS`) |

Indexes: `run_id`, `session_id`, `user_id`, `task_id`.

**`feedback`** — `(run_id, model, user_id)` unique → `rating` 1–5, upsert semantics.
`run_id` is the design doc's `trace_id`.

**`tasks`** — the registry (see §3.5 fields). **`prompt_versions`** —
`(task_id, version)` unique → template/system prompt/note/author; backfilled from
active task configs at migrate time; the task's `prompt_version` points at the live
row. **`shadow_reports`** — persisted comparison reports (match_rate, latency
percentiles, cost, mismatch details JSON). `runs.is_test` flags Studio test-panel
calls (spend still counts).

**Queries** (`queries.go`): `InsertRun`, `GetRunByID` (poll), `ListSessions` /
`GetSession` / `DeleteSessions` (all user-scoped), `UpsertFeedback`,
`DashboardStats(userID)` → totals + `by_task` (runs/tokens/cost/avg-latency/success-rate)
+ `by_model` (incl. avg star rating via pre-aggregated join — no fan-out inflation) +
daily time series; `TaskSpendToday` (budget gate), `TaskDailyStats` (per-task stats
endpoint). Pre-Phase-0 rows surface as task `untagged` via COALESCE.

**`RunWriter`** (`runwriter.go`): buffered channel (1024) + drain goroutine →
`InsertRun`; non-blocking `Write` with dropped-row counter; `Close()` flushes on
shutdown. Handlers go through `Handler.insertRun`, which uses the writer when set and
falls back to synchronous inserts when nil (tests stay deterministic).

### 3.8 HTTP API — `internal/api`

`router.go` — chi router; RequestID/Logger/Recoverer; CORS restricted to
`ALLOWED_ORIGINS` with `AllowCredentials: true`.

**Public:** `GET /health` · `GET /auth/demo-users` · `POST /auth/login {user_id}` (sets
cookie) · `POST /auth/logout`.

**Behind `RequireAuth`** (and, for `/v1/tasks/*`, `RequirePermission` — see the RBAC
matrix in §3.3; the Studio playground rows below are open to any authenticated user):

| Endpoint | Purpose |
|---|---|
| `GET /auth/me` | Frontend auth bootstrap |
| `GET /pricing` | Pricing table for client-side estimation |
| `POST /run` | Playground fan-out (Compare UI). Rows stamped `task_id=playground` |
| `GET/DELETE /sessions`, `GET /sessions/{id}` | Playground history (user-scoped) |
| `GET /sessions/{id}/leaderboard` | Per-session model leaderboard: avg manual ★ per model (`{session_id, entries:[{model, avg_score, rating_count}]}`), ordered by score. User-scoped; the SQL selects the session's `run_id`s in a subquery so each `feedback` row counts once (a fan-out stores one `runs` row per model under one `run_id`, so a naive join inflates) |
| `POST /feedback {run_id, model, rating}` | 1–5★ upsert |
| `GET /dashboard` | Per-user usage: totals, by_task, by_model, daily |
| `POST /v1/tasks` · `GET /v1/tasks` · `GET/PUT /v1/tasks/{id}` | Registry CRUD. PUT has **merge semantics** — absent fields keep current values; an explicit `"input_schema": null` / `"output_schema": null` **clears** that schema (the only way to remove one). Schemas re-validate on write → 422 if not compilable |
| `POST /v1/tasks/{id}/predict {inputs}` | **The product endpoint** (see below) |
| `GET /v1/tasks/runs/{run_id}` | Poll a run (becomes async-result fetch in Phase 3) |
| `GET/POST /v1/tasks/{id}/versions` | Prompt history / save a draft (not activated) |
| `DELETE /v1/tasks/{id}/versions/{version}` | Prune a prompt version. **admin-only** (`task:delete`); **409** for the active version (deploy another first) |
| `POST /v1/tasks/{id}/deploy {version}` | Activate a version (Phase 2 eval gate slots here) |
| `POST /v1/tasks/{id}/test {inputs, version?, model?}` | Run pipeline as `is_test` with prompt/model overrides — Studio test panel |
| `GET /v1/tasks/{id}/stats?days=N` | Task-scoped totals + daily series, all callers |
| `POST /v1/shadow/compare {task_id, items[≤200]}` | Field-level match vs `expected_output` per item; persists report |
| `GET /v1/shadow/reports?task_id=` | List persisted shadow reports |

Route-order note: `/v1/tasks/runs/{run_id}` is registered **before** `/v1/tasks/{task_id}`
so `"runs"` never matches as a task id.

**`Predict` pipeline** (`task_handlers.go` + the shared core in `predict_core.go`,
reused by Test and Shadow): resolve task (404 / 409-if-inactive; in-memory config
cache, no DB read) → **budget gate** (429 + `Retry-After` to UTC midnight when daily
spend ≥ cap; warn-log at 80%; cap 0 = exempt; spend comes from an in-memory view —
`budget_cache.go`: DB SUM refresh ≤ every 5s + per-prediction local increments — so
no per-request aggregate query and async-writer lag can't under-count) → `executePrediction`: `ValidateInput` (422) → `RenderPrompt` →
**cache lookup** (opt-in per task; hit → `cached:true`, zero usage, `cache_hit`
run row, respond immediately) → `llm.CallWithFallback` over
`[model, fallbacks...]` → if output schema:
`ValidateOutput` → `output_valid` flag (invalid output returns **200 with
`output_valid:false` + `raw_response`**, not an error — correction retry is Phase 2;
upstream chain failure returns **502**) → async run write (task/user/provider/version/
fallback/is_test stamped) → **`X-Platform-Degraded: true`** header when a fallback
served it or the chain failed → respond (`fallback_used` included):

```json
{ "task_run_id": "...", "task_id": "...", "prompt_version": 1,
  "model": "llama-groq", "provider": "groq",
  "output": {…}, "output_valid": true, "raw_response": "…", "error": null,
  "cached": false,
  "usage": {"input_tokens":172,"output_tokens":53,"total_tokens":225,"cost_usd":1.3e-05},
  "latency_ms": 696 }
```

---

## 4. Frontend Deep Dive

**Stack:** React 19, Vite 8, Tailwind 4, `@meesho/merlin-ui-tailwind` (design system),
`js-tiktoken`. No router library — a `view` state switch in `AppShell`. Dev proxy
(`vite.config.ts`) forwards `/run /sessions /health /auth /pricing /feedback /dashboard /v1`
to `:8000`.

**Auth flow:** `main.tsx` wraps the app in `AuthProvider` (`src/auth/AuthContext.tsx`),
which bootstraps via `GET /auth/me` (401 = logged out, no crash). `App.tsx` is the gate:
spinner → `LoginScreen` (one-click demo users from `/auth/demo-users`) → `AppShell`.
`useAuth` hook lives in `src/auth/useAuth.ts` (separate file for fast-refresh). The API
client (`src/api/client.ts`) sends `credentials: 'include'` on every call and throws
typed `ApiError{status}`.

**Pages** (top-nav in `AppShell` — `compare | tasks | versions | estimate | dashboard` —
which also fetches `/pricing` once and feeds `setPricing`):
- **Tasks / Studio** (`TasksPage`) — master/detail over the registry. Per task: config
  summary + 30-day usage strip, **model-routing chain editor** (ordered list,
  position 0 = primary; add models from the registry, drag rows to reorder,
  remove; saves `{model, fallback_models}` via PUT merge), **schema editor**
  (`components/SchemaEditor` + `utils/schema.ts`) — per-schema enable toggle
  plus a visual **Fields** mode (name / type / required / description, enum for
  strings, element type for arrays) and a **JSON** mode that round-trips with it
  and is the escape hatch for anything the field view can't represent (the
  converter returns null and forces JSON mode rather than dropping data); both
  schemas save in one PUT, prompt editor with token/cost estimate, **save draft →
  test (schema-generated input form, version/model overrides, validity badge) →
  deploy (confirm)**, version history (the reusable `components/VersionHistory`
  — paginated "show N at a time", side-by-side compare vs the live prompt,
  deploy, and **admin-only delete** of inactive versions). The
  edit→test→deploy loop runs entirely in the browser. **All write/deploy/delete
  actions are role-gated** (`auth/permissions.ts` mirrors the backend matrix —
  UI gating only; the gateway enforces): non-creators see disabled editors + a
  read-only banner, only approver/admin see Deploy, only admin sees Delete.
- **Versions** (`VersionsPage`) — a dedicated, task-agnostic home for prompt
  history: pick a task on the left, manage its versions on the right via the same
  `VersionHistory` component the Studio task detail embeds (identical compare /
  deploy / delete / pagination behaviour).
- **Compare** (`ComparePage` + `useChat`/`useSessions` + `Sidebar`/`ChatArea`/
  `ChatInput`/`SystemPromptBar`) — N-model side-by-side multi-turn chat, image attach,
  temperature/max-token controls, session history, per-response latency/tokens/cost
  metadata and `StarRating` (POST /feedback keyed by the `run_id` threaded through
  `useChat` state). `ChatArea` renders one `ModelColumn` per selected model (each
  column independently scrolls + shows a typing spinner) plus a **🏆 Leaderboard**
  button (disabled until a session exists) that opens `LeaderboardModal` —
  `GET /sessions/{id}/leaderboard`, ranked avg ★ per model with a tie callout,
  framed as the manual precursor to the eval layer. This is the future Prompt Studio
  "sample test panel". **Its API contract (`/run`, `/sessions`) must not leak into
  `/v1/tasks/*`.**
- **Estimate** (`EstimatePage`) — single + batch (blank-line/`---` separated) prompts ×
  model multi-select × expected output tokens → per-model token/cost table + totals.
  Client-side only: `countTokens` (cl100k_base, stated approximation) + `estimateCost`
  fed by backend `/pricing` (fallback table in `utils/tokens.ts` for offline).
- **Dashboard** (`DashboardPage`) — summary cards, **By task** table (runs/tokens/cost/
  latency/success), By model table (incl. avg ★), daily-spend CSS bars. No chart lib.

**Types:** `src/types/index.ts` mirrors the Go JSON contracts exactly (`TRunResponse`,
`TDashboard{by_task,by_model,daily}`, `TUser`, `TPricing` …). Update it whenever a Go
response type changes.

---

## 5. Running & Testing

**One-shot dev orchestrator** — `./dev.sh` (config in `dev.yaml`) runs all three servers
detached (nohup), each pinned to its port (the port is freed first), with pids + logs under
`.dev/`:

```bash
./dev.sh            # restart everything (backend :8000, frontend :5173, client :5174)
./dev.sh status     # show what's listening
./dev.sh logs backend   # tail one service's log
./dev.sh stop [name]    # stop all, or one service
```

Or run each by hand:

```bash
# Backend (loads .env; needs ≥1 provider key)
cd llm_platform_go && go run ./cmd/server          # :8000

# Frontend (Studio)
cd llm_platform_frontend && npm run dev             # :5173 (proxies to :8000)

# Client portal
cd llm_platform_client && npm run dev               # :5174 (proxies /v1 /health /pricing to :8000)

# Mint a service token (e.g. for the client portal or CIS). -role defaults to caller
# (read + predict); use admin|creator|approver|viewer for a different principal.
cd llm_platform_go && go run ./cmd/issue-token -sub svc:cis -email cis@svc.local -role caller -ttl 8760h

# Verify
go build ./... && go vet ./... && go test ./...     # backend
npm run build && npm run lint                       # frontend (3 pre-existing hook warnings OK)
```

**Test suites** (`tests/`, black-box over httptest + in-memory SQLite, plus
`internal/llm/*_test.go`):
- `handlers_test.go` — endpoint shapes, auth-required, sessions CRUD.
  `newTestServerWithClients` injects fake providers.
- `auth_feedback_test.go` — demo store, token round-trip, login flow, feedback +
  dashboard aggregates, per-user isolation.
- `rbac_test.go` — the full role × permission matrix (`TestRBACMatrix`) and that a
  token with no role claim resolves to `caller` (`TestDefaultRoleForTokenWithoutClaim`).
- `prompt_redaction_test.go` — callers without `task:view_prompt` get
  `prompt_template`/`system_prompt` blanked on task + version reads.
- `delete_version_test.go` — version delete is admin-only and 409s on the active version.
- `schema_update_test.go` — PUT merge semantics for schemas, incl. `"input_schema": null`
  clearing one.
- `tasks_test.go` — registry CRUD, prompt-version bump rules, schema/template
  validation, fenced-output parsing, YAML seed + re-seed version bump.
- `predict_test.go` — full predict pipeline against a fake OpenAI-compatible server:
  happy path (attribution, usage, parsed output), 422 input cases, invalid-output
  flagging, 404, playground stamping, dashboard by_task.
- `db_test.go` — run insert/query/delete, pagination, ordering (user-scoped).
- `phase1_test.go` — budget 429 + Retry-After + exemption, full version lifecycle
  (draft doesn't activate, test stamps `is_test` + renders the draft, deploy switches
  production), shadow compare exact numbers + persistence, per-task stats, RunWriter
  flush/drop semantics.
- `internal/llm/runner_test.go` / `provider_test.go` — registry↔pricing.json parity,
  retry rules + error classification, message building, OpenAI-compat wire format
  (headers, endpoint path, error-body mapping). `anthropic_test.go` — happy path,
  SDK-error → `APIError` mapping, refusals surface as 400-class content-level
  errors (never trip the breaker or advance the fallback chain).
- `internal/llm/breaker_test.go` — breaker state machine (fake clock), infra-failure
  classification, fallback advance/stop rules, open-circuit fail-fast.
- `internal/llm/prober_test.go` — probe-only breakers never admit production
  traffic while open; prober closes the circuit only when the provider
  recovers; end-to-end: outage → fallback serves (primary untouched) → probe
  recovery → traffic back on the primary.
- `cache_predict_test.go` — cache hit serves without a provider call (zero cost,
  `cache_hit` row, spend unchanged), key sensitivity (inputs, deploy-with-identical-
  template invalidates), opt-in required, Studio test bypass, failures and
  schema-invalid outputs never cached. `internal/cache/cache_test.go` — key
  determinism/sensitivity, Redis round-trip + TTL via miniredis, outage = miss.

Test auth helper: `authReq(t, method, url, body)` attaches a Bearer token for `u-admin`
signed with the test secret.

---

## 6. Design Decisions & Invariants (do not regress)

1. **Pure Go, single binary** — no LiteLLM/Langfuse/Python. Decided 2026-06-12.
2. **Task is the unit of everything** — cost, versioning, RBAC, budgets, (future) eval.
   Every run row must carry `task_id`.
3. **The platform never owns caller business logic** — no "if confidence < X then
   route to QC" inside the platform.
4. **Seams to preserve:** `users.Store` (identity), `llm.Provider` (model backends),
   `internal/db` query functions (storage engine), `pricing.json` (rates).
5. **Playground ≠ product API** — Compare UI talks to `/run`/`/sessions`; services talk
   to `/v1/tasks/*`. Multi-turn chat semantics must not leak into the product API.
6. **Migrations are idempotent boot-time upgrades** (guarded ALTERs). Demo-era data is
   throwaway; schema migrates, data does not.
7. **Observability writes never fail a response**; prediction responses distinguish
   "model failed" (502) from "output didn't validate" (200 + `output_valid:false`).
8. **Frontend pricing/types always follow the backend** (`/pricing`, mirrored types).
9. **The prediction hot path does no synchronous DB work** (decided 2026-06-12):
   task config from the in-memory store cache, budget from the cached spend view,
   run rows via the async writer, prediction cache in Redis/memory. New per-request
   features must follow this — read from memory, write async.
