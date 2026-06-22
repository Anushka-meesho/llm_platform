# Production Deployment Guide — `llm_platform_go`

**Last updated: 2026-06-22**

This is the complete, self-contained deployment guide for the **`llm_platform_go`
backend** (the repo you are reading this in is the repo root). It assumes no access
to any parent-repo documentation: everything you need to take this Go service from a
laptop to production is here.

---

## 1. Overview

### What gets deployed

This repo ships **one artifact: the HTTP API backend** — a single static Go binary
(`./cmd/server`). It exposes:

- The **Studio/operator API** (`/auth/*`, `/run`, `/sessions`, `/dashboard`,
  `/v1/admin/*`, `/v1/tasks/*` authoring) consumed by human operators through a
  companion web UI.
- The **task-keyed product API** (`POST /v1/tasks/{task_id}/predict`, `GET /v1/tasks/...`)
  consumed by machine callers (service portals, CIS, batch jobs).

The companion **Studio (React/Vite operator UI)** and any **consumer client** are
**separate deployables in separate repos**. They are pure API consumers; this guide
does not deploy them. It only tells you the contract they must satisfy (origins,
cookies, service tokens) so they can talk to this backend.

There is **no file-based task seeding**. Tasks live in the database and are authored
at runtime via `POST /v1/tasks`. Only two built-in tasks are seeded at boot
(`playground` and `attribute-extraction`). Onboarding a new task is an API/UI action
against the running service, never a config rollout — so the database is the source
of truth and must be backed up like any other stateful store.

### The dev → prod gap in one paragraph

In dev the server runs permissively: `APP_ENV=dev`, SQLite in a local file,
passwordless "pick-a-user" demo login (`AUTH_MODE=demo`), an insecure default
`JWT_SECRET`, non-secure cookies, CORS defaulting to the Vite dev origin
(`http://localhost:5173`), and schema migrations applied automatically on every boot.
In prod you flip `APP_ENV=prod`, at which point `config.Validate()` becomes a hard boot
gate (§3) that **refuses to start** unless you supply a strong `JWT_SECRET`,
`AUTH_MODE=sso`, an explicit `ALLOWED_ORIGINS`, `COOKIE_SECURE=true`, and absolute
`DB_PATH`/`PRICING_PATH`. You move to Postgres for horizontal scale, run migrations
out-of-band (the prod server does **not** auto-migrate), terminate TLS at a load
balancer, inject secrets from a secret manager, and mint service tokens for machine
callers. The architecture was built so each of these is a contained config swap, not a
refactor.

---

## 2. Prerequisites

| Need | Detail |
|---|---|
| **Go toolchain** | Go **1.25+** (the module declares `go 1.25.0`). Only needed at build time; the runtime is a static binary. |
| **CGO** | **None.** The SQLite driver is `modernc.org/sqlite` (pure Go) and Postgres uses `jackc/pgx/v5/stdlib`. Build with `CGO_ENABLED=0` and ship in a distroless/scratch image. |
| **Database** | **PostgreSQL 14+** for production (real connection pool, concurrent writers, HA). SQLite is dev-only and forces a single replica. |
| **Redis** *(optional)* | Only if you enable the prediction cache (`REDIS_ADDR`). Without it, caching is off or in-process memory. Recommended once you run multiple replicas and want a shared cache. |
| **A provider key** | **At least one** of: `MEESHO_GATEWAY_VK` (Meesho's bifrost gateway — serves GPT-4o, Gemini, Claude over an OpenAI-compatible wire) and/or `GROQ_API_KEY` (direct Groq, for the `llama-groq` model). The server **refuses to boot with neither set**. |
| **Secret manager** | Vault / AWS Secrets Manager / k8s Secrets — anything that injects `JWT_SECRET` and provider keys as environment variables at deploy time. Never commit secrets. |
| **TLS / load balancer** | TLS terminates at an LB or ingress in front of the binary. The Go server speaks plain HTTP behind it. You need a hostname and certificate for the API origin (e.g. `api.example.com`). |
| **IdP (for humans)** | An OIDC provider for SSO operator login in prod (§6). |

---

## 3. Configuration reference

All configuration is via **environment variables** (12-factor). In dev you may put
them in a gitignored `.env` file at the repo root — `godotenv.Load()` reads it at
startup and is a silent no-op when the file is absent (exactly right in containers).
In prod, inject real env vars from your deployment system / secret manager.

`.env.example` in the repo root is the annotated source of truth and tracks these.

### 3.1 The production boot gate — what `config.Validate()` rejects

When `APP_ENV=prod`, the server calls `config.Validate()` immediately after
`config.Load()` and **`log.Fatal`s (exits non-zero) the boot** if any of these hold.
This is the single most important safety mechanism in the deployment — a misconfigured
prod server never starts rather than running unsafely:

| Rejected in prod when… | Error |
|---|---|
| `JWT_SECRET` is unset or equals the dev default `dev-insecure-secret-change-me` | `JWT_SECRET must be set to a strong non-default value` |
| `AUTH_MODE=demo` | `AUTH_MODE=demo is not allowed in prod (use sso); the passwordless login is dev-only` |
| `ALLOWED_ORIGINS` is empty | `ALLOWED_ORIGINS must list the frontend origin(s)` |
| `COOKIE_SECURE` is not `true` | `COOKIE_SECURE must be true (HTTPS-only session cookie)` |
| `DB_DRIVER=sqlite` and `DB_PATH` is **relative** | `DB_PATH must be an absolute path in prod` |
| `PRICING_PATH` is **relative** | `PRICING_PATH must be an absolute path in prod` |

These checks also run, regardless of environment (fast-fail driver sanity):
`DB_DRIVER` must be `sqlite` or `postgres`; `sqlite` requires `DB_PATH`; `postgres`
requires `DB_DSN`; `AUTH_MODE` must be `demo` or `sso`. And independent of `Validate()`,
`config.Load()` itself fails the boot if **no** provider key is set
(`no provider API keys set: configure at least one of GROQ_API_KEY, MEESHO_GATEWAY_VK`).

`cmd/bootstrap` runs the exact same `Validate()` and prints a pass/fail checklist, so
you can gate a deploy pipeline on it before the server ever starts (§4, §6).

### 3.2 Complete environment variable table

"Req in prod" = must be set to a safe value or the prod boot gate / loader fails.

| Env var | Field | Default | Req in prod | Purpose |
|---|---|---|---|---|
| `APP_ENV` | `AppEnv` | `dev` | **yes** (`prod`) | `dev` = permissive; `prod` = enable the `Validate()` boot gate, skip inline migrations. |
| `PORT` | `Port` | `8000` | no | HTTP listen port (binds `:PORT`). |
| `LOG_LEVEL` | — | `info` | no | `debug` widens the structured `slog` JSON logger. |
| **Database** | | | | |
| `DB_DRIVER` | `DBDriver` | `sqlite` | **yes** (`postgres`) | `sqlite` (dev, single replica) or `postgres` (prod, scalable). |
| `DB_PATH` | `DBPath` | `./llm_platform.db` | if sqlite (**absolute**) | SQLite file path. Must be absolute in prod. |
| `DB_DSN` | `DBDSN` | empty | if postgres | Postgres connection string, e.g. `postgres://user:pass@host:5432/llm_platform?sslmode=require`. |
| **Pricing** | | | | |
| `PRICING_PATH` | `PricingPath` | `./pricing.json` | **yes** (**absolute**) | Path to the pricing JSON the server loads at boot for cost calc. Boot fails if it can't load. |
| **Auth / session** | | | | |
| `JWT_SECRET` | `JWTSecret` | `dev-insecure-secret-change-me` | **yes** (strong) | HS256 signing key for session + service tokens. Use `openssl rand -hex 32` or `cmd/bootstrap`. |
| `AUTH_MODE` | `AuthMode` | `demo` | **yes** (`sso`) | `demo` = passwordless pick-a-user (dev only, routes only registered in demo mode). `sso` = OIDC. |
| `AUTH_COOKIE_NAME` | `AuthCookieName` | `llm_platform_token` | no | Session cookie name. |
| `AUTH_ISSUER` | `AuthIssuer` | `llm-platform-demo` | recommended | JWT `iss` claim. Set to something environment-specific, e.g. `llm-platform-prod`. |
| `TOKEN_EXPIRY` | `TokenExpiry` | `12h` | recommended (shorter) | Session token lifetime. Consider `8h` in prod. |
| **Cookies (cross-origin)** | | | | |
| `COOKIE_DOMAIN` | `CookieDomain` | empty | if cross-origin | Cookie domain. Empty = host-only. Set to the shared parent (`.example.com`) for the subdomain topology (§7). |
| `COOKIE_SECURE` | `CookieSecure` | `false` | **yes** (`true`) | `Secure` flag — cookie only sent over HTTPS. |
| **OIDC (when `AUTH_MODE=sso`)** | | | | |
| `OIDC_ISSUER` | (read directly) | empty | yes for SSO | IdP issuer URL. |
| `OIDC_CLIENT_ID` | (read directly) | empty | yes for SSO | OAuth client id. |
| `OIDC_CLIENT_SECRET` | (read directly) | empty | yes for SSO | OAuth client secret (from secret manager). |
| `OIDC_REDIRECT_URL` | (read directly) | empty | yes for SSO | Callback URL, e.g. `https://api.example.com/auth/sso/callback`. |
| `OIDC_POST_LOGIN_URL` | (read directly) | empty | yes for SSO | Where to land the browser after login, e.g. the Studio origin `https://studio.example.com`. |
| **CORS** | | | | |
| `ALLOWED_ORIGINS` | `AllowedOrigins` | empty → `http://localhost:5173` | **yes** | Comma-separated frontend origin allowlist. In prod set to the exact Studio origin(s). |
| **Providers** | | | | |
| `MEESHO_GATEWAY_VK` | `MeeshoGatewayVK` | empty | one-of | Virtual key for the Meesho bifrost gateway (GPT-4o, Gemini, Claude). |
| `GROQ_API_KEY` | `GroqKey` | empty | one-of | Groq API key (the `llama-groq` model, called directly). |
| `MEESHO_GATEWAY_BASE_URL` | `MeeshoGatewayBaseURL` | `http://llm-gateway.prd.meesho.int/v1` | no | Gateway base URL override. |
| `GROQ_BASE_URL` | `GroqBaseURL` | `https://api.groq.com/openai/v1` | no | Groq base URL override (proxy). |
| **Prediction cache** | | | | |
| `REDIS_ADDR` | `RedisAddr` | empty | recommended (multi-replica) | Redis address. Set → cache backend = `redis`. |
| `REDIS_PASSWORD` | `RedisPassword` | empty | no | Redis auth. |
| `REDIS_DB` | `RedisDB` | `0` | no | Redis DB number. |
| `CACHE_BACKEND` | `CacheBackend` | derived | no | Force `redis` / `memory` / `off`. Derived: `redis` if `REDIS_ADDR` set, else `off`. `memory` = in-process (dev/single-replica only). |
| **Health breaker** (per task+model) | | | | |
| `HEALTH_BREAKER_ENABLED` | `HealthBreakerEnabled` | `true` | no | Master on/off for the per-(task,model) circuit breaker. |
| `HEALTH_FAILURE_THRESHOLD` | `HealthThreshold` | `3` | no | Consecutive failures before a model is skipped for that task. |
| `HEALTH_BASE_COOLDOWN` | `HealthBaseCooldown` | `30s` | no | First cooldown window after tripping. |
| `HEALTH_MAX_COOLDOWN` | `HealthMaxCooldown` | `30m` | no | Cap on the ×2 exponential backoff. |
| **Rate limiter** (per task, rolling window) | | | | |
| `RATE_LIMIT_ENABLED` | `RateLimitEnabled` | `true` | no | Master on/off. |
| `RATE_WINDOW` | `RateWindow` | `1m` | no | Rolling window length per task. |
| `RATE_MAX_REQUESTS` | `RateMaxRequests` | `600` | no | Max requests per task per window (0 = unlimited). → `429` + `Retry-After`. |
| `RATE_MAX_TOKENS` | `RateMaxTokens` | `200000` | no | Max tokens consumed per task per window (0 = unlimited). → `429` + `Retry-After`. |
| `RATE_MAX_INPUT_TOKENS` | `RateMaxInputTokens` | `16000` | no | Max estimated input tokens for a single request (0 = unlimited). → `413 Payload Too Large`. |
| `RATE_CHARS_PER_TOKEN` | `RateCharsPerToken` | `4` | no | Chars-per-token for the cheap input estimate. |
| `RATE_TOKENS_PER_IMAGE` | `RateTokensPerImage` | `1000` | no | Flat token cost added per attached image for estimation. |

Durations accept Go syntax: `30s`, `5m`, `1h30m`, `8h`.

### 3.3 Example production env

```bash
APP_ENV=prod
PORT=8000

# Database — Postgres in prod
DB_DRIVER=postgres
DB_DSN=postgres://llm:••••••@db.internal:5432/llm_platform?sslmode=require

# Pricing — absolute path required in prod
PRICING_PATH=/etc/llm-platform/pricing.json

# Auth / session
JWT_SECRET=••••••••••••••••••••••••••••••••   # 32+ random bytes, from secret manager
AUTH_MODE=sso
AUTH_ISSUER=llm-platform-prod
TOKEN_EXPIRY=8h

# Cookies — cross-origin (Studio on a sibling subdomain)
COOKIE_DOMAIN=.example.com
COOKIE_SECURE=true

# OIDC
OIDC_ISSUER=https://idp.meesho.internal
OIDC_CLIENT_ID=llm-platform
OIDC_CLIENT_SECRET=••••••••
OIDC_REDIRECT_URL=https://api.example.com/auth/sso/callback
OIDC_POST_LOGIN_URL=https://studio.example.com

# CORS
ALLOWED_ORIGINS=https://studio.example.com

# Providers (from secret manager)
MEESHO_GATEWAY_VK=••••••••
# GROQ_API_KEY=••••••••

# Cache (recommended at multiple replicas)
REDIS_ADDR=redis.internal:6379
```

---

## 4. Database

### 4.1 SQLite (dev) vs Postgres (prod)

| | SQLite (`DB_DRIVER=sqlite`) | Postgres (`DB_DRIVER=postgres`) |
|---|---|---|
| Location var | `DB_PATH` (a file) | `DB_DSN` (connection string) |
| Concurrency | 1 writer (WAL allows concurrent readers) | Real pool; many concurrent writers |
| Pool tuning | `SetMaxOpenConns(1)`, `PRAGMA journal_mode=WAL`, `busy_timeout=5000`, `foreign_keys=ON` | `SetMaxOpenConns(20)`, `SetMaxIdleConns(5)`, `SetConnMaxLifetime(30m)`, pings on open |
| Replicas | **Forces 1 replica** (single local file) | Enables N replicas (shared state) |
| Backups | `cp llm_platform.db backup.db` | `pg_dump` / managed snapshots / WAL archiving |
| Use for | dev, tests, low-volume single instance | production, scale, HA |

The dialect seam lives entirely in `internal/db/` — `dialect.go` rewrites portable
`?` placeholders to `$1,$2,…` for Postgres and supplies the handful of
backend-specific SQL fragments (timestamp/date expressions, `ILIKE` vs
`LIKE COLLATE NOCASE`). `created_at` is stored as canonical `YYYY-MM-DD HH:MM:SS`
**text in both backends**, so scanning and ordering are identical. Handler code uses
plain `*sql.DB` and is dialect-agnostic.

> **Postgres validation caveat.** The Postgres path (driver switch, dialect-aware
> queries, idempotent schema in `internal/db/schema_postgres.go`) is **implemented but
> not yet validated against a live Postgres instance** in CI. Before trusting it in
> production, run the test suite and a smoke test with `DB_DRIVER=postgres` against a
> real server. Two known spots to confirm: numeric→float scans in the dashboard
> aggregates, and `shadow_reports.id` (the SQLite insert uses `LastInsertId`, which pgx
> doesn't support — switch to `INSERT … RETURNING id` if that ID is needed on PG). The
> image-count list view also reports presence-only on Postgres (1 if set) rather than
> an exact multi-image count. SQLite is fully tested and is the default.

### 4.2 Provisioning Postgres

1. Create the database and a least-privilege role:
   ```sql
   CREATE DATABASE llm_platform;
   CREATE USER llm WITH PASSWORD '••••••';
   GRANT ALL PRIVILEGES ON DATABASE llm_platform TO llm;
   ```
2. Require TLS in the DSN (`sslmode=require` or stricter).
3. Put the DSN in `DB_DSN` via the secret manager.

### 4.3 Running migrations — `cmd/migrate` and `cmd/bootstrap`

**The prod server does NOT auto-migrate.** In dev (`APP_ENV=dev`) the server runs
`db.Migrate()` inline on every boot for zero-friction local startup. In prod it logs
`prod: skipping inline migrations` and serves immediately — so you must apply the
schema **out-of-band before the new build serves traffic**. This keeps a rolling
deploy from blocking on, or racing, a schema change.

```sh
# Apply schema for the configured backend, then exit. Reads the same env/.env
# as the server.
DB_DRIVER=postgres DB_DSN='postgres://llm:•••@db:5432/llm_platform' go run ./cmd/migrate
# or, with the built binary:  ./migrate
```

`cmd/bootstrap` is the once-per-environment first-run tool. In order it:
1. Generates a strong `JWT_SECRET` (crypto/rand) if one isn't set — prints it, and
   with `-write-env` appends it to the env file.
2. Runs `config.Validate()` and prints a pass/fail checklist (exits non-zero on any
   failure, so it can gate a pipeline).
3. Runs migrations against the configured backend.
4. For SQLite, locks down the DB file (`0600`, ensures parent dir, warns if the file
   sits inside the working dir / web root).
5. With `-issue-admin`, mints a break-glass admin token (default 720h TTL) so the
   platform is reachable before SSO is fully wired.

```sh
APP_ENV=prod DB_DRIVER=postgres DB_DSN='postgres://…' \
  go run ./cmd/bootstrap -write-env -issue-admin -admin-email you@meesho.com
```

### 4.4 The additive, guarded-ALTER migration model

Migrations are **idempotent and additive** — safe to run on every deploy:

- **SQLite** (`migrateSQLite`): `CREATE TABLE IF NOT EXISTS` for all tables/indexes,
  plus a list of `ALTER TABLE … ADD COLUMN` statements wrapped in a guard that ignores
  the `duplicate column` error. Existing databases upgrade in place. A backfill
  populates `prompt_versions` from active tasks via `INSERT OR IGNORE`.
- **Postgres** (`migratePostgres`): every statement uses `CREATE … IF NOT EXISTS` /
  `ADD COLUMN IF NOT EXISTS`, with Postgres-native types (`BIGINT GENERATED ALWAYS AS
  IDENTITY`, `DOUBLE PRECISION`), mirroring the SQLite column set.

**Consequence for operations:** new columns are always added, never dropped or
renamed, and always have defaults. This is what makes binary rollback safe (§11): an
older binary simply ignores columns it doesn't know about.

### 4.5 Backup & restore

- **Postgres:** schedule `pg_dump` (or managed snapshots) and enable
  WAL archiving / PITR. The database holds **all task definitions, prompt version
  history, runs, traces, and feedback** — there is no file-based task config to back up
  separately. Treat task loss as data loss.
- **SQLite (dev):** `cp llm_platform.db backup.db` while the process is idle, or use
  `sqlite3 .backup`.
- **Restore:** restore the dump, run `cmd/migrate` to ensure the schema is current,
  then start the server.

### 4.6 The tables (what you're backing up / observing)

| Table | One row per | Notes |
|---|---|---|
| `runs` | prediction answer served to the caller | full prompt/response, model, latency, tokens, cost, `provider`, `fallback_used`, `cache_hit`, `is_test`, `user_id/email`, `task_id`, `prompt_version`. |
| `gateway_attempts` | every model the fallback walk touched | the full trace behind a run: `seq`, `outcome` (`success`/`error`/`schema_invalid`/`skipped_unhealthy`/`cache_hit`), `fallback_reason`, `http_status`, `infra_failure`, `retry_count`, per-call latency/tokens/cost. |
| `model_health_events` | circuit-breaker state transition | `event` (`failure`/`tripped`/`recovered`/`manual_reset`), `consecutive_failures`, `state`. |
| `tasks` | task config | full prompt template, models, budget, input limits, cache opt-in. |
| `prompt_versions` | one historical prompt snapshot | version history per task. |
| `feedback` | (run_id, model, user_id) rating | UPSERT on re-rate. |
| `shadow_reports` | one shadow-comparison report | match rate, latency, cost aggregates. |

The three hot-path tables (`runs`, `gateway_attempts`, `model_health_events`) are
written via **async batched writers** that drop rows (incrementing a counter) rather
than ever blocking a prediction. They are flushed on graceful shutdown (§8).

---

## 5. Build & package

### 5.1 Static binary

No cgo anywhere — build a fully static binary:

```sh
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /llm-platform ./cmd/server
```

Build the helper commands the same way if you ship them in the image:
`./cmd/migrate`, `./cmd/bootstrap`, `./cmd/issue-token`.

### 5.2 `pricing.json` placement

The server loads the pricing table at boot from `PRICING_PATH` and **fails to start if
it can't be loaded**. In prod `PRICING_PATH` must be **absolute**. Ship `pricing.json`
into the image (or mount it) and point `PRICING_PATH` at it.

### 5.3 Sample multi-stage Dockerfile (API only)

```dockerfile
# ---- build ----
FROM golang:1.25 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /llm-platform ./cmd/server \
 && CGO_ENABLED=0 go build -trimpath -o /migrate ./cmd/migrate

# ---- runtime (distroless, static) ----
FROM gcr.io/distroless/static-debian12
COPY --from=build /llm-platform /llm-platform
COPY --from=build /migrate /migrate
COPY pricing.json /etc/llm-platform/pricing.json
ENV PRICING_PATH=/etc/llm-platform/pricing.json
EXPOSE 8000
ENTRYPOINT ["/llm-platform"]
```

This image is **API-only** — the Studio UI is a separate deployable (static assets on
its own host/CDN). Run `/migrate` as a pre-deploy job (init container / pipeline step)
before rolling the new server, since the prod server does not auto-migrate.

---

## 6. Auth for production

Two principal types, one token mechanism (HS256 JWT signed with `JWT_SECRET`).

### 6.1 Humans → SSO / OIDC (`AUTH_MODE=sso`)

In prod `AUTH_MODE=sso` is **required** (the `Validate()` gate rejects `demo`). In sso
mode the demo pick-a-user routes (`GET /auth/demo-users`, `POST /auth/login`) are **not
even registered** — there is no unauthenticated session-mint path. Instead the router
registers:

- `GET /auth/sso/login` — begins the IdP login (redirect to the provider).
- `GET /auth/sso/callback` — IdP redirects back with `?code&state`.
- `POST /auth/logout`, `GET /auth/me` — unchanged.

The handlers live in `internal/api/auth_sso_handlers.go`. They read these env vars:
`OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, `OIDC_REDIRECT_URL`,
`OIDC_POST_LOGIN_URL`. **Until the IdP handshake is completed, the SSO routes return
`501 Not Implemented`** — the route exists and is documented but cannot mint a session
from unvalidated input. Completing it means: build the authorize URL + signed `state`
(CSRF) in `SSOLogin`; in `SSOCallback` verify state, exchange the code, validate the ID
token, map claims (sub/email/name + groups → RBAC role), resolve the user via a real
`users.Store`, then call the shared `issueSession` and redirect to
`OIDC_POST_LOGIN_URL`. The session-cookie tail is identical to the demo path, so the
rest of the platform is unaffected. A standard implementation uses
`golang.org/x/oauth2` + `github.com/coreos/go-oidc`.

**Bridge while SSO is being wired:** mint a break-glass admin token with
`cmd/bootstrap -issue-admin` (§4.3) and use it as a `Bearer` token for the Studio API.

### 6.2 Machines → service tokens (`cmd/issue-token`)

Machine callers (consumer portals, CIS, batch jobs) authenticate with long-lived
issued JWTs. Mint one per consumer with `cmd/issue-token`:

```sh
go run ./cmd/issue-token -sub svc:cis -email cis@svc.local -name "CIS" -role client -ttl 8760h
```

- The token prints on stdout; pass it as `Authorization: Bearer <token>`.
- **Roles are exactly two:** `admin` (Studio operators — every capability) and
  `client` (service callers — `task:read` + `task:predict` only; **never see prompt
  text**). Scope machine callers to `client` (least privilege).
- Convention: service subjects are prefixed `svc:` so per-caller usage is distinct in
  run attribution and dashboards.
- `cmd/issue-token` needs `JWT_SECRET` in env / `.env`; run it from a controlled admin
  host, prefer short TTLs with scheduled re-issue over year-long tokens, and record
  issued tokens (subject, expiry) in an audit log.

### 6.3 Demo mode disabled in prod

`AUTH_MODE=demo` is a hard boot failure under `APP_ENV=prod`. The passwordless login,
the `/auth/demo-users` route, and the in-memory `DemoStore` exist purely for local dev.

---

## 7. Networking

### 7.1 Reverse proxy / LB and HTTPS

Run the binary behind a load balancer or ingress that **terminates TLS**. The Go server
speaks plain HTTP on `:PORT` (default `8000`); it sets HTTP timeouts (§8) but does not
do TLS itself. Point a public hostname (e.g. `api.example.com`) at the LB with a valid
certificate.

### 7.2 The cross-origin cookie contract

The recommended topology is **sibling subdomains of one parent domain**:

| | Backend (`llm_platform_go`) | Studio (separate repo) |
|---|---|---|
| Origin | `https://api.example.com` | `https://studio.example.com` |
| Set | `ALLOWED_ORIGINS=https://studio.example.com` | points fetch at `https://api.example.com` |
| Set | `COOKIE_DOMAIN=.example.com` | |
| Set | `COOKIE_SECURE=true` | |

Because the two origins share the registrable domain `example.com`, they are
**same-site**, so the session cookie's `SameSite=Lax` works across them once it is
scoped to the parent domain. The CORS middleware (`internal/api/router.go`) already
sends `Access-Control-Allow-Credentials: true`, allows `GET/POST/PUT/DELETE/OPTIONS`
and the `Accept`/`Content-Type`/`Authorization` headers, and the Studio fetch uses
`credentials: 'include'` — so the cookie flows once the three backend vars above are
set.

Key facts:
- `ALLOWED_ORIGINS` is the **exact** CORS allowlist. Empty falls back to the Vite dev
  origin (`http://localhost:5173`) — fine for dev, rejected by the prod gate.
- `COOKIE_DOMAIN` empty = host-only cookie (only works same-origin). Set the shared
  parent for the subdomain topology.
- `COOKIE_SECURE=true` makes the cookie HTTPS-only (required in prod).
- The **Studio origin** is what you list in `ALLOWED_ORIGINS` and (typically) in
  `OIDC_POST_LOGIN_URL`.

If infra forces a fully cross-*site* split (different registrable domains), you'd need
`SameSite=None; Secure` and a shared `COOKIE_DOMAIN` won't help — prefer the
same-parent-domain topology to avoid that complexity.

---

## 8. Health & readiness

| Endpoint | Meaning | Behaviour |
|---|---|---|
| `GET /health` | **Liveness** — process is up | Always `200 {"status":"ok","models_available":[…]}`. No DB touch. |
| `GET /ready` | **Readiness** — can serve traffic | Pings the DB (2s timeout). `200 {"status":"ready"}` on success; `503 {"status":"not_ready","reason":"database unreachable"}` otherwise. |

Both are public (no auth) so probes can reach them.

**Graceful shutdown:** on `SIGINT`/`SIGTERM` the server (1) stops accepting new
connections, (2) drains in-flight requests for up to **25s**, then (3) closes the three
async writers (`runWriter`, `attemptWriter`, `healthWriter`) so their buffered
observability rows flush rather than being dropped on exit. Order matters: HTTP drains
before the writers close.

**Suggested Kubernetes probes:**

```yaml
livenessProbe:
  httpGet: { path: /health, port: 8000 }
  initialDelaySeconds: 5
  periodSeconds: 10
readinessProbe:
  httpGet: { path: /ready, port: 8000 }
  periodSeconds: 5
  failureThreshold: 3
# Give shutdown time to drain (25s) + flush writers:
terminationGracePeriodSeconds: 35
```

---

## 9. Observability

The platform writes a rich observability trail to the database — this is your primary
signal source.

| Source | What it gives you |
|---|---|
| `runs` table | one row per served prediction: model, provider, latency, tokens, `cost_usd`, `success`, `fallback_used`, `cache_hit`, `is_test`, attribution. |
| `gateway_attempts` table | the full fallback-walk trace per run: every model tried, `outcome`, `fallback_reason`, `http_status`, `infra_failure`, `retry_count`, per-attempt latency/cost. This is where you see *why* a fallback happened. |
| `model_health_events` table | circuit-breaker transitions (`failure`/`tripped`/`recovered`/`manual_reset`) per (task, model). |
| `slog` JSON logs (stdout) | structured per-request logs keyed by request id; `LOG_LEVEL=debug` widens. A panic is logged with stack + the standard 500 envelope. |
| Admin API | `/v1/admin/runs`, `/v1/admin/model-health`, `/v1/admin/model-health/events` for live inspection. |

**Dashboards / alerts to wire** (most are SQL aggregates over `runs` /
`gateway_attempts` grouped by task/model/provider/day):

- **Latency** — p50/p95/p99 from `runs.latency_ms` and per-model from
  `gateway_attempts`.
- **Cost** — `sum(cost_usd)` per task/day; alert when a task approaches its
  `daily_budget_usd` (a budget of `0` means *exempt* — the documented escape hatch).
- **4xx / 5xx** — request error rate from logs / a metrics middleware.
- **429 / 413** — rate-limit rejections: `429` (request or token cap) and `413`
  (oversized input). A sustained climb means a caller needs higher limits or smaller
  inputs.
- **Fallback rate** — share of runs with `fallback_used=1`, or `gateway_attempts` with
  `seq>0` serving the answer. Rising fallback = the primary model is degrading.
- **Breaker trips** — count of `model_health_events.event='tripped'` per (task, model);
  alert on flapping.

Cheap future win (not present today): add `prometheus/client_golang` for request and
model-call histograms, a breaker-state gauge, and the writers' dropped-row counter on a
`/metrics` endpoint behind auth or an internal port.

**Log hygiene / retention:** the `runs` and `gateway_attempts` tables store full
prompts/responses by design. Before storing real seller data, agree a retention policy
(e.g. a nightly job that prunes `prompt`/`response` bodies after N days, keeping
aggregates), and never log `Authorization` headers, cookies, or full prompts at info
level.

---

## 10. Scaling & operations

- **The server is stateless.** All shared state lives in the **database**. With
  Postgres you can run N replicas behind the LB and scale horizontally.
- **SQLite forces a single replica** (one local-file writer). Stay at `replicas: 1`
  (with a PVC) while on SQLite; only enable an HPA after moving to Postgres.
- **Prediction cache:** `memory` is **per-process** — fine for one replica, but each
  replica has its own cache, so use **Redis** (`REDIS_ADDR`) for a shared cache across
  replicas. Caching is per-task opt-in (`tasks.cache_enabled`) regardless of backend.
- **Per-task rate limiter is per-instance.** Each replica keeps its own in-memory
  rolling-window state, so the effective limit is roughly `RATE_MAX_* × replica count`.
  If you need a hard global cap, divide the configured limits by the replica count, or
  centralize the limiter (a Redis-backed counter) later. Acceptable as a protective
  guardrail at modest replica counts; document the multiplier.
- **Circuit breaker is per-instance too.** Each replica tracks per-(task, model) health
  independently. Acceptable — centralize in Redis only if flapping across pods is
  observed.
- **Budget gate** reads the shared DB (correct), but read-then-spend across concurrent
  in-flight calls can overshoot a daily budget slightly (≤ concurrency × per-call cost).
  Acceptable; tighten with a reservation (`SELECT … FOR UPDATE`) only if real overshoot
  matters.
- **Async writers** are per-pod buffers that drop (and count) rows under extreme load
  rather than blocking predictions, and flush on graceful shutdown — ensure
  `terminationGracePeriodSeconds` covers the drain (§8).

---

## 11. Rollback

The additive, guarded migration model (§4.4) makes rollback safe:

- **Binary rollback is safe.** Migrations only ever *add* columns/tables with defaults
  and never drop or rename. An older server binary simply ignores columns it doesn't
  know about, so you can roll the deployment back to a previous image **without** a
  down-migration. There are no destructive schema migrations to reverse.
- **Don't roll the schema back.** Leave the (additive) schema in place; only roll the
  binary. A newer schema is forward- and backward-compatible with the binaries around
  it by construction.
- **Disable new behaviour via config, not code.** Most runtime behaviour is
  feature-flagged through env vars or task fields:
  - `HEALTH_BREAKER_ENABLED=false` — route every model regardless of recent failures.
  - `RATE_LIMIT_ENABLED=false` — disable all rate gating.
  - `CACHE_BACKEND=off` (or unset `REDIS_ADDR`) — turn off caching.
  - Per-task fields (edited live via the API): `cache_enabled`, `daily_budget_usd`
    (`0` = exempt), `active`, input-size limits, and the deployed `prompt_version`
    (re-deploy a prior version through `POST /v1/tasks/{task_id}/deploy`).
- A bad prompt deploy is reverted by re-deploying the previous version from
  `prompt_versions` — no code or schema change.

---

## 12. Pre-deploy checklist

**Config & secrets**
- [ ] `APP_ENV=prod` set.
- [ ] `JWT_SECRET` is a strong, non-default value from the secret manager (32+ bytes).
- [ ] `AUTH_MODE=sso` and the `OIDC_*` vars are set (or a break-glass admin token is
      issued as the bridge).
- [ ] `ALLOWED_ORIGINS` lists the exact Studio origin(s).
- [ ] `COOKIE_SECURE=true`; `COOKIE_DOMAIN` set for the subdomain topology.
- [ ] At least one provider key (`MEESHO_GATEWAY_VK` and/or `GROQ_API_KEY`) injected.
- [ ] `PRICING_PATH` is **absolute** and `pricing.json` is present.
- [ ] Ran `cmd/bootstrap` (or `config.Validate()` via a deploy gate) and it passed.

**Database**
- [ ] `DB_DRIVER=postgres`, `DB_DSN` set (TLS required), DB provisioned.
- [ ] `cmd/migrate` run as a pre-deploy step **before** the new server serves traffic.
- [ ] Validated against a live Postgres (suite + smoke) per the §4.1 caveat.
- [ ] Backups / PITR configured.

**Build & runtime**
- [ ] Static binary built with `CGO_ENABLED=0`.
- [ ] Image is API-only; Studio is deployed separately.

**Networking**
- [ ] TLS terminates at the LB/ingress; API hostname + cert in place.
- [ ] Cross-origin cookie contract verified (login from Studio works end to end).

**Health & ops**
- [ ] Liveness → `/health`, readiness → `/ready` probes wired.
- [ ] `terminationGracePeriodSeconds` ≥ 35 (covers the 25s drain + writer flush).
- [ ] `replicas: 1` + PVC if still on SQLite; HPA only after Postgres.
- [ ] Redis configured if running multiple replicas with caching.

**Observability**
- [ ] Latency / cost / 4xx-5xx / 429-413 / fallback-rate / breaker-trip dashboards and
      alerts wired.
- [ ] Log/data retention policy agreed before storing real data.

**Auth**
- [ ] Service tokens minted per machine caller, scoped to `client`, recorded in an
      audit log.
