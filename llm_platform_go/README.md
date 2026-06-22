# LLM Platform — Backend

Go backend for routing, running, and managing LLM predictions at scale, built for
the Meesho cataloging team. It is a **prediction factory**: every use case is a
named **Task** (input/output JSON Schemas, prompt template + versions, model
chain, budgets, and limits) callable at `POST /v1/tasks/{id}/predict`, with a
multi-model fallback gateway, per-(task, model) circuit breaker, prediction
cache, per-task rate + size limits, and full run/observability persistence.

Single statically-linked Go binary, no CGo. Ships against SQLite (dev) or
Postgres (prod) behind one dialect seam.

> **Deeper docs:** the [`docs/`](docs/) folder is the canonical reference —
> [`00-big-picture`](docs/00-big-picture.md) through
> [`10-caching-and-cost`](docs/10-caching-and-cost.md), plus
> [`DEPLOY.md`](docs/DEPLOY.md). The repo-wide guides live one level up in
> [`../docs/repo-guide.md`](../docs/repo-guide.md) and
> [`../docs/repo_work_doc.md`](../docs/repo_work_doc.md).

## Tech stack

| | |
|---|---|
| Language | Go 1.24 |
| Router | [Chi v5](https://github.com/go-chi/chi) |
| Database | SQLite (`modernc.org/sqlite`, WAL) **or** Postgres (`jackc/pgx`) — one dialect seam |
| Auth | JWT (`golang-jwt/jwt v5`) + RBAC; demo or SSO/OIDC login modes |
| Cache | In-memory or Redis (`go-redis v9`) |
| LLM providers | Meesho bifrost gateway (OpenAI-compatible) + Groq direct API |
| Schema validation | JSON Schema (`santhosh-tekuri/jsonschema v6`) |

## Models & providers

All models except Groq's Llama are served through the single **Meesho bifrost
gateway** — an OpenAI-compatible endpoint authenticated with the `x-bf-vk`
header. Vendor names are cost-attribution labels recorded on each run, **not**
separate SDK integrations.

| Model key | Served via | Attribution |
|---|---|---|
| `gpt-4o`, `gpt-4o-mini` | Meesho gateway | `openai` |
| `gemini-2.5-pro`, `gemini-2.5-flash` | Meesho gateway | `gemini` |
| `claude-sonnet-4-6` | Meesho gateway | `anthropic` |
| `llama-groq` (Llama-3.3-70B) | Groq API (direct) | `groq` |

The routing registry in [internal/llm/runner.go](internal/llm/runner.go) is the
single source of truth. Gemini-2.5 **thinking** models are supported: a
`minOutputTokens` floor raises `max_tokens` (thinking tokens share the output
budget), and array-shaped content from thinking models is concatenated (text
parts kept, thought parts discarded).

## Key capabilities

- **Tasks** — input/output JSON Schemas, prompt template with versioning
  (draft → deploy), model chain, temperature, max tokens, daily budget, cache.
- **Fallback gateway** — primary + ordered fallbacks; schema-aware, with retries
  and error classification ([internal/llm](internal/llm)).
- **Circuit breaker** — per-(task, model) health gating, auto-reprobe
  ([internal/health](internal/health)).
- **Per-task rate limiter** — request/token windows via `RATE_*`; `429` (window)
  / `413` (oversized input) ([internal/ratelimit](internal/ratelimit)).
- **Per-task input size limits** — `max_prompt_chars`, `max_image_kb`,
  `max_images` (0 = no limit), enforced as `413` on production predicts **and**
  Studio test runs.
- **Multimodal** — image inputs are typed via a JSON-Schema `format:"image"`
  marker (array of image strings, any field name; `image`/`images` names kept as
  a legacy fallback). Image bytes are attached to the model call as `image_url`
  blocks and are **never inlined into the prompt text** (the template sees a
  count).
- **Prediction cache** — per-task opt-in, in-memory or Redis, keyed on the full
  rendered request ([internal/cache](internal/cache)).
- **Observability** — every run persisted; per-model **gateway attempt trace**
  (`gateway_attempts`); admin run history with snapshot pagination.

## Project layout

```
cmd/
  server/main.go       — HTTP server entry point (:8000)
  issue-token/main.go  — CLI: mint JWT tokens for users
  migrate/             — apply DB schema (run before serving in prod)
  bootstrap/           — first-run setup (schema + seed)

internal/
  api/        — HTTP handlers, router, middleware, predict pipeline
  llm/        — provider interface, fallback chain, model registry, retries
  tasks/      — Task config, prompt versioning, template render, validation
  ratelimit/  — per-task request/token rate limiter
  health/     — per-(task, model) circuit breaker
  cache/      — in-memory + Redis prediction cache
  db/         — schema + queries; dialect seam (sqlite / postgres), async writers
  auth/       — JWT parsing + RBAC
  config/     — env loader + prod hardening (config.Validate)
  users/      — user store (demo + SSO identity seam)
  schema/     — JSON Schema registry + request schemas (YAML)
  types/      — shared request/response DTOs

tests/         — integration tests
pricing.json   — per-model token costs (USD per 1M tokens)
```

## Setup

### 1. Environment file

```bash
cp .env.example .env   # set MEESHO_GATEWAY_VK and/or GROQ_API_KEY
```

| Variable | Required | Description |
|---|---|---|
| `MEESHO_GATEWAY_VK` | one of these two | Virtual key for the Meesho gateway (`x-bf-vk`); serves every non-Groq model |
| `GROQ_API_KEY` | one of these two | Groq API key; serves `llama-groq` (direct) |
| `APP_ENV` | No | `dev` (default, permissive: auto-migrate, demo login) or `prod` (hard-fails boot on insecure defaults) |
| `DB_DRIVER` | No | `sqlite` (default) or `postgres` |
| `DB_PATH` | sqlite | SQLite file path (e.g. `./llm_platform.db`) |
| `DB_DSN` | postgres | Postgres DSN (e.g. `postgres://user:pass@host:5432/llm_platform?sslmode=require`) |
| `JWT_SECRET` | Yes | Secret for signing JWTs (a real secret is required in `prod`) |
| `AUTH_MODE` | No | `demo` (passwordless pick-a-user, dev only) or `sso` (OIDC; see `OIDC_*`) |
| `ALLOWED_ORIGINS` | prod | Comma-separated frontend origins for CORS |
| `PORT` | No | HTTP port (default `8000`) |
| `REDIS_URL` | No | Redis URL; omit for in-memory cache |
| `RATE_*` | No | Per-task rate-limiter window/budgets (see [docs/02-config.md](docs/02-config.md)) |
| `PRICING_PATH` | No | Path to `pricing.json` (absolute required in prod) |

### 2. Database

- **Dev (SQLite):** schema auto-migrates on boot.
- **Prod (Postgres):** run migrations explicitly, then serve:

```bash
go run ./cmd/migrate     # or ./cmd/bootstrap for first-run schema + seed
```

### 3. Run

```bash
go run ./cmd/server      # http://localhost:8000
go run ./cmd/issue-token # mint a JWT for testing
go test ./...            # unit + integration tests
```

`GET /ready` reports readiness (DB reachable, config valid).

## API endpoints

| Method | Path | Auth | Description |
|---|---|---|---|
| `POST` | `/auth/login` | — | Demo/SSO login → session cookie |
| `POST` | `/run` | user | Multi-model playground comparison |
| `GET` | `/sessions`, `/sessions/{id}` | user | Playground session history |
| `POST` | `/feedback` | user | Star-rate a run |
| `GET` | `/pricing`, `/dashboard` | user | Pricing table / usage stats |
| `GET/POST` | `/v1/tasks` | user / `task:write` | List / create tasks |
| `GET/PUT/DELETE` | `/v1/tasks/{id}` | user / `task:write` / `task:delete` | Read / update / delete task |
| `POST` | `/v1/tasks/{id}/predict` | user | **Production prediction** |
| `POST` | `/v1/tasks/{id}/test` | user | Studio test run (flagged, size-limited) |
| `GET/POST` | `/v1/tasks/{id}/versions` | user / `task:write` | List / save draft prompt versions |
| `POST` | `/v1/tasks/{id}/deploy` | `task:write` | Deploy a prompt version |
| `POST` | `/v1/shadow/compare` | user | A/B shadow comparison |
| `GET` | `/v1/admin/runs` | admin | Run history — params: `page,page_size,task_id,model,user_email,q,status,type,has_task,anchor_id` |
| `GET` | `/v1/admin/runs/{id}` | admin | Full run detail incl. gateway trace |
| `GET` | `/v1/admin/model-health` | admin | Circuit-breaker state per (task, model) |
| `POST` | `/v1/admin/model-health/reset` | admin | Force a (task, model) back to healthy |

`anchor_id` pins a point-in-time snapshot so paging through history doesn't shift
as new runs arrive; `has_task` excludes playground/compare runs. See
[docs/08-database.md](docs/08-database.md) and the API sections of
[../docs/repo_work_doc.md](../docs/repo_work_doc.md).

## Contributing

PRs use [`.github/PULL_REQUEST_TEMPLATE.md`](.github/PULL_REQUEST_TEMPLATE.md) —
fill every section (Release Type, Testing Environments, Metrics impacted,
Downstream impacts, Rollback plan). DB migrations must be additive/backward-
compatible (guarded `ALTER`s); reflect any config change in `.env.example`.

## Related

- [Frontend repo](https://github.com/Meesho/cataloging_llm_platform-frontend)
