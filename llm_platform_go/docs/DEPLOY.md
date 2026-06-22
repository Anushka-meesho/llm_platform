# Deploying the LLM Platform

The platform splits into two independently-deployed repos:

| Repo | What it is | Serves |
|------|-----------|--------|
| `llm_platform_go` | Go API backend | the HTTP API on `api.<domain>` |
| `llm_platform_frontend` | Vite/React Studio (operator UI) | static assets on `studio.<domain>` |

`llm_platform_client` (the consumer portal) is a separate API consumer; deploy it
later the same way as the Studio. **Don't** carry `llm_platform.db` or a local
`.env` into the new repos.

Everything that differs per environment — DB location, backend URL, frontend
origin, secrets — is driven by environment variables, so moving environments is
an env-file edit, never a code change.

---

## Topology: subdomains of one parent domain

Frontend and backend live on sibling subdomains (e.g. `studio.example.com` and
`api.example.com`). Because they share the registrable domain `example.com` they
are *same-site*, so the existing `SameSite=Lax` session cookie works across them
with no code change — we only scope the cookie to the parent domain.

### Cross-origin contract

| Backend (`llm_platform_go`) | Frontend (`llm_platform_frontend`) |
|---|---|
| `ALLOWED_ORIGINS=https://studio.example.com` | `VITE_BACKEND_URL=https://api.example.com` |
| `COOKIE_DOMAIN=.example.com` | |
| `COOKIE_SECURE=true` | |

CORS already sends `Access-Control-Allow-Credentials: true` and the frontend
`fetch` uses `credentials:'include'`, so the cookie flows once the three backend
vars above are set.

---

## Backend configuration

Set `APP_ENV=prod`. In prod the server calls `config.Validate()` at boot and
**refuses to start** unless every safety invariant holds:

- `JWT_SECRET` is set to a strong, non-default value
- `AUTH_MODE=sso` (the passwordless demo login is dev-only and isn't even
  registered in sso mode)
- `ALLOWED_ORIGINS` lists the frontend origin
- `COOKIE_SECURE=true`
- `DB_PATH` (sqlite) / `PRICING_PATH` are absolute paths

See `.env.example` for the full annotated list. The provider keys
(`MEESHO_GATEWAY_VK` / `GROQ_API_KEY`) and gateway URLs are unchanged from dev.

### Database — SQLite or Postgres, by config

| `DB_DRIVER` | Location var | Notes |
|---|---|---|
| `sqlite` (default) | `DB_PATH` (a file) | single-writer (WAL); simplest ops; fine for low/moderate write volume |
| `postgres` | `DB_DSN` | real connection pool; needed for scale / HA |

Example DSN: `postgres://user:pass@host:5432/llm_platform?sslmode=require`.

> **Postgres status:** the Postgres path (driver switch, dialect-aware queries,
> idempotent schema in `internal/db/schema_postgres.go`) is implemented but has
> **not yet been validated against a live Postgres instance** — there was none
> available in the build environment. Before trusting it in production, run the
> suite and a smoke test with `DB_DRIVER=postgres` against a real server. Two
> known spots to confirm: numeric→float scans in the dashboard aggregates, and
> `shadow_reports.id` (the insert uses `LastInsertId`, which pgx doesn't support —
> switch to `INSERT … RETURNING id` if that ID is needed on PG). SQLite is fully
> tested and is the default.

---

## First run (per environment)

```sh
# 1. Bootstrap: generate JWT_SECRET, validate config, migrate, lock down SQLite,
#    and (optionally) mint a break-glass admin token.
APP_ENV=prod DB_DRIVER=postgres DB_DSN='postgres://…' \
  go run ./cmd/bootstrap -write-env -issue-admin -admin-email you@meesho.com

# 2. (If you skipped bootstrap's migrate, or on later schema changes) migrate
#    out-of-band before the new build serves traffic — the server does NOT
#    auto-migrate in prod.
go run ./cmd/migrate

# 3. Start the server.
go run ./cmd/server     # or run the built binary
```

`cmd/bootstrap` exits non-zero if any config check fails, so it can gate a deploy
pipeline.

---

## Authentication

- **Operators (humans)** authenticate via **SSO** (`AUTH_MODE=sso`). The IdP
  redirect/callback handlers live in `internal/api/auth_sso_handlers.go`; wire the
  `OIDC_*` env vars and complete the handshake there (the session-cookie tail is
  already shared with the demo path). Until wired, the SSO routes return `501`,
  and the break-glass admin token from `cmd/bootstrap` provides access.
- **Services (client portal, CIS, batch)** authenticate with **long-lived issued
  keys**: mint one per consumer, scoped to the least-privilege `client` role:

  ```sh
  go run ./cmd/issue-token -sub svc:cis -email cis@svc.local -role client -ttl 8760h
  ```

  Roles are `admin` (Studio operators, every capability) and `client` (service
  callers: read contract + predict only; never see prompts).

---

## Operational notes

- **Health vs readiness:** `GET /health` is liveness (process up); `GET /ready`
  pings the DB and returns `503` when it can't serve — point your load balancer /
  orchestrator readiness probe at `/ready`.
- **Graceful shutdown:** on SIGINT/SIGTERM the server stops accepting connections,
  drains in-flight requests, and flushes the async trace/run/health writers before
  exit, so buffered observability rows aren't lost.
- **HTTP timeouts** (read/write/idle) are set to bound slow-client resource use.

Containerization (Dockerfiles/compose) is intentionally deferred for now.
