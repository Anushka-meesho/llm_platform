# LLM Platform — Backend

Go-based backend service for routing, running, and managing LLM predictions at scale. Built for the Meesho cataloging team.

## Tech Stack

| | |
|---|---|
| Language | Go 1.24 |
| Router | [Chi v5](https://github.com/go-chi/chi) |
| Database | SQLite 3 (WAL mode via `modernc.org/sqlite`) |
| Auth | JWT (`golang-jwt/jwt v5`) + RBAC |
| Cache | In-memory or Redis (`go-redis v9`) |
| LLM Provider | Anthropic SDK (`anthropics/anthropic-sdk-go v1.50.1`) |
| Schema Validation | JSON Schema (`santhosh-tekuri/jsonschema v6`) |

## Project Layout

```
cmd/
  server/main.go          — HTTP server entry point (:8000)
  issue-token/main.go     — CLI: generate JWT tokens for users

internal/
  api/                    — HTTP handlers, router, middleware
  llm/                    — Provider interface, fallback chain, circuit breaker
  tasks/                  — Task config, prompt versioning, template rendering
  health/                 — Per-task circuit breaker tracker
  cache/                  — In-memory and Redis prediction cache
  db/                     — SQLite schema, queries, async writers
  auth/                   — JWT parsing + RBAC middleware
  config/                 — .env loader
  users/                  — User store + demo data
  schema/                 — JSON Schema registry + request schemas (YAML)
  types/                  — Shared request/response DTOs

tests/                    — Integration tests
pricing.json              — Per-model token costs (USD per 1M tokens)
```

## Setup

### 1. Create environment file

Create a `.env` file in the project root:

```env
ANTHROPIC_API_KEY=your_anthropic_api_key_here
DB_PATH=./llm_platform.db
JWT_SECRET=your_jwt_secret_here
PORT=8000
REDIS_URL=                  # optional — omit for in-memory cache
```

| Variable | Required | Description |
|---|---|---|
| `ANTHROPIC_API_KEY` | Yes | Anthropic API key |
| `DB_PATH` | Yes | SQLite file path |
| `JWT_SECRET` | Yes | Secret for signing JWT tokens |
| `PORT` | No | HTTP port (default: `8000`) |
| `REDIS_URL` | No | Redis URL; omit to use in-memory cache |

### 2. Run the server

```bash
go run ./cmd/server
```

Server starts on `http://localhost:8000`.

### 3. Issue a JWT token (for testing)

```bash
go run ./cmd/issue-token
```

### 4. Run tests

```bash
go test ./...
```

## API Endpoints

| Method | Path | Auth | Description |
|---|---|---|---|
| `POST` | `/auth/login` | — | Exchange credentials for JWT |
| `POST` | `/run` | JWT | Run an LLM prediction |
| `GET` | `/v1/tasks` | JWT | List tasks |
| `POST` | `/v1/tasks` | JWT | Create a task |
| `GET/PUT/DELETE` | `/v1/tasks/:id` | JWT | Read / update / delete a task |
| `POST` | `/v1/tasks/:id/test` | JWT | Test a task prompt |
| `GET/POST` | `/v1/tasks/:id/versions` | JWT | List / save draft versions |
| `POST` | `/v1/tasks/:id/versions/:v/deploy` | JWT | Deploy a prompt version |
| `POST` | `/v1/shadow/compare` | JWT | A/B shadow comparison |
| `GET` | `/v1/admin/runs` | JWT (admin) | View run history |
| `GET` | `/health/models` | JWT | Circuit breaker status per task+model |
| `POST` | `/health/reset` | JWT (admin) | Reset circuit breaker |
| `POST` | `/feedback` | JWT | Submit run feedback / star rating |
| `GET` | `/pricing` | JWT | Per-model token pricing |
| `GET` | `/dashboard` | JWT | Usage dashboard stats |

## Related

- [Frontend repo](https://github.com/Meesho/cataloging_llm_platform-frontend)
