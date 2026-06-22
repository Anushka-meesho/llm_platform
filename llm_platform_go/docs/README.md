# LLM Platform — Go Backend

A production-ready backend that lets you call multiple LLM providers (OpenAI, Gemini, Groq, Anthropic via Meesho's gateway), compare their outputs, track costs down to the cent, and build reliable product features on top of them — with automatic failover, circuit breakers, prompt versioning, and caching.

---

## How it fits together

```mermaid
graph LR
    U([User / Browser]) -->|HTTP| F[React Frontend]
    F -->|REST API| G[Go Backend<br/>:8000]
    G -->|task predict| PC{Prediction<br/>Cache}
    PC -->|miss| FB[Fallback Chain]
    FB -->|primary| ME[Meesho Gateway<br/>GPT-4o · Gemini · Claude]
    FB -->|groq route| GR[Groq API<br/>Llama-3.3-70B]
    G -->|async| DB[(SQLite DB)]
    G -->|health probe| ME
    G -->|health probe| GR
```

The Go backend sits between the frontend and every LLM provider. It validates inputs, renders prompts, walks the fallback chain, caches results, logs every call, and enforces budgets — all so product features are reliable even when individual providers have outages.

---

## Quick start

```bash
# 1. Copy the sample env file
cp .env.example .env   # or edit .env directly — fill in your provider keys

# 2. Run the server (Go 1.25+ required)
go run ./cmd/server

# 3. Check it's alive
curl http://localhost:8000/health
# → {"status":"ok"}
```

The database (`llm_platform.db`) is created automatically on first run and seeded with two built-in tasks (`playground` and `attribute-extraction`). Tasks live in the database — the single source of truth — and are authored at runtime via the API, not from config files.

---

## Key vocabulary

| Term | Plain-English meaning |
|------|----------------------|
| **Task** | A named, versioned prediction configuration — input/output schema + prompt template + model routing. Like a named function that calls an LLM. |
| **Run** | One execution of one model call. If a task calls 3 models, that's 3 runs. |
| **Session** | A group of runs from the same user in the same conversation thread. |
| **Provider** | A company or gateway that serves LLM APIs (OpenAI, Google Gemini, Groq, Meesho bifrost). |
| **Fallback chain** | An ordered list of models to try. If the first fails, try the second, and so on. |
| **Circuit breaker** | An automatic switch that stops sending requests to a broken provider — like a fuse that blows before the wiring catches fire. |
| **Prompt version** | A numbered snapshot of a task's prompt template. You can draft new versions and deploy them like a software release. |
| **Cache hit** | When an identical request was already answered before, so the stored answer is returned — free of charge, instantly. |

---

## Learning guide

These docs explain every part of the codebase from scratch — no Go experience needed:

| File | What you'll learn |
|------|-------------------|
| [docs/00-big-picture.md](docs/00-big-picture.md) | Why this system exists, what problems it solves, and why Go/SQLite were chosen over the alternatives |
| [docs/01-server-startup.md](docs/01-server-startup.md) | How the server boots step-by-step, why order matters, and what happens if something fails |
| [docs/02-config.md](docs/02-config.md) | Every config field, every environment variable, and the 12-factor philosophy behind it |
| [docs/03-models-and-routing.md](docs/03-models-and-routing.md) | How model routing works, the Provider interface, why the registry map is a single source of truth |
| [docs/04-prediction-flow.md](docs/04-prediction-flow.md) | **The full lifecycle** of one prediction — from HTTP request to DB row |
| [docs/05-fallback-chain.md](docs/05-fallback-chain.md) | How the fallback walk decides which model to try next and when to give up |
| [docs/06-circuit-breaker.md](docs/06-circuit-breaker.md) | The two-layer circuit breaker system and the background recovery prober |
| [docs/07-tasks.md](docs/07-tasks.md) | Task anatomy, JSON Schema validation, Go prompt templates, and versioning |
| [docs/08-database.md](docs/08-database.md) | SQLite, WAL mode, every table, and why DB writes are done asynchronously |
| [docs/09-auth-and-rbac.md](docs/09-auth-and-rbac.md) | JWT authentication, HttpOnly cookies, and the two-role (admin/client) permission model |
| [docs/10-caching-and-cost.md](docs/10-caching-and-cost.md) | The prediction cache, what makes a cache key unique, and how token costs are calculated |

**Suggested reading order:** 00 → 01 → 03 → 04 → 05 → 06 → rest in any order.

---

## Directory map

```
llm_platform_go/
├── cmd/
│   ├── server/main.go         ← entry point — wires everything together
│   ├── bootstrap/main.go      ← first-run: gen JWT secret, validate, migrate, mint admin
│   ├── migrate/main.go        ← out-of-band schema migration (prod doesn't auto-migrate)
│   └── issue-token/main.go    ← CLI tool for minting auth tokens
├── internal/
│   ├── api/                   ← HTTP handlers, router, middleware
│   ├── llm/                   ← provider clients, registry, fallback, circuit breaker
│   ├── tasks/                 ← task config, validation, versioning, store
│   ├── health/                ← per-(task, model) circuit breaker tracker
│   ├── cache/                 ← prediction cache (Redis or memory)
│   ├── db/                    ← SQLite setup, migrations, queries, async writers
│   ├── auth/                  ← JWT, cookies, RBAC
│   ├── users/                 ← user store (demo swap seam)
│   ├── config/                ← environment variable loading
│   └── types/                 ← shared request/response types
├── pricing.json               ← per-model token pricing (USD per 1M tokens)
├── .env                       ← local secrets (gitignored — never commit this)
└── docs/                      ← this learning guide
```
