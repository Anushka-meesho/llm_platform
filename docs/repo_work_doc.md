# Repo Work Doc — LLM Platform

> A living reference for **everything the repo does today**: the architecture, every HTTP endpoint (what it accepts and returns), how a prediction flows end to end, the LLM routing/fallback machinery, caching, prompt versioning, auth/RBAC, the data model, configuration, and the frontend. Workflow diagrams are included throughout.

The platform is a **task-keyed LLM prediction service** plus a **Studio** for authoring/versioning prompts and a **playground** for comparing models. A "task" bundles a prompt template, input/output JSON Schemas, a model routing chain (primary + fallbacks), sampling params, a daily budget, and cache settings. Callers invoke a task by id; the platform renders the prompt, routes to a model (with fallback + circuit breaking), validates output, caches, meters cost, and records every run.

---

## 1. System architecture

```mermaid
flowchart LR
  subgraph Client["Frontend (React + Vite)"]
    UI["Compare / Studio / Versions / Estimate / Dashboard"]
  end

  subgraph API["Go API (chi router)"]
    MW["Auth + RBAC middleware"]
    H["Handlers"]
    EP["executePrediction pipeline"]
  end

  subgraph LLMCORE["LLM execution layer"]
    FB["CallWithFallback (chain walk)"]
    REG["Model registry"]
    BRK["Circuit breakers (per provider)"]
    PRB["Recovery prober (15s)"]
  end

  subgraph Providers["Providers"]
    OAI["OpenAI"]; GRQ["Groq"]; GEM["Gemini"]; ANT["Anthropic"]; MSH["Meesho gateway"]
  end

  subgraph State["State"]
    DB[("SQLite (WAL)")]
    CACHE[("Prediction cache\nRedis | memory | off")]
    STORE["Task config store\n(in-proc cache + editMu)"]
  end

  UI -->|"cookie-auth JSON"| MW --> H
  H --> EP
  H <--> STORE
  STORE <--> DB
  EP --> FB --> REG --> Providers
  FB <--> BRK
  PRB --> Providers
  PRB <--> BRK
  EP <--> CACHE
  EP -->|"async run rows"| DB
```

**Process boundaries**
- One Go server process. Task config lives in SQLite, fronted by an in-process config cache (5s TTL) coordinated by a read/write lock (`editMu`).
- The prediction cache is a separate store: Redis in production, in-process memory for dev, or off.
- All observability writes (run rows) go through an async writer so they never block the request hot path.

---

## 2. Tech stack

**Backend (`llm_platform_go/`)**
- Go, [chi](https://github.com/go-chi/chi) router + CORS middleware, request-id/logger/recoverer.
- SQLite via `modernc.org/sqlite` (pure-Go, no cgo), WAL mode, single writer connection.
- JWT sessions (HS256), httpOnly cookie.
- Providers: direct HTTP for OpenAI-compatible backends; `anthropic-sdk-go` for the native Messages API.
- Prediction cache: `redis/go-redis/v9` or in-process memory.
- JSON Schema validation: `santhosh-tekuri/jsonschema/v6`. Prompt templating: Go `text/template`.

**Frontend (`llm_platform_frontend/`)**
- React 19 + TypeScript, Vite build/dev server.
- Tailwind CSS 4 + Meesho's Merlin UI design system.
- `js-tiktoken` for client-side token estimation.
- Fetch with `credentials: 'include'` (session cookie); dev proxy forwards API paths to `:8000`.

---

## 3. The prediction pipeline (how a predict request works)

Endpoint: `POST /v1/tasks/{task_id}/predict`. The same core (`executePrediction`) backs `/predict`, `/test`, and shadow comparison — differing only by options (test/shadow bypass the cache and don't count as production traffic in the same way).

```mermaid
flowchart TD
  A["POST /predict {inputs}"] --> B{"Task exists & active?"}
  B -- "no" --> B1["404 / 409"]
  B -- "yes" --> C{"Daily budget left?"}
  C -- "no" --> C1["429 + Retry-After (UTC midnight)"]
  C -- "yes" --> D["Validate inputs vs input_schema"]
  D -- "invalid" --> D1["422"]
  D -- "valid" --> E["Render prompt (Go template + inputs)"]
  E --> F["Build messages (system + user)"]
  F --> G["Chain = [primary, ...fallbacks] (read fresh from config store)"]
  G --> H["CallWithFallback walks the chain"]

  subgraph WALK["For each model in chain order"]
    H --> I{"Per-model cache hit?\n(only if cacheable)"}
    I -- "yes" --> I1["Serve cached answer · stop · zero cost"]
    I -- "no" --> J{"Circuit open?"}
    J -- "yes" --> K["Fail fast, advance chain"]
    J -- "no" --> L["Call provider (≤3 tries, backoff on 429/5xx)"]
    L --> M{"Outcome"}
    M -- "success" --> N["Stop"]
    M -- "content error (400/422)" --> N
    M -- "infra/config error (429/5xx/401/403/404/net)" --> K
    K --> I
  end

  N --> O{"Output schema?"}
  O -- "yes" --> P["Strip code fences · validate · set output_valid"]
  O -- "no" --> Q["raw text, output_valid = null"]
  P --> R["Cache fill (per-model, clean success only)"]
  Q --> R
  R --> S["Async run row + update spend"]
  S --> T{"success?"}
  T -- "yes" --> T1["200 (+ X-Platform-Degraded if fallback/degraded)"]
  T -- "no" --> T2["502 (+ X-Platform-Degraded)"]
```

**Key facts**
- **Budget gate** uses in-memory spend (refreshed from DB ≤ every 5s), not a per-request `SUM`. `daily_budget_usd: 0` = exempt. At ≥80% it logs a warning; at 100% it returns `429` with `Retry-After` set to seconds until UTC midnight.
- **Routing chain** is `[task.Model, ...task.FallbackModels]`, read fresh from the config store on every request — a routing edit changes which model runs, immediately.
- **Per-model cache** is consulted *during* the walk, as the router reaches each model — never as a pre-call shortcut. So a recovered primary is always tried live rather than shadowed by a stale lower-priority cache entry.
- **Output handling**: when a task has an output schema, the raw response is de-fenced (```` ```json ```` wrappers stripped) and validated; `output_valid` is `true`/`false`. With no schema, `output` is null and `output_valid` is null (raw text only).
- **Cache fill**: only on a *clean* success (schema valid, or no schema). Failures and schema-invalid outputs are never cached. One entry, keyed on the serving model, at the task's TTL (default 24h).
- **Observability**: every call (including cache hits, marked `cache_hit=1` with zero cost) writes a run row asynchronously.

---

## 4. Authentication & RBAC

### Login flow
1. `POST /auth/login {user_id}` → looks up a user in the identity store (the demo store is an in-memory SSO stand-in).
2. Issues an HS256 JWT with claims `sub, email, name, role, iss, iat, exp`.
3. Sets an httpOnly cookie (`llm_platform_token` by default), `SameSite=Lax`, `Secure` configurable, `MaxAge = TOKEN_EXPIRY` (default 12h).
4. `RequireAuth` middleware accepts the token via `Authorization: Bearer …` or the cookie, validates signature + expiry, and puts the `auth.User` on the request context. Role is embedded in the token, so the predict hot path needs no identity lookup.

The demo store seeds one user per role:

| id | email | role |
|---|---|---|
| `u-admin` | admin@demo.local | admin |
| `u-creator` | creator@demo.local | creator |
| `u-approver` | approver@demo.local | approver |
| `u-viewer` | viewer@demo.local | viewer |

A token with no role claim defaults to **caller** (least privilege that still allows predict + read).

### Roles × permissions

| Role | task:read | task:predict | task:write | task:deploy | task:delete | task:view_prompt |
|---|:---:|:---:|:---:|:---:|:---:|:---:|
| **admin** | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| **creator** | ✓ | ✓ | ✓ | ✗ | ✗ | ✓ |
| **approver** | ✓ | ✓ | ✗ | ✓ | ✗ | ✓ |
| **caller** (default) | ✓ | ✓ | ✗ | ✗ | ✗ | ✗ |
| **viewer** | ✓ | ✗ | ✗ | ✗ | ✗ | ✓ |

- `RequirePermission(perm)` runs after `RequireAuth` and returns `403` (`role 'X' is not permitted to task:Y`) when the role lacks the capability.
- **Prompt redaction**: callers without `task:view_prompt` get `prompt_template` and `system_prompt` blanked in task/version responses — they see the contract (schemas, metadata), not the prompt internals.

---

## 5. API reference

All errors are `{"detail": "<message>"}` with the HTTP status carrying the meaning. All non-public routes require a valid session.

### 5.1 Auth & meta (public unless noted)

| Method · Path | Accepts | Returns |
|---|---|---|
| `GET /health` | — | `{status:"ok", models_available:[…]}` |
| `GET /auth/demo-users` | — | `{users:[{id,email,name,role}]}` |
| `POST /auth/login` | `{user_id}` | `{user:{…}}` + sets cookie · 401 unknown · 422 missing |
| `POST /auth/logout` | — (auth) | `{status:"logged out"}` (clears cookie) |
| `GET /auth/me` | — (auth) | `{user:{…}}` · 401 if not authed |
| `GET /pricing` | — (auth) | `{pricing:{model:{input_cost_per_1k,output_cost_per_1k}}}` |

### 5.2 Playground / analytics (any authenticated principal)

**`POST /run`** — fan out one prompt across N models in parallel (the Compare playground).
- Accepts: `{prompt (req), models?[], model_conversations?{model:[{role,content}]}, temperature?, max_tokens?, session_id?, system_prompt?}`
- Returns: `{run_id, prompt, system_prompt, results:[{model,response,latency_ms,input_tokens,output_tokens,total_tokens,cost_usd,success,error}], total_wall_clock_ms, models_succeeded, models_failed}`

**`GET /sessions?page&page_size`** — paginated session list (default page_size 8, max 100).
- Returns: `{page,page_size,total_sessions,total_pages,sessions:[{session_id,first_prompt,turn_count,created_at}]}`

**`GET /sessions/{session_id}`** — full conversation: `{session_id, turns:[{run_id,prompt,system_prompt,created_at,results:[…]}]}` · 404 if missing.

**`DELETE /sessions`** — `{session_ids:[…]}` → `{deleted_count, session_ids}`.

**`POST /feedback`** — `{run_id, model, rating (1–5)}` → echoes back · 422 out of range.

**`GET /dashboard`** — aggregate analytics:
`{total_runs, total_tokens, total_cost_usd, by_task:[{task_id,runs,total_tokens,cost_usd,avg_latency_ms,success_rate}], by_model:[{model,runs,total_tokens,cost_usd,avg_latency_ms,avg_rating,rating_count}], daily:[{date,cost_usd,total_tokens,runs}]}`.

### 5.3 Task product API (RBAC-gated)

The full **Task object** (returned by create/get/update/list):
```
{ id, name, description,
  input_schema|null, output_schema|null,
  prompt_template, system_prompt, prompt_version,
  model, fallback_models[],
  temperature, max_tokens, daily_budget_usd,
  cache_enabled, cache_ttl_seconds, active,
  created_at, updated_at }
```

| Method · Path | Perm | Accepts | Returns / notes |
|---|---|---|---|
| `POST /v1/tasks` | write | full Task body (`id` slug `[a-z0-9-]{2,64}`, `name`, `prompt_template`, `model` required) | `201` Task · `422` validation (bad slug, unknown model, bad schema/template, temp∉[0,2]) |
| `GET /v1/tasks` | read | — | `{tasks:[…]}` (prompts blanked without `view_prompt`) |
| `GET /v1/tasks/{task_id}` | read | — | Task · `404` |
| `PUT /v1/tasks/{task_id}` | write | **partial** patch (only present fields change; `input_schema:null` removes it) | updated Task · routing changes here. Prompt change bumps `prompt_version` |
| `POST /v1/tasks/{task_id}/predict` | predict | `{inputs:{…}}` | predict response (below) · `409` inactive · `429` budget · `502` chain failed |
| `GET /v1/tasks/runs/{run_id}` | read | — | `{run_id,task_id,prompt_version,provider,created_at,results:[…]}` |
| `GET /v1/tasks/{task_id}/versions` | read | — | `{task_id,active_version,versions:[{version,prompt_template,system_prompt,note,created_by,created_at,active}]}` |
| `POST /v1/tasks/{task_id}/versions` | write | `{prompt_template (req), system_prompt?, note?}` | `201 {task_id,version,active:false}` (saves a draft; doesn't activate) |
| `DELETE /v1/tasks/{task_id}/versions/{version}` | delete | — | `{task_id,version,status:"deleted"}` · `409` if it's the active version |
| `POST /v1/tasks/{task_id}/deploy` | deploy | `{version (req)}` | `{task_id,active_version,status:"deployed"}` (makes a version live) |
| `POST /v1/tasks/{task_id}/test` | write | `{inputs (req), version?, model?}` | predict response, marked `is_test`; **bypasses cache**, supports version/model overrides |
| `GET /v1/tasks/{task_id}/stats?days=30` | read | — | `{task_id,days,totals:{total_cost_usd,total_tokens,success_count,failure_count,avg_latency_ms}, daily:[{date,cost_usd,tokens,runs}]}` |

**Predict response shape** (`/predict` and `/test`):
```
{ task_run_id, task_id, prompt_version, model, provider,
  output|null,            // parsed JSON when output_schema validates
  output_valid|null,      // null when task has no output schema
  raw_response|null, error|null,
  fallback_used,          // served by a non-primary model
  cached,                 // served from the prediction cache (zero cost)
  usage:{input_tokens,output_tokens,total_tokens,cost_usd},
  latency_ms }
```
Header `X-Platform-Degraded: true` is set when a fallback served it or the chain failed.

### 5.4 Shadow comparison harness

**`POST /v1/shadow/compare`** (write) — run a labeled dataset through a task and score field-level accuracy.
- Accepts: `{task_id (req), items:[{inputs, expected_output}] (req, ≤200)}`
- Returns: `{id, task_id, items, match_rate (0..1), items_fully_matched, avg_latency_ms, p50_latency_ms, p95_latency_ms, total_cost_usd, mismatches:[{item,field,expected,got}] (≤20), created_at}`

**`GET /v1/shadow/reports?task_id=`** (read) — last 50 reports (newest first), each with summary metrics + a `details` blob (`items_fully_matched`, `p50_latency_ms`, `mismatches`).

---

## 6. LLM execution layer

### Model registry
A single `registry` map is the source of truth for routing. Every model key maps to `{modelID, provider, clientFn, reasoning}`:

| Provider | Model keys (friendly) |
|---|---|
| OpenAI | `gpt-5.1, gpt-5, gpt-5-mini, gpt-5-nano, gpt-4.1, gpt-4.1-mini, gpt-4.1-nano, gpt-4o, gpt-4o-mini` |
| Groq | `llama-groq` (→ llama-3.3-70b-versatile) |
| Gemini | `gemini-3-pro, gemini-2.5-pro, gemini-2.5-flash, gemini-2.5-flash-lite, gemini-flash` |
| Anthropic | `claude-fable-5, claude-opus-4-8, claude-sonnet-4-6, claude-haiku-4-5` |
| Meesho gateway | `meesho-gemini-2.5-flash` (→ vertex/gemini-2.5-flash) |

- **`reasoning` flag** (gpt-5 family): the chat-completions wire rejects `max_tokens`/`temperature`, so `CallModel` sends `max_completion_tokens` and omits temperature.

### Providers
- **`Provider` interface**: `Call(ctx, *chatRequest) (*chatResponse, error)`.
- **OpenAI-compatible provider** covers OpenAI, Groq, Gemini, and the Meesho gateway (POST `{baseURL}/chat/completions`). Auth is `Authorization: Bearer <key>`, except the Meesho gateway which uses the `x-bf-vk` virtual-key header.
- **Anthropic provider** uses the native Messages API (SDK retries disabled — the platform owns retry policy). System prompt → `system`, temperature not forwarded (current models reject it), `thinking` left at model default. A `refusal` stop reason maps to a `400`.
- `BuildClients` wires one provider per backend. Anthropic is left `nil` when its key is absent (so a missing key is a normal "not configured" call error, not a tripped breaker).

### Fallback semantics
`CallWithFallback` walks `[primary, …fallbacks]` in order:
- **Stops** on a success or a content error (`400/422`) — retrying a bad request elsewhere just burns money on the same bug.
- **Advances** on provider-specific trouble: infra (`429/5xx`, network, timeout, open circuit) and provider-config errors (`401/403/404`, provider not configured).
- Sets `FallbackUsed` (served by a non-primary) and `Degraded` (fallback used, or the whole chain failed) for the `X-Platform-Degraded` contract.

### Circuit breaker (per provider)

```mermaid
stateDiagram-v2
  [*] --> Closed
  Closed --> Open: 3 consecutive infra failures
  Open --> HalfOpen: cooldown 30s elapsed (admits 1 probe)\n(skipped in probe-only mode)
  HalfOpen --> Closed: probe succeeds
  HalfOpen --> Open: probe fails
```

- Trips after **3** consecutive infra failures; cooldown **30s**.
- **Probe-only mode** (enabled in production via the recovery prober): open circuits never half-open on production traffic — they fail fast to the next model, and only an out-of-band probe success closes them.
- Only infra failures count against the breaker; a `4xx` (except `429`) and a caller cancellation are "healthy" exchanges.

### Recovery prober
Background loop (default every **15s**, 5s probe timeout, no retries): pings each unhealthy provider's circuit with a 1-token request. Any completed exchange (even a `4xx`) proves the provider is reachable and closes the circuit, returning traffic to the highest-priority healthy model automatically.

### Retry & cost
- **Retry**: up to 3 attempts per model, backing off 2s/4s/6s, only for retryable statuses (`429/500/503`). Context cancellation/timeout stops retries.
- **Cost**: `CalculateCost(model, in, out)` uses `pricing.json` (`{model:{input_per_1m, output_per_1m}}`), rounded to 6 decimals; unknown models cost `0`. Loaded once at startup from `PRICING_PATH`.

---

## 7. Prediction cache

- **Interface**: `Get(ctx,key)→([]byte,bool)`, `Set(ctx,key,val,ttl)`. Errors are treated as misses — caching never fails a prediction.
- **Backends**: Redis (`NewRedis`, pings at boot), in-process memory (lazy TTL expiry), or off. Selected by config (below).
- **Key**: `"predict:" + SHA-256(JSON{task_id, prompt_version, model, system_prompt, rendered_prompt, temperature, max_tokens, output_schema})`. The key is **per model** — the routing chain is *not* part of the key. A deploy (version bump), param change, prompt change, or model change all produce new keys, so invalidation is implicit; stale entries age out by TTL.
- **TTL**: per-task `cache_ttl_seconds`, else 24h default.
- **Consulted during the fallback walk**: only when the router actually reaches a model. Studio test/shadow runs bypass the cache entirely.

---

## 8. Tasks, prompts, schemas

### Task config & validation
Defaults: `temperature 0→0.2`, `max_tokens 0→1000`, `prompt_version 0→1`, `fallback_models nil→[]`. Validation: slug id, known model(s), `name`/`prompt_template`/`model` required, `temperature∈[0,2]`, `max_tokens>0`, `cache_ttl_seconds≥0`, schemas must compile, template must parse.

### YAML seeding & routing persistence
- `tasks.d/*.yaml` files are **upserted** at startup (the onboarding contract). `SeedPlayground` adds a built-in free-form playground task (idempotent).
- **Routing persistence**: for an *existing* task, the YAML re-seed **preserves** the live `model`/`fallback_models` (and the `active` flag) — routing is seeded only at first creation and thereafter owned at runtime via the API/UI, surviving restarts until someone changes it. Prompt/schema edits in YAML *do* re-apply on restart (and bump the version).

### Prompt rendering & validation
- **Render**: Go `text/template` (`{{.field}}`, `{{if .field}}…{{end}}`), compiled templates cached by content hash, `missingkey=error` (referencing an undeclared key fails loudly). Declared input fields are pre-seeded so optional fields work with `{{if}}`.
- **Input validation**: against `input_schema` (JSON Schema) when present; otherwise any input is accepted.
- **Output validation**: strips a wrapping markdown code fence, then validates against `output_schema`; returns the cleaned JSON. No schema → raw text, no validation.

### Prompt version lifecycle

```mermaid
flowchart LR
  E["Edit prompt in Studio"] -->|"write"| D["Save draft (POST /versions)\nversion = max+1, active:false"]
  D --> T["Test draft (POST /test, version override)"]
  T --> P{"Good?"}
  P -- "no" --> E
  P -- "yes" --> DP["Deploy (POST /deploy)\n→ active_version, cache invalidates"]
  DP --> L["Live: /predict uses the deployed version"]
  D -. "admin" .-> X["Delete a non-active version"]
```

- Editing a task's prompt via `PUT` also bumps the version (records history). Drafts never auto-activate; `deploy` is the explicit publish gate (`task:deploy`). The active version can't be deleted.
- Edits to task config are serialized by the store's `editMu` write lock; predictions reading config take a shared lock, so a reader during an edit waits and then sees the new config (single-process coordination).

---

## 9. Data model (SQLite, WAL)

| Table | Purpose | Notable columns |
|---|---|---|
| `tasks` | task registry / config | id (PK), name, description, input_schema, output_schema, prompt_template, system_prompt, prompt_version, model, fallback_models (JSON), temperature, max_tokens, daily_budget_usd, active, cache_enabled, cache_ttl_seconds, created_at, updated_at |
| `prompt_versions` | prompt history & drafts | id, task_id, version, prompt_template, system_prompt, note, created_by, created_at · unique (task_id, version) |
| `runs` | every call (predict, /run, test, cache hits) | run_id, session_id, prompt, system_prompt, model, response, latency_ms, input/output/total_tokens, cost_usd, success, error, user_id, user_email, task_id, prompt_version, provider, fallback_used, cache_hit, is_test, created_at |
| `feedback` | star ratings | run_id, model, user_id, rating · unique (run_id, model, user_id) |
| `shadow_reports` | accuracy comparisons | id, task_id, created_by, items, match_rate, avg_latency_ms, p95_latency_ms, total_cost_usd, details (JSON), created_at |

- **Single writer** (`SetMaxOpenConns(1)`) + `busy_timeout=5000` avoid "database is locked".
- **`RunWriter`**: a 1024-entry buffered channel drained by one goroutine; handlers submit run rows without blocking. If the buffer is full, the row is dropped and counted (a prediction is never blocked by observability). `Close()` flushes on shutdown.

---

## 10. Configuration (environment variables)

| Var | Default | Controls |
|---|---|---|
| `PORT` | `8000` | HTTP port |
| `DB_PATH` | `./llm_platform.db` | SQLite file |
| `PRICING_PATH` | `./pricing.json` | cost table |
| `TASKS_DIR` | `./tasks.d` | YAML task configs to seed |
| `OPENAI_API_KEY` / `_BASE_URL` | — / `api.openai.com/v1` | OpenAI |
| `GROQ_API_KEY` / `_BASE_URL` | — / `api.groq.com/openai/v1` | Groq |
| `GEMINI_API_KEY` / `_BASE_URL` | — / Gemini OpenAI-compat endpoint | Gemini |
| `ANTHROPIC_API_KEY` / `_BASE_URL` | — / SDK default | Anthropic |
| `MEESHO_GATEWAY_VK` / `_BASE_URL` | — / internal gateway URL | Meesho gateway (x-bf-vk) |
| `REDIS_ADDR` / `REDIS_PASSWORD` / `REDIS_DB` | — / — / `0` | Redis cache |
| `CACHE_BACKEND` | `redis` if `REDIS_ADDR` set, else `off` | `redis` \| `memory` \| `off` |
| `JWT_SECRET` | `dev-insecure-secret-change-me` | session signing key |
| `AUTH_COOKIE_NAME` | `llm_platform_token` | cookie name |
| `AUTH_ISSUER` | `llm-platform-demo` | JWT `iss` |
| `COOKIE_DOMAIN` / `COOKIE_SECURE` | — / `false` | cookie scope/security |
| `TOKEN_EXPIRY` | `12h` | session lifetime |
| `ALLOWED_ORIGINS` | `http://localhost:5173` | CORS origins |

At least one provider key is required at boot (a warning logs the missing ones; the server still starts — those models fail at call time).

---

## 11. Frontend

Single-page app gated by `/auth/me` (spinner → login → app). Navigation lives in `AppShell`; pricing is fetched once on mount and feeds client-side cost estimates.

| Page | What a user does |
|---|---|
| **Compare** (playground) | Pick 2–N models, enter prompt/system prompt (+ images), tune temperature/max tokens, see side-by-side responses with latency/tokens/cost, rate 1–5★, browse/load/delete sessions |
| **Tasks (Studio)** | Browse tasks; view config + 30-day stats; edit system + prompt template; save drafts; edit input/output schemas (visual field editor or raw JSON); configure model routing; **test** against any version/model (client-measured round-trip latency); view/compare/deploy version history |
| **Versions** | Dedicated version browser per task: paginated history, compare two versions, deploy (approver/admin), delete (admin) |
| **Estimate** | Pre-flight token + cost calculator (single or batch) across all models, before spending anything |
| **Dashboard** | Totals (runs/tokens/spend), per-task and per-model breakdowns, daily spend trend, ratings, success rates |

**API client** (`src/api/client.ts`): one method per endpoint above, all with `credentials:'include'`; an `ApiError` carries the HTTP status so a `401` can trigger re-auth.

**Client RBAC** (`src/auth/permissions.ts`) mirrors the backend table and hides/disables controls (save draft, deploy, delete) by role — the backend remains the source of truth and enforces every check. Callers without `view_prompt` never receive prompt text.

**Notable components/utils**: `SchemaEditor` (dual visual/raw JSON Schema editor), `VersionHistory` (reused by Studio + Versions), `MessageBubble` (response + rating), `useChat`/`useSessions` hooks, `tokens.ts` (tiktoken counting + cost), `schema.ts` (JSON Schema ↔ field list).

---

## 12. Running locally

- **Backend**: `cd llm_platform_go && go run ./cmd/server` (listens on `:8000`; needs ≥1 provider key in `.env`).
- **Frontend**: `cd llm_platform_frontend && npm run dev` (Vite on `:5173`, proxies API to `:8000`).
- **Tests**: `cd llm_platform_go && go test ./...` (add `-race` for the concurrency paths).

---

*Generated from the codebase as of this revision. When endpoints, the model registry, or RBAC change, update the relevant table here.*
