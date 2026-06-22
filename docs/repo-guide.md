# LLM Platform — Complete Repository Guide

**Last updated:** 2026-06-22
**Scope:** `llm_platform_go` (backend) + `llm_platform_frontend` (operator Studio UI) +
`llm_platform_client` (consumer-facing client portal). The Python `llm_platform_v0` is
superseded and excluded.

> **Deployment readiness (2026-06-19):** the backend is being prepared to deploy as its
> own repo, separate from the frontend, on subdomains of one parent domain. That work
> added: a **pluggable database** (SQLite *or* Postgres via `DB_DRIVER`), an `APP_ENV=prod`
> config gate (`config.Validate()` refuses insecure boots), an `AUTH_MODE` switch (demo
> login vs an SSO scaffold), `cmd/migrate` + `cmd/bootstrap`, graceful shutdown + HTTP
> timeouts, and a `/ready` probe. See `llm_platform_go/docs/DEPLOY.md`. RBAC is **two roles**
> today — `admin` and `client` (see §3.3).

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
- Task registry (DB-backed, authored via the Studio API/UI), task-keyed prediction endpoint with schema enforcement
- **Multimodal input:** a task may accept one image (`image`) or many (`images[]`) — base64 data URLs or image URLs — attached to vision models as OpenAI multimodal content blocks (the live `attribute-extraction` task uses this)
- **Resilient routing:** fallback chains + a **per-(task, model)** health breaker that skips a model for one task after repeated failures (incl. schema-invalid output) with exponential-backoff cooldown + admin reset, `X-Platform-Degraded` contract
- **Admin observability:** a cross-tenant **prompt-history** viewer (every user's runs, filterable + paginated) and a **model-health** console (live circuit states + persisted fallback/health events), both admin-only
- **Budget enforcement:** per-task daily caps → 429 + `Retry-After` (0 = exempt)
- **Per-task rate limiting:** a per-task rolling window caps requests, total tokens, and per-request input size → 413 (oversized input) / 429 (rate or token budget) with `Retry-After`; reserve-then-reconcile against tokens actually consumed (`internal/ratelimit`, see §3.9)
- **Service auth + RBAC:** `cmd/issue-token` mints long-lived `svc:*` Bearer tokens (CIS-ready) with a role; role-based authorization enforced at the gateway. **Two roles today — `admin` and `client`** (see §3.3)
- **Prompt registry:** first-class versions (draft → test → deploy), auto-history, Studio UI (Tasks page)
- **Shadow harness:** `/v1/shadow/compare` measures field-level match rate + latency p50/p95 vs labelled expectations
- **Observability:** async buffered **run writer** (one row per call) **and gateway-attempt writer** (one row per model the fallback walk touched — full per-attempt trace incl. fallbacks, skips, cache hits, and schema-invalid responses), neither blocking the hot path; per-task stats endpoint (see §3.7)
- **Pluggable database:** SQLite (default) **or** Postgres, selected by `DB_DRIVER`; the query layer is dialect-aware (`internal/db/dialect.go`). Postgres is implemented but pending validation against a live instance (see §3.7)
- **Prediction cache (Redis):** per-task opt-in exact-match cache — key pins prompt version + rendered prompt + model + params + schema; hits are zero-cost `cached:true` responses (pulled forward from Phase 3.3)
- Demo SSO + swappable user store, JWT cookie auth
- Multi-model playground (Compare UI), pre-call cost estimation, 1–5★ feedback
- **Per-session model leaderboard:** `GET /sessions/{id}/leaderboard` averages the manual
  1–5★ ratings per model within a Compare session (Leaderboard modal) — the manual
  precursor to the automated eval layer
- Per-task + per-model + daily cost dashboard
- **Client portal** (`llm_platform_client`, :5174): consumer-facing catalog + live Try-it
  predict panel, authenticated as a baked-in `svc:demo-client` service token (no login)

Planning / ops docs: `docs/gap-analysis-roadmap.md` (gap analysis vs. the design doc),
`docs/phase-workflow.md` (execution plan for Phases 1–4),
`docs/deployment-guide.md` (every dev/demo assumption to swap before a real
deployment, mapped to the seam that contains it), and
`llm_platform_go/docs/DEPLOY.md` (the concrete split-repo / config / first-run runbook).

---

## 2. Repo Layout

```
llm_platform/
├── dev.sh / dev.yaml              # local dev orchestrator: runs all 3 servers detached
│                                  #   (./dev.sh restart|start|stop|status|logs); pids+logs in .dev/
├── docs/                          # planning + this guide
├── llm_platform_go/               # Go backend (single binary)
│   ├── cmd/server/main.go         # boot sequence (validate → open DB → serve; graceful shutdown)
│   ├── cmd/issue-token/main.go    # mint long-lived svc:* Bearer tokens (-role client|admin)
│   ├── cmd/migrate/main.go        # apply DB schema out-of-band (prod doesn't auto-migrate)
│   ├── cmd/bootstrap/main.go      # first-run: gen JWT secret, validate, migrate, lock DB, mint break-glass admin
│   ├── docs/DEPLOY.md             # split-repo / cross-origin / first-run runbook
│   ├── internal/
│   │   ├── api/                   # HTTP layer: router, middleware, handlers, SSO scaffold
│   │   ├── auth/                  # JWT issue/parse, cookie management, RBAC (admin/client)
│   │   ├── cache/                 # prediction cache: Redis / memory behind Cache iface
│   │   ├── config/                # env-driven config + Validate() (prod safety gate)
│   │   ├── db/                    # open/migrate (sqlite|postgres), dialect seam, SQL queries, async writers
│   │   ├── health/                # per-(task, model) circuit-breaker tracker
│   │   ├── llm/                   # provider clients, model routing, pricing, fallback walk
│   │   ├── ratelimit/             # per-task request/token rolling-window limiter
│   │   ├── schema/               # embedded request-body JSON Schemas (422 before handler)
│   │   ├── tasks/                 # Task registry: model, store, validate, render, seed
│   │   ├── types/                 # request/response contracts + RunRow + GatewayAttempt
│   │   └── users/                 # identity seam: Store interface + DemoStore
│   ├── tests/                     # black-box HTTP + DB tests
│   ├── pricing.json               # per-model $/1M token rates
│   ├── .env.example               # annotated config template (copy to .env)
│   └── .env                       # local secrets (gitignored; never committed)
├── llm_platform_frontend/         # React 19 + Vite + Tailwind 4 + Meesho merlin-ui
│   └── src/
│       ├── api/client.ts          # typed fetch wrapper (cookie credentials)
│       ├── auth/                  # AuthContext provider + useAuth hook
│       ├── components/            # AppShell, LoginScreen, Sidebar, ChatArea, ModelColumn,
│       │                          #   ChatInput, MessageBubble, StarRating, LeaderboardModal,
│       │                          #   SchemaEditor, VersionHistory, SystemPromptBar
│       ├── hooks/                 # useChat (Compare state), useSessions
│       ├── pages/                 # ComparePage, TasksPage, VersionsPage, EstimatePage, DashboardPage,
│       │                          #   AdminRunsPage (prompt history), ModelHealthPage (admin-only)
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
3. **`config.Validate()`** — in `APP_ENV=prod` this **hard-fails the boot** on any insecure
   default: dev/blank `JWT_SECRET`, `AUTH_MODE=demo`, missing `ALLOWED_ORIGINS`,
   `COOKIE_SECURE=false`, or a relative `DB_PATH`/`PRICING_PATH`. In dev it's a no-op
   beyond driver sanity. (See §3.2.)
4. `llm.LoadPricing(pricing.json)` — cost table into memory.
5. `db.Open(DB_DRIVER, DB_PATH, DB_DSN)` — `sqlite` (WAL, single writer) **or** `postgres`
   (pgx, real pool). **Migrations:** auto-run here in dev only; in prod they're applied
   out-of-band via `cmd/migrate` (so a rolling deploy never blocks on / races a schema change).
6. `llm.BuildClients` — one `Provider` per backend. There are **two**: `Groq` (direct
   API) and `Meesho` (the internal bifrost gateway, OpenAI-compatible with `x-bf-vk`
   auth). Every non-Groq model is served through the Meesho gateway.
7. `tasks.NewStore` → `tasks.SeedPlayground` + `tasks.SeedAttributeExtraction` — seeds the
   built-in `playground` task and the live `attribute-extraction` task (idempotent; never
   overwrites). Other product tasks live in the DB, authored at runtime via the Studio.
8. `users.NewDemoStore()` — **the identity swap seam** (see §3.4).
9. `db.NewRunWriter` + `db.NewGatewayAttemptWriter` — async observability writers (run rows
   + per-attempt gateway trace); `Close()` flushes each on shutdown. See §3.7.
10. `db.NewHealthEventWriter` + `health.NewTracker(...)` — the per-(task, model) health
    breaker (thresholds from config; transitions persisted via the async health writer). See §3.6.
11. `ratelimit.New(...)` — per-task request/token rolling-window limiter (§3.9).
12. Prediction cache by `CACHE_BACKEND`: Redis (boot fails on bad addr) / in-process
    memory / off.
13. `api.NewRouter(RouterDeps{...})` → an `http.Server` with read/write/idle **timeouts**;
    `ListenAndServe` in a goroutine. **SIGINT/SIGTERM → graceful shutdown**: stop accepting,
    drain in-flight requests, then close the async writers so buffered rows flush.

### 3.2 Configuration — `internal/config`

| Env var | Default | Purpose |
|---|---|---|
| `APP_ENV` | `dev` | `prod` turns on `config.Validate()` (rejects insecure defaults) and disables inline migrations |
| `AUTH_MODE` | `demo` | `demo` = passwordless pick-a-user login (dev); `sso` = IdP redirect/callback, demo login unregistered |
| `DB_DRIVER` | `sqlite` | `sqlite` (location = `DB_PATH`) or `postgres` (location = `DB_DSN`) |
| `DB_DSN` | — | Postgres connection string, e.g. `postgres://user:pass@host:5432/db?sslmode=require` (required when `DB_DRIVER=postgres`) |
| `GROQ_API_KEY` | — | Groq API key (direct); at least one of this or `MEESHO_GATEWAY_VK` is required at boot |
| `GROQ_BASE_URL` | `https://api.groq.com/openai/v1` | Groq base URL — override for a proxy/self-hosted endpoint |
| `MEESHO_GATEWAY_VK` | — | Virtual key for the Meesho internal LLM gateway (sent as the `x-bf-vk` header); serves every non-Groq model (GPT-4o, Gemini 2.5, Claude) |
| `MEESHO_GATEWAY_BASE_URL` | `http://llm-gateway.prd.meesho.int/v1` | Meesho gateway base URL (OpenAI-compatible `/chat/completions`) |
| `DB_PATH` | `./llm_platform.db` | SQLite file (used only when `DB_DRIVER=sqlite`; must be absolute in prod) |
| `PORT` | `8000` | HTTP port |
| `PRICING_PATH` | `./pricing.json` | Cost table |
| `JWT_SECRET` | dev placeholder | Signs session tokens — **set a real one outside dev** |
| `AUTH_COOKIE_NAME` | `llm_platform_token` | Session cookie |
| `AUTH_ISSUER` | `llm-platform-demo` | JWT `iss` |
| `TOKEN_EXPIRY` | `12h` | Session lifetime |
| `COOKIE_DOMAIN` / `COOKIE_SECURE` | empty / false | Cookie scoping (set `COOKIE_DOMAIN=.example.com` + `COOKIE_SECURE=true` for the subdomain topology; Secure required in prod) |
| `ALLOWED_ORIGINS` | `http://localhost:5173` (dev fallback) | CORS allowlist, comma-separated (credentials mode); **required in prod** — the frontend origin(s) |
| `OIDC_ISSUER` / `OIDC_CLIENT_ID` / `OIDC_CLIENT_SECRET` / `OIDC_REDIRECT_URL` / `OIDC_POST_LOGIN_URL` | — | SSO config, read only when `AUTH_MODE=sso`; consumed by the IdP handshake scaffold in `auth_sso_handlers.go` (§3.3) |
| `REDIS_ADDR` / `REDIS_PASSWORD` / `REDIS_DB` | — | Prediction cache backend; addr set → Redis (boot fails on bad addr) |
| `CACHE_BACKEND` | derived | `redis` \| `memory` (in-process, dev only) \| `off` (default when no `REDIS_ADDR`) |
| `HEALTH_BREAKER_ENABLED` | `true` | Per-(task, model) health breaker on/off (off = every model is tried every time) |
| `HEALTH_FAILURE_THRESHOLD` | `3` | Consecutive failures (provider error OR schema-invalid output) before a model is tripped unhealthy for a task |
| `HEALTH_BASE_COOLDOWN` | `30s` | First unhealthy cooldown window; doubles on each re-trip |
| `HEALTH_MAX_COOLDOWN` | `30m` | Cap for the backed-off cooldown |
| `RATE_LIMIT_ENABLED` | `true` | Per-task request/token rate limiter on/off (§3.9) |
| `RATE_WINDOW` | `1m` | Rolling window per task |
| `RATE_MAX_REQUESTS` | `600` | Max accepted requests per task per window (0 = unlimited) |
| `RATE_MAX_TOKENS` | `200000` | Max tokens consumed per task per window (0 = unlimited) |
| `RATE_MAX_INPUT_TOKENS` | `16000` | Max estimated input tokens for one request → 413 (0 = unlimited) |
| `RATE_CHARS_PER_TOKEN` | `4` | Token estimation divisor |
| `RATE_TOKENS_PER_IMAGE` | `1000` | Flat token estimate per attached image |

### 3.3 Auth — `internal/auth`

HS256 JWT with claims `{sub, email, name, iss, iat, exp}`. Token is accepted from either
the `Authorization: Bearer` header **or** the HttpOnly session cookie (`TokenFromRequest`).
`RequireAuth` middleware (in `internal/api/middleware.go`) parses/validates and puts an
`auth.User{Subject, Email, Name}` on the request context; handlers read it via
`auth.FromContext` / the `requireUser` helper. Cookie helpers: `SetAuthCookie`,
`ClearAuthCookie`.

The Bearer path means **service-to-service auth already works mechanically** — what's
missing (Phase 1) is a way to mint long-lived service principals distinct from UI sessions.

**Role-based authorization (`internal/auth/rbac.go`).** Two principals exist: the human
**operator** (`admin`), who runs the platform via the Studio and holds every capability, and
the service **client**, a backend that only invokes the product predict API and never sees
prompts. RBAC encodes this as six capabilities — `task:read`, `task:predict`, `task:write`
(create/update/draft/test/shadow), `task:deploy` (the publish gate, deliberately split from
`task:write`), `task:delete` (destructive — deleting a whole task or pruning prompt versions),
and `task:view_prompt` (see the prompt text itself — withheld from clients, who integrate
against the task contract and "never touch prompts" per the PFS) — mapped to two roles:

| Role | read | predict | write | deploy | delete | view_prompt |
|---|---|---|---|---|---|---|
| `admin` | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| `client` | ✓ | ✓ | — | — | — | — |

> The capability set is finer-grained than the two roles (it anticipates future
> creator/approver splits), but only `admin` and `client` are wired today. `write`,
> `deploy`, and `delete` are therefore effectively admin-only.

**Prompt redaction:** a `client` (anyone without `task:view_prompt`) gets task config, schemas,
and its own run outputs, but `GET /v1/tasks`, `GET /v1/tasks/{id}`, and
`GET /v1/tasks/{id}/versions` blank the `prompt_template` / `system_prompt` fields
(`redactedTask` in `handlers.go` copies the cached task so the shared config-cache entry is
never mutated; `ListPromptVersions` blanks version bodies the same way).

The role rides **inside the signed JWT** (`Claims.Role`), so the gateway authorizes from the
token alone — no per-request identity-store lookup (hot path stays DB-free). A token with no
role claim resolves to `admin` (`DefaultRole`, kept for backward compatibility — `issue-token`
always stamps an explicit role). `RequirePermission(perm)` middleware
(`internal/api/middleware.go`) gates each `/v1` route in `router.go` and returns **403** on
denial; the admin observability routes use `RequireAdmin` (gated on the `admin` *role*, not a
capability). Studio playground routes (`/run`, `/sessions`, `/feedback`, `/dashboard`,
`/pricing`, `/auth/me`) stay open to any authenticated user. Demo login stamps the role from
`users.User.Role`; `cmd/issue-token -role` mints service tokens with a role (default `client`,
validated against `KnownRole`).

**Auth mode (`AUTH_MODE`).** `demo` registers the passwordless pick-a-user login
(`/auth/demo-users`, `/auth/login`) for local dev. `sso` unregisters those entirely and instead
registers `/auth/sso/login` + `/auth/sso/callback` — an **OIDC scaffold** in
`auth_sso_handlers.go` that shares the session-cookie tail (`issueSession`) with the demo path.
The IdP handshake itself is a documented `TODO` (returns `501` until the `OIDC_*` env is wired);
the break-glass admin token from `cmd/bootstrap` provides access until then. `config.Validate()`
forbids `AUTH_MODE=demo` in prod.

Frontend mirrors the matrix in `src/auth/permissions.ts` (admin-only today; UI gating only — the
gateway is the source of truth) and the Studio hides/disables edit/deploy/delete actions for
non-admins.

### 3.4 Identity seam — `internal/users`

```go
type Store interface {
    GetByID(ctx, id) (*User, error)  // ErrNotFound when absent
    List(ctx) ([]*User, error)       // demo-SSO login screen only
}
```

`DemoStore` is in-memory, seeded with a single user — `u-admin` (`admin@demo.local`, role
`admin`) — and persists **nothing** (by design — there is no demo data to migrate). `User.Role`
is the field the demo login handler stamps into the session token. **Moving to real SSO =
implement `Store` against the IdP + change one constructor line in `main.go`** (the IdP supplies
the identity and role); the SSO route scaffold (§3.3) is the place the callback resolves a user
and calls `issueSession`. Nothing else in the codebase knows where users come from.

### 3.5 Task registry — `internal/tasks` (the platform core)

**`Task`** (`task.go`): `ID` (slug), `Name`, `Description`, `InputSchema` /
`OutputSchema` (JSON Schema, both optional), `PromptTemplate`, `SystemPrompt`,
`PromptVersion`, `Model`, `FallbackModels` (executed by `CallWithFallback`), `Temperature`
(default 0.2), `MaxTokens` (default 1000), `DailyBudgetUSD` (enforced by the budget gate),
`CacheEnabled` / `CacheTTLSeconds` (per-task prediction-cache opt-in, default TTL 24h),
`MaxPromptChars` (max characters of the rendered system+user prompt sent to the model),
`MaxImageKB` (max size per uploaded image, in KB), `MaxImages` (max number of images per
prediction) — these three are per-task input-size guardrails where `0` = no limit and
`Validate()` rejects negative values —
`Active`. `Validate()` checks slug shape, required fields, **known model routing keys**,
schema compilability, and template parsability — a bad config is rejected at write time,
not call time.

**`Store`** (`store.go` + `versions.go`): SQLite CRUD. `Get` is served from an
**in-memory config cache** (write-invalidated by Create/Update/Deploy, 5s TTL
for out-of-band convergence) so the prediction hot path never reads the DB for
task config; treat returned `*Task` as immutable. `Update` **auto-bumps
`prompt_version`** when `PromptTemplate` or `SystemPrompt` changed (next number =
`max(prompt_versions)+1` so it never collides with drafts) and appends a history row;
non-prompt updates don't. `Delete` (admin-only at the route) removes a task and its
prompt-version history — run rows stay for audit — and refuses the built-in `playground`
task. Version methods: `ListVersions` (active flagged), `GetVersion`, `SaveDraft` (records
without activating), `Deploy` (copies a version into the live config). All SQL is contained
here (Postgres move = this file + `internal/db`).

**Validation** (`validate.go`): `santhosh-tekuri/jsonschema/v6`, compiled schemas cached
by **content hash** (task edits naturally invalidate). `ValidateInput(task, rawJSON)`;
`ValidateOutput(task, modelText)` strips markdown code fences (`StripCodeFences`), parses,
validates, returns the cleaned JSON.

**Rendering** (`render.go`): Go `text/template` with `missingkey=error`. Fields declared
in the input schema but absent from the request are pre-filled with `""` — so
`{{if .description}}…{{end}}` optional-field guards work, while a template referencing an
**undeclared** key fails loudly. Parsed templates cached by content hash. **Image fields are
exposed to the template as their image *count*, not the raw value** (`imageInputFields` /
`imageValueCount`): a base64 data URL is never inlined into the prompt text (which would bloat
the prompt and the rate-limiter input estimate) — `{{if .image}}` still gates on presence and
`{{.image}}` renders a small number instead of the bytes; the images themselves travel to the
model as `image_url` attachments (§3.6).

**Seeding** (`seed.go`): just `SeedPlayground`, which registers the built-in `playground`
task once and never overwrites it. **There is no file/YAML seeding** — the DB is the single
source of truth for tasks; product tasks are authored, edited, and deleted at runtime through
the Studio (`POST/PUT/DELETE /v1/tasks`). A fresh database starts with only the playground
task.

**Task config example** — `attribute-extraction` (live, created via the Studio):
input `{title*, description, category*, brand, images[]}` → output
`{attributes: {string: string}, confidence: 0..1}`, model `gemini-2.5-flash` (vision),
fallback `[gpt-4o-mini]` (vision), budget $50/day, `max_tokens: 2048` (Gemini 2.5 Flash
spends hidden "thinking" tokens from the same budget; a tight cap truncates the JSON →
schema-invalid), cache enabled at 24h TTL. The `images` field takes base64
data URLs or image URLs and is attached to the vision model as multimodal content blocks.
It is now **typed** via a JSON-Schema marker — `format:"image"` on the array's items (the
field is recognised as an image input by that marker, not by its property name) — so an image
field can carry any name. The backend still honours the implicit `image`/`images` names as a
legacy fallback. See §3.6 (Multimodal input).

### 3.6 Model layer — `internal/llm`

**`Provider` interface** (`client.go`): `Call(ctx, *chatRequest) (*chatResponse, error)`
over the OpenAI-compatible chat completions wire format. There is a single
implementation, `openAICompatProvider`, which serves **both** backends: Groq's
direct API (`Authorization: Bearer`) and the Meesho gateway (the `x-bf-vk`
virtual-key header instead of Bearer — `openAICompatProvider.authHeader`).
`NewOpenAICompatProvider(baseURL, apiKey)` is exported for vLLM/self-hosted/
test fakes. There is **no native Anthropic/Gemini SDK provider** — the OpenAI,
Gemini, and Claude models are all reached over the gateway's OpenAI-compatible
wire, so no vendor-specific provider code is needed.

**Routing registry** (`runner.go`): friendly key → concrete model ID + provider
attribution + client + flags. It is the single source of truth for routing, and
**all non-Groq models route through the Meesho gateway** (`clientFn: meeshoC`);
only `llama-groq` calls a vendor API directly. Six models are active today
(more vendor variants are present but commented out — because they share the
gateway wire, enabling one is a one-line uncomment, no new provider code):
- **`openai` (via Meesho gateway):** `gpt-4o`, `gpt-4o-mini`
- **`gemini` (via Meesho gateway):** `gemini-2.5-pro`, `gemini-2.5-flash`
  (model ids `vertex/gemini-2.5-pro` / `vertex/gemini-2.5-flash`)
- **`anthropic` (via Meesho gateway):** `claude-sonnet-4-6`
  (model id `anthropic/claude-sonnet-4-6`)
- **`groq` (direct API):** `llama-groq` (`llama-3.3-70b-versatile`)

The bracketed name is the `provider` attribution string recorded on each run. The
`reasoning` flag (for OpenAI reasoning-family models that reject `max_tokens` /
temperature) still exists in `providerConfig`, but no currently-active model sets it.

**Gemini-2.5 thinking support** (`runner.go` + `client.go`): the Gemini 2.5 models spend hidden
"thinking" tokens from the *same* output budget as the answer, so a low user-supplied
`max_tokens` can leave no room for the actual response. `providerConfig.minOutputTokens` sets a
per-model floor (`gemini-2.5-pro`: 8192, `gemini-2.5-flash`: 4096); `CallModel` silently raises
`max_tokens` to that floor when it is lower. And `ChatMessage.UnmarshalJSON` now accepts the
**array-shaped** content (`[{type:text,…}, …]`) that thinking models can return — it concatenates
the `text` parts and **discards thought (non-text) parts** — in addition to the plain-string
content form (so non-thinking models are unaffected).

Helpers: `ProviderName(model)`, `KnownModel(model)`, `AllModels()` (sorted keys —
backs `/health` `models_available`; `DefaultModels` stays the small /run
fan-out default). A registry test enforces every entry has a pricing.json row
and vice versa.

**`CallModel(ctx, clients, model, messages, temp, maxTokens) ModelResult`** — the single
execution path used by both the playground fan-out and `/predict`. Retries up to 3× on
429/500/503 with linear backoff (2s, 4s), respects context cancellation, classifies
errors into human-readable strings (`classifyError`: timeouts, network, auth, rate-limit,
provider-down). Never panics; failures come back as `ModelResult{Success: false, Error}`.

**Error classification** (`failure.go`): `isInfraFailure` (429/5xx/network/timeout — 4xx
config errors and caller cancellation don't count) and `shouldFallback` (infra *plus*
401/403/404 provider-config errors) decide whether a failed call advances the chain. There
is **no** per-provider circuit breaker or background recovery prober — provider failures are
handled entirely by the per-(task, model) health breaker below, discovered in-band.

**Fallback chain** (`fallback.go`): `CallWithFallbackOpts(models []string, …, FallbackOptions)`
tries primary then fallbacks. `FallbackOptions` carries three optional hooks: the per-model
cache `Lookup`, a `HealthGate` (the per-(task, model) breaker — §below), and an
`OutputValidator` (schema check). For each model in priority order it:
- **skips** the model entirely (no call) if the `HealthGate` reports it unhealthy for this task;
- serves a cached answer if `Lookup` hits;
- otherwise calls it live. A **usable** success (passes the validator, or no validator) is
  returned and recorded healthy; a provider error **or a schema-invalid response** is recorded
  against health and **advances the chain to the next model in the same request**; a 400/422
  content error returns immediately (bad input — not the model's fault, not counted).

So a content-level success or 4xx returns immediately, while infra/config failures *and*
schema-invalid output advance the chain. Sets `ModelResult.FallbackUsed` and `.Degraded`
(drives the `X-Platform-Degraded` header). `CallWithFallback`/`CallWithFallbackCached` remain
as thin wrappers (no gate, no validator) for callers that don't need health gating.

**Multimodal input** (`client.go`): `ChatMessage.Images []string` holds image references
(base64 data URLs or image URLs). Its custom `MarshalJSON` emits the plain-string content
form when there are no images (byte-identical to the legacy wire form) and the OpenAI
multimodal array (`[{type:text}, {type:image_url}…]`) when there are — so one or many images
ride the same OpenAI-compatible endpoint. The predict pipeline collects images
(`collectImages` + `imageSchemaFieldNames` in `predict_core.go`) by the **schema marker**: any
input-schema property typed as an image — a string with `format:"image"`, or an array whose
items carry `format:"image"` — under **any** property name. The implicit `image`/`images`
names are still honoured as a **legacy fallback** (so untyped callers and pre-typing tasks keep
working). A property may hold a single string or an array; blank/non-string entries are dropped.
Collected images are folded into the cache key and persisted on the run row.

**Per-(task, model) health breaker** (`internal/health`): distinct from the provider breaker
above — keyed on a specific task's use of a specific model, so a model that misbehaves for one
task is routed around **only for that task**. The `Tracker` (process-wide, mutex-guarded) is
fed by the fallback walk through a task-bound `HealthGate` adapter (`predict_core.go`):
- every failed call — a provider error (network / 401-403 auth / 429 / 5xx / timeout) **or a
  schema-invalid output** — increments a consecutive-failure counter;
- after `HEALTH_FAILURE_THRESHOLD` (default 3) consecutive failures the model is tripped
  **unhealthy** for a cooldown window (`HEALTH_BASE_COOLDOWN`, default 30s) and **skipped** —
  no call is made — until it elapses;
- when the window elapses, one **probing** trial is allowed; a success recovers it to healthy,
  a failure re-trips it with a **doubled** cooldown (×2, capped at `HEALTH_MAX_COOLDOWN`,
  default 30m);
- an admin can force any model back to healthy (`POST /v1/admin/model-health/reset`).

Health gating + schema-aware fallback apply to **production predicts only** (`useCache` set,
no model override) — Studio test panels, shadow runs, and single-model overrides call the
model as asked and don't feed production health. Live state is in-process (like the provider
breaker; it resets on restart); every transition (`failure` / `tripped` / `recovered` /
`manual_reset`) is persisted to `model_health_events` via an async writer for observation.

**`RunAll`** — playground fan-out: goroutine per model, buffered channel, results in
arrival order (fastest first).

**Prediction cache** (`internal/cache`): per-task opt-in exact-match cache
(Redis in production, in-process memory for dev, off when unconfigured — `Cache`
interface is the seam). Every key is a SHA-256 over *what determined the output*:
task id, **deployed prompt version**, the **fully rendered prompt** (template +
every input/context value), system prompt, temperature, max_tokens, output
schema, and any **image inputs** (in order) — so deploys, param changes, schema
edits, and a different photo all invalidate implicitly. The chain is **not** part
of the key: the cache is keyed **per model** (the single routing key that produced
the answer), and is consulted *during* the fallback walk as the router reaches each
model — never as a pre-call shortcut past a higher-priority model. So a recovered
primary is always tried live rather than shadowed by a stale lower-priority entry.
TTL is the per-task `cache_ttl_seconds` or `DefaultTTL` (24h) — "model X said Y for
prompt P" is a stable fact regardless of chain composition.

Fill writes one entry under the serving model (**including a fallback**). A cached
answer from a non-primary model is still reported `fallback_used` / `X-Platform-
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

**Pluggable backend (`db.Open(driver, sqlitePath, postgresDSN)`):**
- **`sqlite`** (default) via `modernc.org/sqlite` (pure Go, no cgo). WAL mode,
  `busy_timeout=5000`, `foreign_keys=ON`, `SetMaxOpenConns(1)` (single writer). Fully tested.
- **`postgres`** via `jackc/pgx/v5/stdlib`, with a real pool (`MaxOpenConns=20`, idle/lifetime
  tuned). Implemented but **pending validation against a live Postgres** (no instance was
  available when it was written — see `docs/DEPLOY.md` for the two spots to confirm:
  numeric→float dashboard scans, and `shadow_reports.id` which uses `LastInsertId`, unsupported
  by pgx).

**Dialect seam (`dialect.go`):** the process-global `activeDriver` is set by `Open`. All hand-
written queries use portable `?` placeholders and route through `exec`/`query`/`queryRow` (and
the exported `db.Exec`/`db.Query`/`db.QueryRow`/`db.Rebind` for the `tasks` store and shadow
handler), which rewrite `?`→`$n` on Postgres. The few diverging fragments are behind helpers:
`nowExpr()`, `todayExpr()`, `daysAgoExpr(n)`, `ciLike(col)`. `created_at` is stored as TEXT in
**both** backends (canonical `YYYY-MM-DD HH:MM:SS`) so scanning, `substr()`, and ordering are
identical.

**Migration strategy:** `Migrate` dispatches on the driver. SQLite keeps the original idempotent
`CREATE TABLE IF NOT EXISTS` + guarded `ALTER TABLE ADD COLUMN` (ignore "duplicate column") so
existing dev DBs upgrade in place; Postgres applies an idempotent schema in `schema_postgres.go`
(`CREATE … IF NOT EXISTS`, `ADD COLUMN IF NOT EXISTS`, native types). Migrations auto-run on boot
in dev only; in prod run `cmd/migrate` out-of-band. Follow this pattern for future schema changes.

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
| Request | `prompt` (rendered), `system_prompt`, `model`, `image` (multimodal inputs — a JSON array of data URLs / image URLs, or NULL for text-only; legacy single-string rows still parse) |
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

**`model_health_events`** — the durable log behind the per-(task, model) health
breaker (§3.6): `(task_id, model, provider, event, reason, consecutive_failures,
cooldown_ms, state, created_at)`, where `event ∈ {failure, tripped, recovered,
manual_reset}`. Indexed on `(task_id, model)` and `created_at`. The live circuit
state is in-memory (`internal/health`); this table is the persisted history for the
admin model-health console and post-hoc analysis.

**`gateway_attempts`** — the full per-attempt trace of a prediction's fallback walk: **one row
per model the walk touched**, in walk order (`seq`, 0 = configured primary), under the run's
`run_id`. Where `runs` holds the single answer served, this holds everything behind it —
`outcome ∈ {success, error, schema_invalid, skipped_unhealthy, cache_hit}`, `fallback_used` +
`fallback_reason`, the model's `response`, classified `error` + `http_status` + `infra_failure`,
`retry_count`, per-call `latency_ms`, usage, cost, and `is_test`. Indexed on `run_id`, `task_id`,
`model`, `outcome`, `created_at`. Surfaced by `ListGatewayAttempts(run_id)` and attached to the
admin run-detail response (`RunDetailResponse.Attempts`), so the History drawer shows the whole
fallback story for one run, not just the winner.

**Queries** (`queries.go`): `InsertRun`, `GetRunByID` (poll), `ListSessions` /
`GetSession` / `DeleteSessions` (all user-scoped), `UpsertFeedback`,
`DashboardStats(userID)` → totals + `by_task` (runs/tokens/cost/avg-latency/success-rate)
+ `by_model` (incl. avg star rating via pre-aggregated join — no fan-out inflation) +
daily time series; `TaskSpendToday` (budget gate), `TaskDailyStats` (per-task stats
endpoint). Pre-Phase-0 rows surface as task `untagged` via COALESCE. **Admin
(cross-tenant, not user-scoped):** `ListAllRuns(filter, page, pageSize)` (lightweight
rows — truncated prompt preview + image count, never the bytes — with filters on
task/model/user/status/prod-vs-test and prompt-text search; `RunFilter` also carries `MaxID`
(a point-in-time snapshot **anchor** — only rows with `id <= MaxID` are returned, so rows
inserted while a user pages never shift the slices; 0 resolves to the current `MAX(id)` and the
anchor actually used is returned for pinning the next page) and `HasTask` (when true, excludes
playground/compare runs)), `GetRunDetail(runID)`
(full prompt + per-model results + image data URLs), `DistinctRunModels` (filter
dropdown), `InsertHealthEvent`, `ListHealthEvents(taskID, model, page, pageSize)`.
Image columns serialize via `imagesToColumn` / `ParseImagesColumn` (JSON array,
back-compatible with legacy single-string rows).

**`RunWriter`** (`runwriter.go`): buffered channel (1024) + drain goroutine →
`InsertRun`; non-blocking `Write` with dropped-row counter; `Close()` flushes on
shutdown. Handlers go through `Handler.insertRun`, which uses the writer when set and
falls back to synchronous inserts when nil (tests stay deterministic).
**`GatewayAttemptWriter`** (`attemptwriter.go`): the same pattern for `gateway_attempts` (larger
buffer, 4096, because one run emits several attempt rows) → `InsertGatewayAttempt`.
**`HealthEventWriter`** (`healthwriter.go`): the same pattern for `model_health_events`
— a buffered channel drained to `InsertHealthEvent`, wired as the health tracker's
event sink so transitions persist off the request hot path. All three `Close()` are
idempotent and flushed by the graceful-shutdown path (§3.1).

### 3.8 HTTP API — `internal/api`

`router.go` — chi router; RequestID/Logger/Recoverer; CORS restricted to
`ALLOWED_ORIGINS` with `AllowCredentials: true` and methods `GET/POST/PUT/DELETE/OPTIONS`.

**Public:** `GET /health` (liveness) · `GET /ready` (readiness — pings the DB, `503` when it
can't serve). **Auth routes depend on `AUTH_MODE`:** in `demo` — `GET /auth/demo-users` ·
`POST /auth/login {user_id}` (sets cookie) · `POST /auth/logout`; in `sso` — `GET /auth/sso/login`
· `GET /auth/sso/callback` · `POST /auth/logout` (the demo login routes are not registered at all).

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
| `POST /v1/tasks` · `GET /v1/tasks` · `GET/PUT /v1/tasks/{id}` | Registry CRUD. **`POST` is how tasks are authored** (`task:write` → admin; no YAML seeding). PUT has **merge semantics** — absent fields keep current values; an explicit `"input_schema": null` / `"output_schema": null` **clears** that schema (the only way to remove one). Schemas re-validate on write → 422 if not compilable |
| `DELETE /v1/tasks/{id}` | Delete a whole task + its prompt-version history (run rows kept for audit). **admin-only** (`task:delete`); **404** unknown · **409** for the built-in `playground` task |
| `POST /v1/tasks/{id}/predict {inputs}` | **The product endpoint** (see below) |
| `GET /v1/tasks/runs/{run_id}` | Poll a run (becomes async-result fetch in Phase 3) |
| `GET/POST /v1/tasks/{id}/versions` | Prompt history / save a draft (not activated) |
| `DELETE /v1/tasks/{id}/versions/{version}` | Prune a prompt version. **admin-only** (`task:delete`); **409** for the active version (deploy another first) |
| `POST /v1/tasks/{id}/deploy {version}` | Activate a version (Phase 2 eval gate slots here) |
| `POST /v1/tasks/{id}/test {inputs, version?, model?}` | Run pipeline as `is_test` with prompt/model overrides — Studio test panel |
| `GET /v1/tasks/{id}/stats?days=N` | Task-scoped totals + daily series, all callers |
| `POST /v1/shadow/compare {task_id, items[≤200]}` | Field-level match vs `expected_output` per item; persists report |
| `GET /v1/shadow/reports?task_id=` | List persisted shadow reports |

**Admin-only** (`RequireAdmin` middleware — gated on the `admin` *role*, not a
capability, because these are privacy-sensitive cross-tenant views; non-admins get **403**):

| Endpoint | Purpose |
|---|---|
| `GET /v1/admin/runs?page&page_size&task_id&model&user_email&q&status&type&anchor_id&has_task` | Prompt history across **all** users, newest first. Lightweight rows (truncated `prompt_preview` + `image_count`, never bytes). `status` = `success`\|`error`; `type` = `production`\|`test`. `anchor_id` pins a point-in-time snapshot (only runs with `id ≤ anchor_id`, so a growing runs table never shifts rows across pages; absent → newest run now, and the resolved anchor is returned). `has_task=true` excludes playground/compare runs |
| `GET /v1/admin/runs/models` | Distinct models seen in `runs` (filter dropdown) |
| `GET /v1/admin/runs/{run_id}` | Full run detail: prompt, system prompt, image data URLs, per-model results |
| `GET /v1/admin/model-health` | Live per-(task, model) circuit states `{enabled, statuses:[{task_id, model, provider, state, consecutive_failures, total_failures, total_successes, trips, cooldown_ms, open_for_seconds, last_reason, last_change}]}` |
| `GET /v1/admin/model-health/events?task_id&model&page&page_size` | Persisted health/fallback events (newest first) |
| `POST /v1/admin/model-health/reset {task_id, model}` | Force a model back to healthy (records a `manual_reset` event); **404** if the pair was never tracked |

Route-order note: `/v1/tasks/runs/{run_id}` is registered **before** `/v1/tasks/{task_id}`
so `"runs"` never matches as a task id; likewise `/v1/admin/runs/models` is registered
before `/v1/admin/runs/{run_id}`.

**`Predict` pipeline** (`task_handlers.go` + the shared core in `predict_core.go`,
reused by Test and Shadow): resolve task (404 / 409-if-inactive; in-memory config
cache, no DB read) → **budget gate** (429 + `Retry-After` to UTC midnight when daily
spend ≥ cap; warn-log at 80%; cap 0 = exempt; spend comes from an in-memory view —
`budget_cache.go`: DB SUM refresh ≤ every 5s + per-prediction local increments — so
no per-request aggregate query and async-writer lag can't under-count) → `executePrediction`: `ValidateInput` (422) → `RenderPrompt` (+ `collectImages`
attaches the schema-typed image fields as vision blocks) → **per-task input-size limits**
(`enforceInputLimits`: `MaxPromptChars` / `MaxImageKB` / `MaxImages`, 0 = no limit) →
oversized input returns **413** (a SECOND, deterministic 413 source distinct from the §3.9
rate-limiter's per-request input cap — retrying won't help, the input has to shrink) →
`llm.CallWithFallbackOpts` over
`[model, fallbacks...]` with the per-model **cache lookup** (hit → `cached:true`, zero
usage, `cache_hit` run row), the **health gate** (skips models unhealthy for this task),
and the **output validator** (production predicts only) → a provider error **or a
schema-invalid output** advances the chain to the next model *in the same request*; a
clean schema-valid success stops. Invalid output from *every* model returns **200 with
`output_valid:false` + `raw_response`** (not an error; correction retry is Phase 2); an
upstream chain failure, or all models unhealthy, returns **502** → async run write
(task/user/provider/version/fallback/is_test/images stamped) → **`X-Platform-Degraded:
true`** header when a fallback served it or the chain failed → respond (`fallback_used`
included):

```json
{ "task_run_id": "...", "task_id": "...", "prompt_version": 1,
  "model": "llama-groq", "provider": "groq",
  "output": {…}, "output_valid": true, "raw_response": "…", "error": null,
  "cached": false,
  "usage": {"input_tokens":172,"output_tokens":53,"total_tokens":225,"cost_usd":1.3e-05},
  "latency_ms": 696, "gateway_latency_ms": 712 }
```
`latency_ms` is the winning model's call time; `gateway_latency_ms` is the end-to-end
platform wall-clock (input validation + the whole fallback walk, including failed attempts,
+ output validation + cache work). Gateway ≥ model; the client portal shows both plus the
computed `(+Nms overhead)`.

### 3.9 Per-task rate limiter — `internal/ratelimit`

A per-task **rolling-window** limiter (config in §3.2, `RATE_*`) sits in front of production
predicts, independent of the daily budget gate. Each task gets its own window (default 1m) with
its own lock — tasks never serialize against each other. Three gates:

1. **Per-request input cap** (`MaxInputTokens`) — an estimate over the request's chars
   (`/RateCharsPerToken`) plus `RateTokensPerImage` per image; over the cap → **413**
   (`input_too_large`), no `Retry-After` (retrying the same input won't help).
2. **Request-rate cap** (`MaxRequests` per window) → **429** with `Retry-After` = time to window refill.
3. **Token budget** (`MaxTokens` per window) → **429** with `Retry-After`.

It uses a **reserve-then-reconcile** pattern: `executePrediction` reserves the request's estimate
up front, then `Reconcile`s to the tokens actually consumed (input+output across every attempt,
incl. failed/fallback ones). A request that ran always counts toward the request gate even if it
failed. A `0` for any `Max…` disables that gate; `RATE_LIMIT_ENABLED=false` turns the whole thing
off. Test/shadow runs are not rate-limited.

---

## 4. Frontend Deep Dive

**Stack:** React 19, Vite 8, Tailwind 4, `@meesho/merlin-ui-tailwind` (design system),
`js-tiktoken`. No router library — a `view` state switch in `AppShell`. In dev the API base is
`''` and the Vite proxy (`vite.config.ts`) forwards `/run /sessions /health /auth /pricing
/feedback /dashboard /v1` to `:8000`; for a split-repo prod build `src/api/client.ts` reads
`const BASE = (import.meta.env.VITE_BACKEND_URL ?? '').replace(/\/+$/, '')` so calls target the
backend's origin (see `.env.example` and `DEPLOY.md`).

**Auth flow:** `main.tsx` wraps the app in `AuthProvider` (`src/auth/AuthContext.tsx`),
which bootstraps via `GET /auth/me` (401 = logged out, no crash). `App.tsx` is the gate:
spinner → `LoginScreen` (one-click demo users from `/auth/demo-users`) → `AppShell`.
`useAuth` hook lives in `src/auth/useAuth.ts` (separate file for fast-refresh). The API
client (`src/api/client.ts`) sends `credentials: 'include'` on every call and throws
typed `ApiError{status}`.

**Pages** (top-nav in `AppShell` — `compare | tasks | dashboard`, plus `history | health | test`
shown **only to admins** (`user.role === 'admin'`); the shell fetches `/pricing` once and feeds
`setPricing`). Note: there is no longer an *Estimate* tab, and `VersionsPage` exists as a
component home but is not wired into the current top-nav (version management lives inside the
Tasks detail). The admin **Test** tab embeds the client portal (`ClientPortalPage`) for in-Studio
predict testing.
- **Tasks / Studio** (`TasksPage`) — master/detail over the registry. A **+ New** button
  (admin) opens a `CreateTaskForm` that authors a task from scratch — id (live slug +
  duplicate check), name, description, primary model + fallback chain, temperature, max
  tokens, daily budget, cache on/off + TTL, an **Input limits** section (max text length /
  max image size KB / max number of images — blank or 0 = no limit, mapping to
  `max_prompt_chars` / `max_image_kb` / `max_images`), system prompt, prompt template, and
  optional input/output schemas (reusing `SchemaEditor`) — and POSTs it to `/v1/tasks`. The detail
  header carries an **admin-only Delete task** button (confirm → `DELETE /v1/tasks/{id}`,
  refuses `playground`). Per task: config
  summary + 30-day usage strip, **model-routing chain editor** (ordered list,
  position 0 = primary; add models from the registry, drag rows to reorder,
  remove; saves `{model, fallback_models}` via PUT merge), **schema editor**
  (`components/SchemaEditor` + `utils/schema.ts`) — per-schema enable toggle
  plus a visual **Fields** mode (name / type / required / description, enum for
  strings, element type for arrays, and an **`image`** field type for input schemas —
  which serializes to a JSON-Schema array with `items.format:"image"`, the marker the
  backend recognises images by) and a **JSON** mode that round-trips with it
  and is the escape hatch for anything the field view can't represent (the
  converter returns null and forces JSON mode rather than dropping data); both
  schemas save in one PUT, prompt editor with token/cost estimate, **save draft →
  test (schema-generated input form, version/model overrides, validity badge) →
  deploy (confirm)**, version history (the reusable `components/VersionHistory`
  — paginated "show N at a time", side-by-side compare vs the live prompt,
  deploy, and delete of inactive versions). The
  edit→test→deploy loop runs entirely in the browser. **All write/deploy/delete
  actions are role-gated** (`auth/permissions.ts` mirrors the backend matrix —
  UI gating only; the gateway enforces): with the current two roles these are all
  **admin-only**, so a `client` sees disabled editors + a read-only banner.
- **Versions** (`VersionsPage`, *not currently in the top-nav*) — a dedicated, task-agnostic
  home for prompt history: pick a task on the left, manage its versions on the right via the same
  `VersionHistory` component the Studio task detail embeds (identical compare / deploy / delete /
  pagination behaviour). The component is the live path (embedded in Tasks); the standalone page
  is retained but unlinked.
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
- **Dashboard** (`DashboardPage`) — summary cards, **By task** table (runs/tokens/cost/
  latency/success), By model table (incl. avg ★), daily-spend CSS bars. No chart lib.
- **History** (`AdminRunsPage`, admin-only) — cross-tenant prompt history: a filterable,
  paginated table of every user's runs (search prompt text; filter by task / model / user
  email / status / production-vs-test) with status pills (ok/error, cached, fallback, test).
  The list is lightweight (server-truncated previews + an image indicator) so it stays fast
  on large prompts/images; clicking a row opens a detail drawer that lazily fetches the full
  prompt, system prompt, image grid, and per-model responses (wrapped + height-capped so huge
  bodies never break the layout).
- **Health** (`ModelHealthPage`, admin-only) — the per-(task, model) circuit-breaker console:
  a live status table (auto-polls every 4s) showing each model's state (healthy / probing /
  unhealthy + cooldown remaining), consecutive failures, trips, and last reason, with a
  one-click **Mark healthy** override (`POST /v1/admin/model-health/reset`); plus a filterable
  **event log** (failure / tripped / recovered / manual_reset) — click a status row to filter
  the log to that pair.
- **Test** (`ClientPortalPage`, admin-only) — embeds the consumer client-portal experience inside
  the Studio so an operator can exercise the real `POST /v1/tasks/{id}/predict` against a task
  (the same surface external callers use; see §the client portal in the work doc).

**Types:** `src/types/index.ts` mirrors the Go JSON contracts exactly (`TRunResponse`,
`TDashboard{by_task,by_model,daily}`, `TUser`, `TPricing`, the admin
`TRunListResponse`/`TRunDetail`/`TRunFilters`, and the health
`TModelHealthResponse`/`THealthEventsResponse` …). Update it whenever a Go response type
changes.

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

# Mint a service token (e.g. for the client portal or CIS). -role defaults to client
# (read + predict); use admin for a Studio operator principal.
cd llm_platform_go && go run ./cmd/issue-token -sub svc:cis -email cis@svc.local -role client -ttl 8760h

# First-run / deploy helpers (see docs/DEPLOY.md)
cd llm_platform_go && go run ./cmd/migrate                 # apply schema (sqlite|postgres per DB_DRIVER)
cd llm_platform_go && go run ./cmd/bootstrap -issue-admin  # gen secret, validate, migrate, lock DB, mint admin

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
- `rbac_test.go` — the role × permission matrix (`TestRBACMatrix`) and that a
  token with no role claim resolves to `admin` (`TestDefaultRoleForTokenWithoutClaim`).
- `prompt_redaction_test.go` (`TestPromptVisibility`) — a `client` (no `task:view_prompt`) gets
  `prompt_template`/`system_prompt` blanked on task + version reads; `admin` sees them.
- `delete_version_test.go` — version delete is admin-only and 409s on the active version.
- `schema_update_test.go` — PUT merge semantics for schemas, incl. `"input_schema": null`
  clearing one.
- `tasks_test.go` — registry CRUD, prompt-version bump rules, schema/template
  validation, fenced-output parsing.
- `predict_test.go` — full predict pipeline against a fake OpenAI-compatible server:
  happy path (attribution, usage, parsed output), 422 input cases, invalid-output
  flagging, 404, playground stamping, dashboard by_task, **single + multiple image
  forward/store** (image_url blocks to the provider, JSON-array `image` column).
- `db_test.go` — run insert/query/delete, pagination, ordering (user-scoped).
- `phase1_test.go` — budget 429 + Retry-After + exemption, full version lifecycle
  (draft doesn't activate, test stamps `is_test` + renders the draft, deploy switches
  production), shadow compare exact numbers + persistence, per-task stats, RunWriter
  flush/drop semantics.
- `internal/llm/runner_test.go` / `provider_test.go` — registry↔pricing.json parity,
  retry rules + error classification, message building, OpenAI-compat wire format
  (headers, endpoint path, error-body mapping) — exercising the single
  `openAICompatProvider` that serves both the Groq and Meesho-gateway backends.
- `internal/llm/breaker_test.go` — infra-failure classification (`isInfraFailure`)
  and fallback advance/stop rules across infra/config/content errors and a dead chain.
- `cache_predict_test.go` — cache hit serves without a provider call (zero cost,
  `cache_hit` row, spend unchanged), key sensitivity (inputs, deploy-with-identical-
  template invalidates), opt-in required, Studio test bypass, failures and
  schema-invalid outputs never cached. `internal/cache/cache_test.go` — key
  determinism/sensitivity, Redis round-trip + TTL via miniredis, outage = miss.
- `admin_runs_test.go` — admin prompt-history list (lightweight rows + filters),
  full run detail, filter-on-missing-task = empty, unknown run = 404, and that
  non-admins get **403** on the admin endpoints.
- `model_health_test.go` — schema-invalid output falls back to the next model
  in-request; a model that keeps failing trips unhealthy, surfaces in the admin
  snapshot with persisted events, is **skipped** while open, and an admin reset
  restores it; unknown reset = 404, non-admin = 403.
- `internal/health/tracker_test.go` — the per-(task, model) breaker state machine
  (fake clock): trip after threshold, skip-then-probe, exponential backoff + cap,
  manual reset, disabled = always allow, nil-tracker safety.
- `internal/llm/multimodal_test.go` — `ChatMessage` marshaling: text-only keeps the
  legacy plain-string content form; with images it emits the OpenAI multimodal array.

Test auth helper: `authReq(t, method, url, body)` attaches a Bearer token for `u-admin`
signed with the test secret; `roleReq(t, role, …)` mints a token for any role (used by the
RBAC and admin 403 tests).

---

## 6. Design Decisions & Invariants (do not regress)

1. **Pure Go, single binary** — no LiteLLM/Langfuse/Python. Decided 2026-06-12.
2. **Task is the unit of everything** — cost, versioning, RBAC, budgets, (future) eval.
   Every run row must carry `task_id`.
3. **The platform never owns caller business logic** — no "if confidence < X then
   route to QC" inside the platform.
4. **Seams to preserve:** `users.Store` (identity — the SSO swap point), `llm.Provider` (model
   backends), `internal/db` query functions + `dialect.go` (storage engine — now realized as a
   sqlite|postgres switch; keep new SQL portable / route through the dialect helpers),
   `pricing.json` (rates).
5. **Playground ≠ product API** — Compare UI talks to `/run`/`/sessions`; services talk
   to `/v1/tasks/*`. Multi-turn chat semantics must not leak into the product API.
6. **Migrations are idempotent** and **per-dialect** (`Migrate` dispatches on the driver; SQLite
   guarded ALTERs, Postgres `… IF NOT EXISTS`). They auto-run on boot **in dev only**; prod
   applies them out-of-band via `cmd/migrate`. Demo-era data is throwaway; schema migrates, data
   does not.
11. **Prod refuses to boot misconfigured.** `config.Validate()` (gated by `APP_ENV=prod`) is the
    single place insecure defaults are rejected; never weaken it to "log and continue". New
    secrets/origins/secure-cookie requirements belong there.
7. **Observability writes never fail a response**; prediction responses distinguish
   "model failed" (502) from "output didn't validate" (200 + `output_valid:false`).
8. **Frontend pricing/types always follow the backend** (`/pricing`, mirrored types).
9. **The prediction hot path does no synchronous DB work** (decided 2026-06-12):
   task config from the in-memory store cache, budget from the cached spend view,
   run rows via the async writer, prediction cache in Redis/memory, health gate from
   the in-memory tracker (events persisted async). New per-request features must follow
   this — read from memory, write async.
10. **Health is per-(task, model), live state in-memory, history persisted.** The
    per-(task, model) breaker (`internal/health`) is the platform's only breaker. Live
    state resets on restart; only `model_health_events` is durable. Admin observability/override routes
    (`/v1/admin/*`) are gated on the `admin` *role* (`RequireAdmin`), not a task capability,
    because they expose cross-tenant data.
