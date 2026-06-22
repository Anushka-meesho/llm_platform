# Engineering Guide — `llm_platform_go` (Go backend)

**Last updated: 2026-06-22**

> This is the **narrative onboarding companion** to `docs/repo_work_doc.md` (the reference
> manual). Where the work doc is tables and exact contracts, this guide is the *why* — the
> design rationale, the boot story, the invariants you must not regress, and the seams you'll
> reach for first. Read the work doc when you need the precise field list or endpoint signature;
> read this when you're new to the repo and want the shape of the thing in your head.
>
> **All paths are rooted at the backend repo** (`internal/llm/runner.go`, `cmd/server/main.go`).
> This repo (`llm_platform_go`) is the *standalone Go backend* — a single binary, no Python in
> the hot path, no native vendor LLM SDKs.

---

## Table of contents

1. [What this is](#1-what-this-is)
2. [Repo layout](#2-repo-layout)
3. [Boot sequence](#3-boot-sequence)
4. [Configuration](#4-configuration)
5. [Auth & RBAC](#5-auth--rbac)
6. [Task registry](#6-task-registry)
7. [Model layer](#7-model-layer)
8. [Per-task limits — two distinct 413s](#8-per-task-limits--the-two-413-sources)
9. [Database](#9-database)
10. [HTTP API](#10-http-api)
11. [Running & testing](#11-running--testing)
12. [Design decisions & invariants](#12-design-decisions--invariants-do-not-regress)

### Companion frontends (out of scope here)

This repo is **backend-only**. Two separate React/Vite single-page apps consume it over HTTP:

- a **Studio operator UI** (Compare playground, Tasks authoring, Dashboard, and the admin-only
  History / Health / Test pages) — a **cookie-auth** client; and
- a **client portal** for consumers (task catalog + a live Try-it predict panel against
  `/v1/tasks/*`) — an `Authorization: Bearer` **service-token** client (no login).

The **cross-origin contract** the backend owns for them is: CORS (`ALLOWED_ORIGINS` with
`AllowCredentials: true`), the session-cookie scope (`COOKIE_DOMAIN` / `COOKIE_SECURE`), and
the split-repo `VITE_BACKEND_URL` wiring. The full split-repo / cross-origin / first-run runbook
is `docs/DEPLOY.md` — that is the single source for deployment topology; this guide does not
duplicate it.

---

## 1. What this is

The backend is Meesho's internal **LLM prediction factory**. The product unit is a **Task**:
a caller registers one (input/output JSON Schemas + a prompt template + a model routing chain +
sampling params + a daily budget + per-task input caps + cache settings), then makes a single
HTTP call — `POST /v1/tasks/{id}/predict` with `{inputs}` — and gets back a schema-validated,
structured prediction with full cost/token/latency attribution. The platform owns prompt
execution, model routing, output validation, cost metering, health/fallback, and run recording.
It deliberately does **not** own caller business logic, orchestration, or data preprocessing.

**The one architecture decision that shapes everything: pure Go, single binary.** No LiteLLM,
no Langfuse, no Python in the request path. Routing, prompt management, tracing, eval, and the
cache are all built in this repo. Two driver consequences you'll feel constantly:

- **No CGo.** SQLite is `modernc.org/sqlite` (pure-Go); the binary builds and ships without a C
  toolchain. Postgres is `pgx`. Choosing between them is a runtime flag (`DB_DRIVER`), not a
  build tag.
- **No native vendor SDKs.** Every model — OpenAI, Gemini, Claude, Llama — is reached over an
  **OpenAI-compatible** chat-completions wire. There is exactly one provider implementation; the
  difference between "Groq" and "the Meesho gateway" is just a base URL and an auth header.

**Current capabilities** (Phases 0 + 1 complete):

- DB-backed task registry (authored through the Studio API — no YAML/file seeding) + the
  schema-enforcing predict endpoint.
- **Multimodal input** — a task may accept one or many images (base64 data URLs or image URLs),
  attached to vision models as OpenAI multimodal content blocks. The live `attribute-extraction`
  task uses this.
- **Resilient routing** — fallback chains plus a per-`(task, model)` health breaker that routes
  around a model *for one task* after repeated failures (provider error *or* schema-invalid
  output), with exponential-backoff cooldown + admin reset, surfaced via `X-Platform-Degraded`.
- **Prompt registry** — first-class versions with a draft → test → deploy lifecycle and auto
  history.
- **Budget enforcement** (per-task daily cap → 429) and **per-task rate limiting**
  (rolling-window request / token / input caps → 429 / 413).
- **Prediction cache** (Redis / in-memory / off) — per-task opt-in, exact-match, zero-cost hits.
- **Admin observability** — a cross-tenant prompt-history viewer and a model-health console.
- **Shadow harness** — field-level match-rate + latency p50/p95 against a labelled dataset.
- **Pluggable database** (SQLite or Postgres) behind a dialect seam.
- **Service auth + two-role RBAC** (`admin` / `client`) with prompt redaction for clients.
- **Deployment readiness** — `APP_ENV=prod` boot gate, `AUTH_MODE` switch (demo vs SSO scaffold),
  `cmd/migrate` + `cmd/bootstrap`, graceful shutdown + HTTP timeouts, a `/ready` probe.

The deeper-design notes live in `docs/00-big-picture.md` … `docs/10-caching-and-cost.md`; the
gap analysis and roadmap are in the repo-root `docs/`.

---

## 2. Repo layout

Annotated tree of what matters. One line per package.

```
llm_platform_go/                       # repo root (the single Go binary)
├── cmd/
│   ├── server/main.go                 # the server: boot sequence (validate → DB → wire → serve → graceful shutdown)
│   ├── issue-token/main.go            # mint long-lived svc:* Bearer JWTs (-role client|admin)
│   ├── migrate/main.go                # apply DB schema out-of-band (prod never auto-migrates)
│   └── bootstrap/main.go              # first-run: gen JWT secret, Validate, migrate, lock DB file, mint break-glass admin
├── internal/
│   ├── api/                           # HTTP layer — chi router, middleware, handlers, predict core, SSO scaffold
│   ├── auth/                          # HS256 JWT issue/parse, cookie helpers, RBAC capability map
│   ├── cache/                         # prediction cache behind a Cache interface — Redis | memory | off
│   ├── config/                        # env-driven config + Validate() (the prod safety gate)
│   ├── db/                            # open/migrate (sqlite|postgres), dialect seam, all SQL, the 3 async writers
│   ├── health/                        # the per-(task, model) circuit-breaker Tracker (in-process)
│   ├── llm/                           # provider client, routing registry, CallModel, fallback walk, pricing
│   ├── ratelimit/                     # per-task rolling-window request/token/input limiter
│   ├── schema/                        # embedded request-body JSON Schemas (422 before a handler runs)
│   ├── tasks/                         # Task registry: task.go, store, validate, render, versions, seed
│   ├── types/                         # request/response contracts + RunRow + GatewayAttempt
│   └── users/                         # identity seam: Store interface + DemoStore (the SSO swap point)
├── tests/                             # black-box HTTP + DB tests (httptest + in-memory SQLite)
├── pricing.json                       # per-model $/1M-token rates (the cost source of truth)
├── .env.example                       # annotated config template (copy to .env)
└── docs/                              # this guide, repo_work_doc.md, DEPLOY.md, 00–10 design notes
```

**`internal/api` files worth knowing by name:** `router.go` (route table + CORS),
`middleware.go` (`RequireAuth` / `RequirePermission` / `RequireAdmin`), `predict_core.go` (the
shared `executePrediction` pipeline), `task_handlers.go` (Predict/Test), `admin_handlers.go`
(cross-tenant runs + model-health), `shadow_handlers.go`, `version_handlers.go`,
`budget_cache.go` (the cached per-task spend view), `auth_sso_handlers.go` (the OIDC scaffold).

**`internal/llm` files:** `runner.go` (the registry + `CallModel`), `client.go` (the
OpenAI-compatible provider + multimodal marshal + array-content unmarshal), `fallback.go`
(`CallWithFallbackOpts`), `failure.go` (`isInfraFailure` / `shouldFallback`), `pricing.go`,
`models.go`.

**`pricing.json`** — `{model: {input_per_1m, output_per_1m}}`, loaded once at boot from
`PRICING_PATH`. A registry test enforces that every active model has a row here and vice versa,
so adding a model without pricing it fails CI.

---

## 3. Boot sequence

`cmd/server/main.go` runs these steps in order. The shape to remember: **load → validate →
open state → wire workers → serve → drain.** Nothing in the request hot path does synchronous
DB work, which is why so much of boot is about standing up in-memory views and async writers.

1. **`godotenv.Load()`** — `.env` is optional; real environment variables work too.
2. **`config.Load()`** — parses every env var (§4). Fails the boot only if **zero** provider keys
   are set; a single missing key just logs a warning and that provider fails at call time.
3. **`config.Validate()`** — the prod safety gate. When `APP_ENV=prod` it **hard-fails the boot**
   on any insecure default: a dev/blank `JWT_SECRET`, `AUTH_MODE=demo`, missing `ALLOWED_ORIGINS`,
   `COOKIE_SECURE=false`, or a relative `DB_PATH` / `PRICING_PATH`. In dev it is essentially a
   no-op beyond driver sanity. This is the *single* place insecure config is rejected — see the
   invariant in §12.
4. **`llm.LoadPricing(PRICING_PATH)`** — the cost table into memory.
5. **`db.Open(DB_DRIVER, DB_PATH, DB_DSN)`** — SQLite (WAL, single writer) **or** Postgres (pgx,
   real pool). **Migrations auto-run here in dev only**; in prod they are applied out-of-band by
   `cmd/migrate`, so a rolling deploy never blocks on or races a schema change.
6. **`llm.BuildClients(...)`** — one `Provider` per backend. There are two: `Groq` (direct API,
   `Authorization: Bearer`) and `Meesho` (the internal gateway, OpenAI-compatible with `x-bf-vk`
   auth). Either is left `nil` when its key is absent, so a missing key is a normal "not
   configured" call error — never a tripped breaker.
7. **`tasks.NewStore(...)` → `tasks.SeedPlayground` (+ `SeedAttributeExtraction`)** — idempotently
   seeds the built-in `playground` task and the live `attribute-extraction` task; never
   overwrites. Every other task lives in the DB, authored at runtime. A fresh DB starts with just
   those built-ins.
8. **`users.NewDemoStore()`** — the identity swap seam (§5). Seeds a single `u-admin` user.
9. **`db.NewRunWriter` + `db.NewGatewayAttemptWriter`** — async observability writers (run rows +
   per-attempt gateway trace).
10. **`db.NewHealthEventWriter` + `health.NewTracker(...)`** — the per-`(task, model)` breaker
    (thresholds from config; transitions persisted via the health writer).
11. **`ratelimit.New(...)`** — the per-task rolling-window limiter.
12. **Prediction cache** — by `CACHE_BACKEND`: Redis (boot fails on a bad addr — fail fast), in-
    process memory, or off.
13. **`api.NewRouter(RouterDeps{...})` → `http.Server`** with read/write/idle **timeouts**;
    `ListenAndServe` in a goroutine. **SIGINT/SIGTERM triggers graceful shutdown**: stop
    accepting, drain in-flight requests, then `Close()` the three async writers so buffered rows
    flush before exit.

**`/ready` vs `/health`.** `/health` is liveness (always 200 with `models_available`). `/ready`
is readiness — it pings the DB and returns 503 (`not_ready`) when the DB can't serve, so an
orchestrator can hold traffic off a process whose DB is down.

---

## 4. Configuration

All config is loaded by `internal/config` (`.env` via godotenv, then real env). In
`APP_ENV=prod`, `config.Validate()` rejects insecure defaults at boot (§3 step 3).

| Var | Default | Controls |
|---|---|---|
| `APP_ENV` | `dev` | `prod` turns on `config.Validate()` and disables inline (boot-time) migrations |
| `AUTH_MODE` | `demo` | `demo` = passwordless pick-a-user login; `sso` = OIDC redirect/callback (demo login unregistered) |
| `PORT` | `8000` | HTTP listen port |
| `DB_DRIVER` | `sqlite` | `sqlite` (location = `DB_PATH`) or `postgres` (location = `DB_DSN`) |
| `DB_PATH` | `./llm_platform.db` | SQLite file (only when `DB_DRIVER=sqlite`; **must be absolute in prod**) |
| `DB_DSN` | — | Postgres connection string (required when `DB_DRIVER=postgres`) |
| `PRICING_PATH` | `./pricing.json` | Cost table (**absolute in prod**) |
| `GROQ_API_KEY` / `GROQ_BASE_URL` | — / `https://api.groq.com/openai/v1` | Groq (direct API) |
| `MEESHO_GATEWAY_VK` / `MEESHO_GATEWAY_BASE_URL` | — / internal gateway URL | Meesho gateway (`x-bf-vk`) — serves **every non-Groq model** (GPT-4o, Gemini, Claude) |
| `JWT_SECRET` | `dev-insecure-secret-change-me` | HS256 session/service signing key — **must be real outside dev** |
| `AUTH_COOKIE_NAME` | `llm_platform_token` | Session cookie name |
| `AUTH_ISSUER` | `llm-platform-demo` | JWT `iss` claim |
| `TOKEN_EXPIRY` | `12h` | Session lifetime / cookie MaxAge |
| `COOKIE_DOMAIN` / `COOKIE_SECURE` | — / `false` | Cookie scope/security — `.example.com` + `true` for the subdomain topology; **Secure required in prod** |
| `ALLOWED_ORIGINS` | `http://localhost:5173` (dev fallback) | CORS allowlist, comma-separated, credentials mode; **required in prod** |
| `OIDC_ISSUER` / `OIDC_CLIENT_ID` / `OIDC_CLIENT_SECRET` / `OIDC_REDIRECT_URL` / `OIDC_POST_LOGIN_URL` | — | SSO config, read only when `AUTH_MODE=sso` |
| `REDIS_ADDR` / `REDIS_PASSWORD` / `REDIS_DB` | — / — / `0` | Redis cache backend |
| `CACHE_BACKEND` | `redis` if `REDIS_ADDR` set, else `off` | `redis` \| `memory` (dev) \| `off` |
| `HEALTH_BREAKER_ENABLED` | `true` | Per-(task, model) health breaker on/off |
| `HEALTH_FAILURE_THRESHOLD` | `3` | Consecutive failures (provider error or schema-invalid) before tripping |
| `HEALTH_BASE_COOLDOWN` | `30s` | First unhealthy window (doubles per re-trip) |
| `HEALTH_MAX_COOLDOWN` | `30m` | Cap for the backed-off cooldown |
| `RATE_LIMIT_ENABLED` | `true` | Per-task rate limiter on/off |
| `RATE_WINDOW` | `1m` | Rolling window per task |
| `RATE_MAX_REQUESTS` / `RATE_MAX_TOKENS` / `RATE_MAX_INPUT_TOKENS` | `600` / `200000` / `16000` | per-window request cap / per-window token cap / per-request input cap (0 = off) |
| `RATE_CHARS_PER_TOKEN` / `RATE_TOKENS_PER_IMAGE` | `4` / `1000` | Input-token estimation |

**Prod hardening checklist** (all enforced by `config.Validate()` when `APP_ENV=prod`): a real
`JWT_SECRET`; `AUTH_MODE=sso` (demo login is forbidden); a non-empty `ALLOWED_ORIGINS`;
`COOKIE_SECURE=true`; absolute `DB_PATH` and `PRICING_PATH`. At least one provider key
(`GROQ_API_KEY` or `MEESHO_GATEWAY_VK`) is required at boot regardless of env. The annotated
template is `.env.example`; the deploy runbook is `docs/DEPLOY.md`.

---

## 5. Auth & RBAC

**The token is the source of authority.** Everything authorizes from a signed HS256 JWT — never
from a per-request identity-store lookup — so the predict hot path stays DB-free (an invariant,
§12). The claims are `{sub, email, name, role, iss, iat, exp}` (`internal/auth/auth.go`). A token
arrives either in `Authorization: Bearer …` (service callers, the client portal) or in the
HttpOnly session cookie (the Studio); `TokenFromRequest` accepts both. `RequireAuth`
(`internal/api/middleware.go`) validates signature + expiry and puts an `auth.User` (incl. role)
on the request context.

### Demo vs SSO (`AUTH_MODE`)

`router.go` registers different routes per mode:

- **`demo`** (dev default) — the passwordless pick-a-user login: `GET /auth/demo-users` +
  `POST /auth/login {user_id}`. The flow: look the user up in `internal/users`, mint a JWT, set
  the cookie (`SameSite=Lax`, `Secure` per `COOKIE_SECURE`, `MaxAge = TOKEN_EXPIRY`). The demo
  store seeds a single user — `u-admin` / `admin@demo.local` / role `admin`.
- **`sso`** — the demo routes are **not registered at all**; `GET /auth/sso/login` +
  `GET /auth/sso/callback` take their place (the OIDC scaffold in `auth_sso_handlers.go`). It
  shares the cookie-minting tail (`issueSession`) with the demo path, so once the IdP handshake
  resolves a user, the session is identical. The handshake itself is a documented `TODO`
  returning `501` until the `OIDC_*` env is wired; until then the break-glass admin token from
  `cmd/bootstrap` provides access. `config.Validate()` forbids `demo` in prod.

### Two roles, a capability matrix

Two principals exist today: the human **operator** (`admin`, runs the platform via the Studio,
holds everything) and the service **client** (a backend that only invokes the product predict API
and never sees prompts). RBAC (`internal/auth/rbac.go`) encodes this as six capabilities mapped to
two roles:

| Role | task:read | task:predict | task:write | task:deploy | task:delete | task:view_prompt |
|---|:---:|:---:|:---:|:---:|:---:|:---:|
| **admin** | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| **client** | ✓ | ✓ | ✗ | ✗ | ✗ | ✗ |

The capability set is **finer-grained than the two roles on purpose** — it anticipates a future
creator/approver split (note `task:deploy` is deliberately separate from `task:write`, the publish
gate). But only `admin` and `client` are wired, so `write` / `deploy` / `delete` are effectively
admin-only today.

- `RequirePermission(perm)` runs after `RequireAuth` and returns **403** when the role lacks the
  capability. The cross-tenant admin views use `RequireAdmin` — gated on the `admin` **role**, not
  a capability, because they expose privacy-sensitive cross-tenant data.
- A token with **no** role claim resolves to `admin` (`DefaultRole`, kept for backward
  compatibility; `cmd/issue-token` always stamps an explicit role).

### Prompt redaction

A `client` (anyone without `task:view_prompt`) sees the task **contract** (schemas, metadata) but
not the prompt internals: `GET /v1/tasks`, `GET /v1/tasks/{id}`, and `…/versions` blank
`prompt_template` and `system_prompt`. The redaction copies the cached task (`redactedTask` in
`handlers.go`) so the shared config-cache entry is never mutated.

### The identity seam (`internal/users`)

```go
type Store interface {
    GetByID(ctx, id) (*User, error)  // ErrNotFound when absent
    List(ctx) ([]*User, error)       // demo login screen only
}
```

`DemoStore` is in-memory and persists nothing — by design, there's no demo data to migrate.
**Moving to real SSO = implement `Store` against the IdP and change one constructor line in
`main.go`** (the SSO callback resolves a user and calls `issueSession`). Nothing else in the
codebase knows where users come from — this is the SSO swap point.

---

## 6. Task registry

A **Task** (`internal/tasks/task.go`) is the unit of everything — cost, versioning, RBAC,
budgets, caching, and future eval all key off `task_id`. The full struct:

```
ID              // slug [a-z0-9-]{2,64}
Name, Description
InputSchema     // JSON Schema, optional — validates {inputs}
OutputSchema    // JSON Schema, optional — validates the model's output
PromptTemplate  // Go text/template
SystemPrompt
PromptVersion   // the live version number; auto-bumped on prompt edits
Model           // the routing primary
FallbackModels  // ordered fallback chain
Temperature     // default 0.2
MaxTokens       // default 1000
DailyBudgetUSD  // 0 = exempt from the budget gate
CacheEnabled, CacheTTLSeconds   // per-task prediction-cache opt-in; default TTL 24h
MaxPromptChars  // per-task input cap — max chars of the rendered (system+user) prompt; 0 = no limit
MaxImageKB      // per-task input cap — max KB per image; 0 = no limit
MaxImages       // per-task input cap — max image count; 0 = no limit
Active
CreatedAt, UpdatedAt
```

### The DB is the single source of truth — no YAML

There is **no file/YAML seeding layer**. Tasks are created, edited, and deleted at runtime through
the Studio API (`POST/PUT/DELETE /v1/tasks`) and persist in the `tasks` table. Boot-time seeding is
limited to `SeedPlayground` (+ the live `attribute-extraction` task) in `seed.go`, which are
idempotent and never overwrite. This is a deliberate move away from config-file tasks: authoring,
versioning, and RBAC all live behind one API, so there's no second source of truth to drift.

### Store, config cache, and the edit lock

`Store` (`store.go` + `versions.go`) is the DB CRUD. `Get` is served from an **in-memory config
cache** (5s TTL, for out-of-band convergence; write-invalidated by Create/Update/Deploy) so the
predict hot path never reads the DB for task config — treat a returned `*Task` as immutable.
Config edits are serialized by the store's `editMu` write lock; a prediction reading config takes
a shared read lock, so a reader during an edit waits and then sees the new config (single-process
coordination). `Update` **auto-bumps `prompt_version`** only when `PromptTemplate` or
`SystemPrompt` changed (next number = `max(prompt_versions)+1`, so it never collides with a draft)
and appends a history row; non-prompt updates don't bump.

### Validation (`validate.go`)

A bad config is rejected **at write time, not call time**: slug shape, required `name` /
`prompt_template` / `model`, **known routing keys** (primary + every fallback must exist in the
registry), `temperature ∈ [0,2]`, `max_tokens > 0`, `cache_ttl_seconds ≥ 0`, schemas must compile,
template must parse. The three input-size limits (`max_prompt_chars`, `max_image_kb`, `max_images`)
must each be **non-negative** — `Validate()` rejects negatives. Compiled schemas are cached by
content hash, so an edit naturally invalidates them.

### Prompt rendering (`render.go`) — images by COUNT, never inlined

Rendering is Go `text/template` (`{{.field}}`, `{{if .field}}…{{end}}`) with `missingkey=error`
(referencing an undeclared key fails loudly), templates cached by content hash. Declared input
fields absent from the request are pre-seeded with `""` so optional-field `{{if}}` guards work.

The important rendering rule: **image fields are exposed to the template as their image *count*,
not the raw bytes** (`imageInputFields` / `imageValueCount`). A base64 data URL is never inlined
into the prompt text — that would bloat the prompt and the rate-limiter's input estimate. So
`{{if .images}}…{{end}}` still gates on presence and `{{.images}}` renders a small number; the
actual image bytes travel separately, attached to the user message as `image_url` vision blocks
(§7).

### Image inputs are typed by a JSON-Schema marker

An image input is recognised by a **schema marker**, not by a fixed property name: a property
typed as a string with `format:"image"`, or an array whose `items` carry `format:"image"`, under
**any** name the author chose. The implicit `image` / `images` names are kept only as a **legacy
fallback** for untyped callers and pre-typing tasks. This is what lets a task declare, say, a
`product_photos` image field and have the platform attach it to the vision model automatically.

### Prompt version lifecycle: draft → test → deploy

`SaveDraft` records a new version **without activating it** (`POST /versions` → `active:false`).
You test the draft (`POST /test` with a `version` override), and only `Deploy` (`POST /deploy`,
`task:deploy`) makes a version live — the explicit publish gate. The active version can't be
deleted (`409`); deploying another first is the way out. Editing a prompt via `PUT` also bumps the
version, so history is never lost.

### Worked example — `attribute-extraction` (live)

Input `{title*, description, category*, brand, images[]}` → output
`{attributes:{string:string}, confidence:0..1}`, model `gemini-2.5-flash` (vision), fallback
`[gpt-4o-mini]`, budget $50/day, **`max_tokens: 2048`** (Gemini 2.5 Flash spends hidden
"thinking" tokens from the same output budget — a tight cap truncates the JSON into a
schema-invalid response), cache on at 24h. The image field is typed via `items.format:"image"`,
so it's recognised as an image input by the marker rather than its name.

---

## 7. Model layer

This is `internal/llm`. The whole layer rests on one idea: **one provider, two wirings.**

### The provider — one implementation, two backends

`Provider` is a one-method interface: `Call(ctx, *chatRequest) (*chatResponse, error)` over the
OpenAI-compatible chat-completions wire (`client.go`). There is a **single** implementation,
`openAICompatProvider`. `BuildClients` wires two instances of it: `Groq` (direct vendor API,
`Authorization: Bearer`) and `Meesho` (the internal gateway, the `x-bf-vk` virtual-key header
instead). Either is `nil` when its key is absent. There is **no native Anthropic or Gemini
provider** — OpenAI, Gemini, and Claude all ride the gateway's OpenAI-compatible wire, so adding a
vendor needs no new provider code.

### Routing registry (`runner.go`)

A single `registry` map is the source of truth for routing: friendly key → `providerConfig`
(`{modelID, provider, clientFn, reasoning, minOutputTokens}`). **Every non-Groq model routes
through the Meesho gateway** (`clientFn: meeshoC`); only `llama-groq` calls a vendor directly. The
active keys today:

| Provider attribution | Served via | Friendly keys | Underlying modelID |
|---|---|---|---|
| `openai` | Meesho gateway | `gpt-4o`, `gpt-4o-mini` | `openai/gpt-4o`, `openai/gpt-4o-mini` |
| `gemini` | Meesho gateway | `gemini-2.5-pro`, `gemini-2.5-flash` | `vertex/gemini-2.5-pro`, `vertex/gemini-2.5-flash` |
| `anthropic` | Meesho gateway | `claude-sonnet-4-6` | `anthropic/claude-sonnet-4-6` |
| `groq` | Groq API (direct) | `llama-groq` | `llama-3.3-70b-versatile` |

`DefaultModels` (the `/run` fan-out default) is `["gpt-4o-mini", "gemini-2.5-flash", "llama-groq"]`.
More vendor variants sit commented out; because they share the gateway wire, enabling one is a
one-line uncomment with no new code (and the registry↔`pricing.json` parity test forces you to
price it).

The `reasoning` flag exists in `providerConfig` for OpenAI reasoning-family models (which reject
`max_tokens`/`temperature` and need `max_completion_tokens` instead), but no currently-active model
sets it.

### `CallModel` — the single execution path

`CallModel(ctx, clients, model, messages, temp, maxTokens) ModelResult` is used by **both** the
playground fan-out (`RunAll`) and `/predict`. It:

- **Retries up to 3×** on retryable statuses (`429/500/503`) with linear backoff (2s, 4s), and
  stops on context cancellation/timeout.
- **Classifies errors** (`classifyError`) into human-readable strings — timeout, network, auth,
  rate-limit, provider-down — so failures are legible in run rows.
- **Never panics** — a failure comes back as `ModelResult{Success:false, Error}`.

**Error classification → fallback decision** (`failure.go`): `isInfraFailure` is true for
`429/5xx`, network, and timeout (4xx config errors and caller cancellation are *not* infra);
`shouldFallback` is infra **plus** `401/403/404` provider-config errors. Those decide whether a
failed call advances the chain.

### Gemini-2.5 thinking support

Two accommodations, both in `runner.go` + `client.go`:

- **`minOutputTokens` floor.** Thinking tokens share the *same* output budget as the answer, so a
  low `max_tokens` can leave no room for the actual response. Each thinking model carries a floor —
  `gemini-2.5-pro`: **8192**, `gemini-2.5-flash`: **4096** — and `CallModel` silently raises
  `max_tokens` to that floor when it's lower.
- **Array-content unmarshal.** Thinking models can return **array-shaped** `content`
  (`[{type:"text",…}, {type:"thought",…}]`). `ChatMessage.UnmarshalJSON` accepts both the plain
  string form and the array form — it concatenates the text parts and **discards thought parts**.
  Non-thinking models are unaffected.

### Multimodal input (`collectImages`)

`ChatMessage.Images []string` holds image references. Its custom `MarshalJSON` emits the plain
string-content form when there are no images (byte-identical to the legacy wire) and the OpenAI
multimodal array (`[{type:text}, {type:image_url}…]`) when there are. The predict pipeline collects
images by the **schema marker** (`collectImages` + `imageSchemaFieldNames` in `predict_core.go`) —
any input-schema property typed as an image under any name — with the implicit `image`/`images`
names honoured as a **legacy fallback**. A field may hold one string or an array; blank/non-string
entries are dropped. Collected images are folded into the cache key and persisted on the run row.

### The fallback walk (`fallback.go`)

`CallWithFallbackOpts` walks `[primary, …fallbacks]` in order with three optional hooks — a
per-model cache `Lookup`, a `HealthGate` (the breaker), and an `OutputValidator`. For each model:

- **Skip** (no call) if the health gate reports it unhealthy *for this task*.
- **Serve cached** if `Lookup` hits — but only *during* the walk, as the router reaches that model,
  never as a pre-call shortcut. (So a recovered primary is always tried live, not shadowed by a
  stale lower-priority cache entry.)
- Otherwise **call live** and decide:
  - **Stop** on a usable success — a provider success that passes the validator (or no validator).
  - **Advance** on infra/config trouble (`429/5xx`/network/timeout, `401/403/404`, not-configured)
    **and on schema-invalid output** (a "schematic" failure) — recording the failure against
    health each time.
  - **Return immediately** on a `400/422` content error — bad input; retrying elsewhere just burns
    money, and it's not counted against health.

It sets `FallbackUsed` (served by a non-primary) and `Degraded` (fallback used, or the whole chain
failed) for the `X-Platform-Degraded` header, and records one `Attempt` per model touched
(`success` / `error` / `schema_invalid` / `skipped_unhealthy` / `cache_hit`) — the source of the
`gateway_attempts` trace (§9). The health gate + output validator are wired **only for production
predicts** (`useCache`, no model override); `CallWithFallback`/`CallWithFallbackCached` are thin
wrappers without them for callers that don't need gating.

### The schema-aware fallback chain

The combination above is what makes routing self-healing: a model that returns **schema-invalid**
output is treated like a failure — the walk advances to the next model in the *same request* and
the breaker records it. If *every* model returns schema-invalid output, the last one is returned
with `output_valid:false` (a 200, not an error). Only an upstream chain failure (or all models
unhealthy) is a 502.

### Per-`(task, model)` circuit breaker (`internal/health`)

The platform's **only** breaker, keyed on a *specific task's use of a specific model* — so a model
misbehaving for one task is routed around only for that task. The `Tracker` is process-wide and
mutex-guarded; the fallback walk feeds it through a task-bound `HealthGate` adapter built in
`predict_core.go`. There is **no separate per-provider breaker and no background prober** —
failures are discovered in-band, and a model's recovery trial is simply the next production request
after its cooldown elapses.

- **Trip:** after `HEALTH_FAILURE_THRESHOLD` (3) consecutive failures the model goes **unhealthy**
  and is **skipped — no call** — for `HEALTH_BASE_COOLDOWN` (30s).
- **Backoff:** each re-trip (a failed probe) **doubles** the cooldown, capped at
  `HEALTH_MAX_COOLDOWN` (30m).
- **Recover:** a successful probe — or an admin reset (`POST /v1/admin/model-health/reset`) —
  returns it to healthy and resets the counters.
- **What counts:** any chain-advancing provider error **and** any schema-invalid output. A
  `400/422` content error does not.
- **Scope:** production predicts only. Live state is **in-process and resets on restart**; every
  transition (`failure` / `tripped` / `recovered` / `manual_reset`) is persisted to
  `model_health_events` via an async writer. `HEALTH_BREAKER_ENABLED=false` turns it off (every
  model tried every time).

### `RunAll`, cache, and pricing

`RunAll` is the playground fan-out: goroutine per model, buffered channel, results in arrival
order (fastest first). The prediction cache (§9 / `internal/cache`) is consulted inside the walk;
a clean success fills one entry keyed on the serving model at the task's TTL. Pricing
(`pricing.go` + `pricing.json`): `CalculateCost(model, in, out)` rounds to 6 decimals, unknown
models cost 0, and `PricingTable()` backs `GET /pricing` so the frontends estimate with identical
rates.

---

## 8. Per-task limits — the TWO 413 sources

This is a subtlety worth pinning down because it confuses people: a predict can return **413** for
**two different, independent reasons**, from two different subsystems. Knowing which one fired tells
you whether retrying could ever help.

### Source A — the rate limiter (`internal/ratelimit`, production only)

A per-task **rolling-window** limiter sits in front of production predicts, independent of the
daily budget gate. Each task gets its own window (default 1m) with its own lock — tasks never
serialize against each other. Three gates:

1. **Per-request input cap** (`RATE_MAX_INPUT_TOKENS`): an *estimate* = chars/`RATE_CHARS_PER_TOKEN`
   + `RATE_TOKENS_PER_IMAGE` per image; over the cap → **413** (`input_too_large`), no
   `Retry-After`.
2. **Request-rate cap** (`RATE_MAX_REQUESTS`/window) → **429** with `Retry-After` = time to window
   refill.
3. **Token budget** (`RATE_MAX_TOKENS`/window) → **429** with `Retry-After`.

It uses **reserve-then-reconcile**: `executePrediction` reserves the request's estimate up front,
then reconciles to the tokens actually consumed (input+output across *every* attempt, incl.
failed/fallback ones). A request that ran always counts toward the request gate. A `0` for any
`Max…` disables that gate; `RATE_LIMIT_ENABLED=false` disables the limiter. **Test/shadow runs are
not rate-limited.**

### Source B — the per-task input-size caps (predict AND Studio test)

Separately, each task may set hard ceilings: `MaxPromptChars` (rendered system+user prompt),
`MaxImageKB` (per image), `MaxImages` (count) — `0` = no limit, validated non-negative.
`enforceInputLimits` in `executePrediction` rejects an over-limit request with **413**
(`input_too_large`).

### Why two

| | Rate limiter input cap | Per-task size caps |
|---|---|---|
| Where | `internal/ratelimit` (estimate, token-based) | `enforceInputLimits` (exact, char/KB/count) |
| Scope | **production predicts only** | **production predicts AND Studio test runs** |
| Tunable by | global `RATE_*` env | per-task config fields |
| Status | 413 (input cap) / 429 (rate, token) | 413 only |

Both 413s mean "the input is too big — retrying the same input won't help, shrink it." The
distinction: the rate-limiter cap is a coarse global estimate that's off in production; the per-task
caps are exact, author-set guardrails enforced even on test runs (so an operator can't sneak an
oversized input past them via the Studio Test panel).

---

## 9. Database

`internal/db`. The schema is **identical across backends** (selected by `DB_DRIVER`);
`created_at` is stored as TEXT (`YYYY-MM-DD HH:MM:SS`) in both so scans/`substr()`/ordering match.

### Pluggable backend + dialect seam

`db.Open(driver, sqlitePath, postgresDSN)`:

- **`sqlite`** (default, `modernc.org/sqlite`, pure-Go): WAL, `busy_timeout=5000`,
  `foreign_keys=ON`, `SetMaxOpenConns(1)` (single writer — the reason SQLite never throws "database
  is locked" here). Fully tested.
- **`postgres`** (`jackc/pgx/v5`): a real pool (`MaxOpenConns=20`). Implemented but **pending
  validation against a live instance** — `docs/DEPLOY.md` lists the two spots to confirm (numeric→
  float dashboard scans, and `shadow_reports.id` which uses `LastInsertId`, unsupported by pgx →
  switch to `RETURNING id` if needed).

The **dialect seam** (`dialect.go`) sets a process-global `activeDriver` in `Open`. All hand-written
queries use portable `?` placeholders and route through `exec`/`query`/`queryRow` (and exported
`Exec`/`Query`/`QueryRow`/`Rebind` for the tasks store + shadow handler), which rewrite `?`→`$n` on
Postgres. Diverging fragments hide behind `nowExpr()` / `todayExpr()` / `daysAgoExpr(n)` /
`ciLike(col)`. So one set of queries in `queries.go` serves both backends — keep new SQL portable.

### Migrations — additive, guarded, idempotent

`Migrate` dispatches on the driver. SQLite uses `CREATE TABLE IF NOT EXISTS` + **guarded
`ALTER TABLE ADD COLUMN`** (ignore "duplicate column") so existing dev DBs upgrade in place;
Postgres applies an idempotent schema in `schema_postgres.go` (`… IF NOT EXISTS`, native types).
Migrations auto-run on boot **in dev only**; prod runs `cmd/migrate` out-of-band. Follow this
additive pattern for future schema changes — never a destructive migration.

### The out-of-band tools

- **`cmd/migrate`** — applies the schema for the configured `DB_DRIVER`. The prod migration path.
- **`cmd/bootstrap -issue-admin`** — first-run: generates a `JWT_SECRET`, runs `config.Validate()`,
  migrates, locks down a SQLite file's permissions, and mints a break-glass admin token.

### Tables

| Table | Purpose | Key columns |
|---|---|---|
| `tasks` | registry / config | id (PK), name, description, input_schema, output_schema, prompt_template, system_prompt, prompt_version, model, fallback_models (JSON), temperature, max_tokens, daily_budget_usd, **max_prompt_chars, max_image_kb, max_images** (the 3 new INTEGER NOT NULL DEFAULT 0 cols), active, cache_enabled, cache_ttl_seconds, created_at, updated_at |
| `prompt_versions` | prompt history & drafts | id, task_id, version, prompt_template, system_prompt, note, created_by, created_at · unique (task_id, version) |
| `runs` | the served answer — one row per model call (predict, /run, test, cache hits) | run_id, session_id, prompt, system_prompt, **image** (JSON array of data URLs/URLs; NULL for text-only; legacy single-string still parses), model, response, latency_ms, input/output/total_tokens, cost_usd, success, error, user_id, user_email, task_id, prompt_version, provider, fallback_used, cache_hit, is_test, created_at · indexed run_id, session_id, user_id, task_id |
| `gateway_attempts` | full fallback-walk trace — **one row per model touched** | id, run_id, task_id, seq (0=primary), model, provider, outcome (success\|error\|schema_invalid\|skipped_unhealthy\|cache_hit), fallback_used, fallback_reason, response, error, http_status, infra_failure, retry_count, latency_ms, usage, cost_usd, is_test, created_at · indexed run_id, task_id, model, outcome, created_at |
| `feedback` | star ratings | (run_id, model, user_id) unique → rating 1–5 (upsert) |
| `shadow_reports` | accuracy comparisons | id, task_id, created_by, items, match_rate, avg/p95_latency_ms, total_cost_usd, details (JSON), created_at |
| `model_health_events` | per-(task, model) breaker history | id, task_id, model, provider, event (failure\|tripped\|recovered\|manual_reset), reason, consecutive_failures, cooldown_ms, state, created_at · indexed (task_id, model) + created_at |

The relationship to remember: **`runs` holds the single answer served; `gateway_attempts` holds
everything behind it** (every model the walk touched, including skips, cache hits, and
schema-invalid responses). `ListGatewayAttempts(run_id)` attaches the whole trace to the admin
run-detail response.

### The three async writers

`RunWriter` (`runs`, 1024 buffer), `GatewayAttemptWriter` (`gateway_attempts`, **4096** buffer
since one run emits several attempts), and `HealthEventWriter` (`model_health_events`) are all the
same pattern: a buffered channel drained by one goroutine; handlers submit without blocking; a full
buffer **drops + counts** the row rather than blocking a prediction. All `Close()` are idempotent
and flushed by graceful shutdown. This is the mechanism behind the "observability never fails a
response" invariant (§12). Handlers go through `Handler.insertRun`, which uses the writer when set
and falls back to a synchronous insert when nil (so tests stay deterministic).

### Snapshot pagination + filters

The admin runs list (`ListAllRuns(filter, page, pageSize)`) returns **lightweight** rows —
truncated prompt preview + image **count**, never the bytes. `RunFilter` carries two notable
fields:

- **`MaxID` / `anchor_id`** — a point-in-time snapshot **anchor**: only rows with `id <= MaxID` are
  returned, so rows inserted while a user pages never shift the slices. `0` resolves to the current
  `MAX(id)` and the anchor actually used is echoed back to pin the next page.
- **`HasTask`** — when true, excludes playground/compare runs (so the History view shows only
  product-task runs).

Plus filters on task / model / user-email / status / production-vs-test and a prompt-text search.
Image columns serialize via `imagesToColumn` / `ParseImagesColumn` (JSON array, back-compatible
with legacy single-string rows), so one or many images share one column.

---

## 10. HTTP API

`router.go` — a chi router with RequestID / Logger / Recoverer middleware and **CORS restricted to
`ALLOWED_ORIGINS`** with `AllowCredentials: true` and methods `GET/POST/PUT/DELETE/OPTIONS`. All
errors are `{"detail": "<message>"}`; the HTTP status carries the meaning. Every non-public route
requires a valid session (`RequireAuth`); `/v1/tasks/*` additionally runs `RequirePermission`
(§5); `/v1/admin/*` runs `RequireAdmin`.

### Public routes

| Method · Path | Returns |
|---|---|
| `GET /health` | liveness `{status:"ok", models_available:[…]}` |
| `GET /ready` | readiness — pings DB → `{status:"ready"}` (200) or `{status:"not_ready",reason}` (503) |
| `GET /auth/demo-users` *(demo mode)* | `{users:[…]}` |
| `POST /auth/login {user_id}` *(demo mode)* | `{user:{…}}` + cookie · 401 unknown · 422 missing |
| `GET /auth/sso/login` · `GET /auth/sso/callback` *(sso mode)* | OIDC redirect/callback (501 until wired) |

### Authed `/v1` and playground (any authenticated principal except where RBAC-gated)

| Method · Path | Perm | Notes |
|---|---|---|
| `GET /auth/me` · `POST /auth/logout` | auth | session bootstrap / logout |
| `GET /pricing` | auth | pricing table for client-side estimation |
| `POST /run` | auth | playground fan-out across N models; rows stamped `task_id=playground` |
| `GET/DELETE /sessions` · `GET /sessions/{id}` · `GET /sessions/{id}/leaderboard` | auth | playground history (user-scoped); leaderboard averages manual ★ per model via a `run_id`-subquery so each rating counts once |
| `POST /feedback {run_id, model, rating}` | auth | 1–5★ upsert · 422 out of range |
| `GET /dashboard` | auth | per-user totals + by_task + by_model + daily |
| `POST /v1/tasks` | write | **how tasks are authored** (no YAML); 422 on bad slug/model/schema/template |
| `GET /v1/tasks` · `GET /v1/tasks/{id}` | read | prompts blanked without `view_prompt` |
| `PUT /v1/tasks/{id}` | write | **merge semantics** — absent fields unchanged; `"input_schema":null` clears it; prompt change bumps version |
| `DELETE /v1/tasks/{id}` | delete | **admin-only**; removes task + version history (runs kept); 409 for `playground` |
| `POST /v1/tasks/{id}/predict {inputs}` | predict | **the product endpoint** (pipeline below) |
| `GET /v1/tasks/runs/{run_id}` | read | poll a run |
| `GET/POST /v1/tasks/{id}/versions` | read / write | history / save a draft (not activated) |
| `DELETE /v1/tasks/{id}/versions/{version}` | delete | admin-only; 409 on the active version |
| `POST /v1/tasks/{id}/deploy {version}` | deploy | make a version live |
| `POST /v1/tasks/{id}/test {inputs, version?, model?}` | write | runs the pipeline as `is_test`; **bypasses cache**; size caps still enforced (413) |
| `GET /v1/tasks/{id}/stats?days=N` | read | task totals + daily series |
| `POST /v1/shadow/compare {task_id, items[≤200]}` | write | field-level match-rate + latency p50/p95; persists a report |
| `GET /v1/shadow/reports?task_id=` | read | last 50 reports |

**Route-order note:** `/v1/tasks/runs/{run_id}` is registered *before* `/v1/tasks/{task_id}` so
`"runs"` never matches as a task id; likewise `/v1/admin/runs/models` before
`/v1/admin/runs/{run_id}`.

### Admin endpoints (`RequireAdmin` — the `admin` role, not a capability; 403 otherwise)

| Method · Path | Notes |
|---|---|
| `GET /v1/admin/runs` | params: `page, page_size (≤100), task_id, model, user_email, q (prompt substring), status (success\|error), type (production\|test), has_task (true → exclude playground/compare), anchor_id (snapshot pin — echoed back)`. Lightweight rows: truncated preview + image **count**, never bytes |
| `GET /v1/admin/runs/models` | distinct models in `runs` (filter dropdown) |
| `GET /v1/admin/runs/{run_id}` | full detail: prompt, system prompt, image data URLs, per-model results, **+ the gateway-attempt trace** |
| `GET /v1/admin/model-health` | live per-(task, model) circuit states |
| `GET /v1/admin/model-health/events?task_id&model&page&page_size` | persisted health/fallback events (newest first) |
| `POST /v1/admin/model-health/reset {task_id, model}` | force a model back to healthy (records `manual_reset`); 404 if never tracked |

### The Predict pipeline, step by step

`task_handlers.go` + the shared `executePrediction` in `predict_core.go` (reused by Test and
Shadow, differing only by options):

1. **Resolve task** — 404 unknown, 409 inactive. From the in-memory config cache, **no DB read**.
2. **Budget gate** — 429 + `Retry-After` (to UTC midnight) when daily spend ≥ cap; warn-log at 80%;
   cap 0 = exempt. Spend comes from the cached in-memory view (`budget_cache.go`: DB SUM refresh
   ≤ every 5s + per-prediction local increments), so no per-request aggregate query.
3. **Rate limiter** (production only) — reserve the request's estimate; over the input cap → 413,
   over rate/token → 429 + `Retry-After`.
4. **`ValidateInput`** vs `input_schema` → 422.
5. **`RenderPrompt`** + `collectImages` (attaches schema-typed image fields as vision blocks).
6. **`enforceInputLimits`** — the per-task size caps → 413 (Source B, §8; also runs on test).
7. **`CallWithFallbackOpts`** over `[model, …fallbacks]` with the per-model cache lookup, the health
   gate, and the output validator (production only). A provider error **or** schema-invalid output
   advances the chain in the same request; a clean schema-valid success stops.
8. **Cache fill** — only on a clean success, keyed on the serving model.
9. **Reconcile** the rate-limiter reservation to actual tokens.
10. **Async run write** (+ images + the gateway trace); update spend.
11. **Respond** — `X-Platform-Degraded: true` when a fallback served it or the chain failed.

### The valid-only response shape

```json
{ "task_run_id": "...", "task_id": "...", "prompt_version": 1,
  "model": "llama-groq", "provider": "groq",
  "output": {…},            // parsed JSON ONLY when output_schema validates
  "output_valid": true,     // true/false with a schema; null when the task has no output schema
  "raw_response": "…", "error": null,
  "fallback_used": false,   // served by a non-primary model
  "cached": false,          // served from the prediction cache (zero cost)
  "usage": {"input_tokens":172,"output_tokens":53,"total_tokens":225,"cost_usd":1.3e-05},
  "latency_ms": 696,        // the winning model's call time only
  "gateway_latency_ms": 712 }
```

`gateway_latency_ms` is the **end-to-end platform wall-clock** — input validation + the whole
fallback walk (including failed attempts) + output validation + cache work — and is always
`≥ latency_ms`; the gap is the gateway's own overhead plus losing models. If *every* model returns
schema-invalid output, the last is returned with `output_valid:false` at **200** (not an error); an
upstream chain failure or all-models-unhealthy is **502**. Both 200 and 502 set
`X-Platform-Degraded` when degraded.

### Gateway attempt tracing

Behind every predict, the fallback walk writes one `gateway_attempts` row per model it touched
(success / error / schema_invalid / skipped_unhealthy / cache_hit), so the admin run-detail drawer
shows the whole fallback story for one run, not just the winner.

---

## 11. Running & testing

```bash
# Server (loads .env; needs ≥1 provider key) — listens on :8000
go run ./cmd/server

# Mint a service token (client portal / CIS). -role defaults to client (read+predict).
go run ./cmd/issue-token -sub svc:cis -email cis@svc.local -role client -ttl 8760h

# First-run / deploy helpers (see docs/DEPLOY.md)
go run ./cmd/migrate                 # apply schema for the configured DB_DRIVER
go run ./cmd/bootstrap -issue-admin  # gen secret, validate, migrate, lock DB, mint break-glass admin

# Verify
go build ./... && go vet ./... && go test ./...
go test -race ./...   # for the concurrency paths: fallback walk, health tracker, rate limiter, writers
```

### Test inventory

Black-box HTTP + DB tests live in `tests/` (httptest over an in-memory SQLite; fake providers
injected via `newTestServerWithClients`), plus white-box tests under `internal/llm/`,
`internal/health/`, and `internal/cache/`. Helpers: `authReq(...)` attaches a `u-admin` Bearer
token signed with the test secret; `roleReq(t, role, …)` mints any role.

- `tests/handlers_test.go` — endpoint shapes, auth-required, sessions CRUD.
- `tests/auth_feedback_test.go` — demo store, token round-trip, login, feedback + dashboard, per-user isolation.
- `tests/rbac_test.go` — the role × permission matrix + no-role-claim → admin default.
- `tests/prompt_redaction_test.go` — client gets prompts blanked, admin sees them.
- `tests/delete_version_test.go` — version delete is admin-only, 409 on the active version.
- `tests/schema_update_test.go` — PUT merge semantics incl. `"input_schema":null` clearing.
- `tests/tasks_test.go` — registry CRUD, version-bump rules, schema/template validation, fenced output.
- `tests/predict_test.go` — full pipeline against a fake provider: attribution, usage, parsed output, 422s, invalid-output flagging, 404, playground stamping, single + multiple image forward/store.
- `tests/db_test.go` — run insert/query/delete, pagination, ordering (user-scoped).
- `tests/phase1_test.go` — budget 429 + exemption, the version lifecycle, shadow exact numbers, per-task stats, RunWriter flush/drop.
- `tests/cache_predict_test.go` — cache hit serves with zero cost + `cache_hit` row, key sensitivity, opt-in required, test bypass, failures never cached.
- `tests/admin_runs_test.go` — admin history list + filters, run detail, unknown = 404, non-admin = 403.
- `tests/model_health_test.go` — schema-invalid falls back in-request; repeated failures trip + are skipped + admin reset restores; unknown reset = 404, non-admin = 403.
- `tests/gateway_attempts_test.go` — the per-attempt trace.
- `tests/ratelimit_predict_test.go` — the rate-limiter gates (413 / 429).
- `tests/request_validation_test.go` — embedded request-body schema validation (422).
- `internal/llm/runner_test.go` / `provider_test.go` — registry↔pricing.json parity, retry + error classification, wire format.
- `internal/llm/breaker_test.go` / `attempts_test.go` / `multimodal_test.go` — fallback advance/stop rules, attempt recording, multimodal marshal.
- `internal/health/tracker_test.go` — the breaker state machine (fake clock): trip, skip-then-probe, backoff + cap, manual reset, disabled, nil safety.
- `internal/cache/cache_test.go` — key determinism/sensitivity, Redis round-trip + TTL via miniredis, outage = miss.

---

## 12. Design decisions & invariants (do not regress)

The backend "do not regress" rules. Touching these without preserving the invariant is how the
platform rots.

1. **Pure Go, single binary, no CGo.** No LiteLLM/Langfuse/Python in the request path. SQLite is
   pure-Go; the DB engine is a runtime flag, not a build tag.
2. **One provider, two wirings.** Every model is reached over the OpenAI-compatible wire; adding a
   vendor means a new registry key + pricing row, not new provider code. Keep it that way.
3. **The Task is the unit of everything** — cost, versioning, RBAC, budgets, cache, future eval.
   Every run row must carry `task_id`.
4. **The platform never owns caller business logic** — no "if confidence < X route to QC" inside
   the platform.
5. **Preserve the seams:** `users.Store` (identity / SSO swap), `llm.Provider` (model backends),
   `internal/db` queries + `dialect.go` (storage engine — keep new SQL portable, route through the
   dialect helpers), `pricing.json` (rates).
6. **Playground ≠ product API.** Compare talks to `/run` / `/sessions`; services talk to
   `/v1/tasks/*`. Multi-turn chat semantics must not leak into the product API.
7. **The prediction hot path does no synchronous DB work.** Task config from the in-memory store
   cache, budget from the cached spend view, run rows via the async writer, cache in Redis/memory,
   health gate from the in-memory tracker (events persisted async). New per-request features must
   read from memory and write async.
8. **Observability writes never fail a response.** A full async-writer buffer drops + counts a row
   rather than blocking. Predictions distinguish "model failed" (502) from "output didn't validate"
   (200 + `output_valid:false`).
9. **Authorize from the token alone.** The role rides inside the signed JWT; no per-request
   identity-store lookup on the hot path. Clients never see prompts (redaction).
10. **Health is per-(task, model), live state in-memory, history persisted.** The single breaker.
    Live state resets on restart; only `model_health_events` is durable. Admin observability routes
    are gated on the `admin` *role* (`RequireAdmin`), not a task capability, because they expose
    cross-tenant data.
11. **Prod refuses to boot misconfigured.** `config.Validate()` (gated by `APP_ENV=prod`) is the
    single place insecure defaults are rejected — never weaken it to "log and continue." New
    secret/origin/secure-cookie requirements belong there.
12. **Migrations are additive, idempotent, and per-dialect**, auto-run in dev only, applied
    out-of-band (`cmd/migrate`) in prod. Schema migrates; demo-era data is throwaway.
13. **Frontend pricing and types always follow the backend** (`/pricing`, mirrored response types).

---

*Companion to `docs/repo_work_doc.md` (reference manual) and `docs/DEPLOY.md` (deploy runbook).
When the model registry, the endpoint set, RBAC, or the schema change, update this guide and the
work doc together.*
