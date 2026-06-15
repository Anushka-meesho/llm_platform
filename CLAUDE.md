# CLAUDE.md — LLM Platform

This file is context for Claude Code. It describes the project architecture, conventions, and critical constraints needed to make useful edits without breaking things.

---

## Project Layout

```
INTERN/
├── llm_platform_frontend/   React + TypeScript + Vite (port 5173)
├── llm_platform_go/         Go backend (port 8000)
└── llm_platform_v0/         Legacy Python — pending deletion, do not touch
```

---

## How to Run

Both services must run simultaneously.

**Backend** (from `llm_platform_go/`):
```bash
# Set BIFROST_VIRTUAL_KEY in .env, then:
go run ./cmd/server
```

**Frontend** (from `llm_platform_frontend/`):
```bash
npm install
npm run dev
```

Open `http://localhost:5173`. Vite proxies `/run`, `/sessions`, `/health` → `localhost:8000`.

---

## Common Commands

```bash
# Frontend
npm run dev        # dev server with HMR
npm run build      # tsc type-check + Vite bundle
npm run lint       # ESLint

# Backend
go run ./cmd/server   # run server
go build ./...        # compile check
go test ./...         # run all tests
```

---

## Environment Variables

Backend reads from `.env` (via godotenv) or the environment directly.

| Variable               | Required | Default                                    |
|------------------------|----------|--------------------------------------------|
| `BIFROST_VIRTUAL_KEY`  | yes      | —                                          |
| `BIFROST_URL`          | no       | `http://llm-gateway.prd.meesho.int/v1`     |
| `PORT`                 | no       | `8000`                                     |
| `DB_PATH`              | no       | `./llm_platform.db`                        |
| `PRICING_PATH`         | no       | `./pricing.json`                           |

---

## Architecture

### Frontend
- All state lives in `src/hooks/useChat.ts` (conversations, models, temperature, maxOutputTokens) and `src/hooks/useSessions.ts`
- LLM calls flow: `useChat.submitPrompt` → `src/api/client.ts` → `POST /run`
- Token estimation is client-side: `src/utils/tokens.ts` uses `js-tiktoken` with `cl100k_base` encoder (one shared instance, lazy-initialized)
- Components use `@meesho/merlin-ui-tailwind` — prefer Merlin components over raw HTML

### Backend
- `POST /run` fans out to all selected models concurrently via a buffered channel; results return fastest-first
- All provider calls route through a single **Bifrost LLM gateway** — no direct provider API keys. The `Provider` interface in `internal/llm/client.go` abstracts the gateway; `bifrostProvider` implements it
- Auth uses the `x-bf-vk` header (Bifrost virtual key) — not `Authorization: Bearer`
- Model IDs use provider-prefixed format: `openai/gpt-4o-mini`, `groq/llama-3.3-70b-versatile`, `anthropic/claude-3-5-sonnet-20241022`
- Retries and fallbacks are handled by the Bifrost gateway — no retry logic in this codebase
- SQLite stores every run; `session_id` groups multi-turn conversations (single writer — `MaxOpenConns=1`)

---

## Key Files

| File | Purpose |
|------|---------|
| `llm_platform_go/internal/llm/client.go` | `Provider` interface, `bifrostProvider` (single gateway client), shared types (`chatRequest`, `chatResponse`, `chatMessage`, `APIError`), `BuildClients()` |
| `llm_platform_go/internal/llm/runner.go` | Model `registry` (friendly name → Bifrost model ID), `StreamAll()` (concurrent fan-out), `callSingleModel()` |
| `llm_platform_go/internal/llm/pricing.go` | `CalculateCost()` — reads from `pricing.json` at startup |
| `llm_platform_go/pricing.json` | Per-model token rates (input/output per 1M tokens) — edit this to update pricing |
| `llm_platform_go/internal/types/request.go` | `RunRequest` struct — must stay in sync with frontend `TRunRequest` |
| `llm_platform_go/internal/types/response.go` | `ModelResultResponse` struct — must stay in sync with frontend `TModelResult` |
| `llm_platform_frontend/src/types/index.ts` | All TypeScript types + `MODELS` const |
| `llm_platform_frontend/src/hooks/useChat.ts` | Central frontend state; contains `submitPrompt` which calls the API |
| `llm_platform_frontend/src/utils/tokens.ts` | `countTokens()`, `estimateCost()`, `PRICING` table, `formatCost()` |

---

## Type Contract (critical)

The Go types and TypeScript types must stay in sync — a mismatch silently breaks the app.

| Go (`internal/types/`) | TypeScript (`src/types/index.ts`) |
|------------------------|-----------------------------------|
| `RunRequest` | `TRunRequest` |
| `ModelResultResponse` | `TModelResult` |
| `RunResponse` | `TRunResponse` |

JSON field names are `snake_case` on the wire; Go struct tags enforce this.

---

## Adding or Changing Models

All models route through the Bifrost gateway — no per-provider wiring needed.

### Add any model (any provider supported by Bifrost)
1. `llm_platform_go/internal/llm/runner.go` — add entry to `registry`: `"friendly-name": "provider/actual-model-id"` (e.g. `"gpt-4o": "openai/gpt-4o"`)
2. `llm_platform_go/pricing.json` — add `"friendly-name": { "input_per_1m": X, "output_per_1m": Y, "context_window": N, "max_output_tokens": N }`
3. `llm_platform_frontend/src/types/index.ts` — add name to `MODELS` const
4. `llm_platform_frontend/src/utils/tokens.ts` — add pricing entry to `PRICING`, `CONTEXT_WINDOWS`, and `MAX_OUTPUT_TOKENS` tables

The virtual key must be authorized for the model/provider in the Bifrost dashboard.

---

## Frontend Conventions

- **Merlin UI**: always use `Button`, `TextArea`, `Spinner`, `Typography` from `@meesho/merlin-ui-tailwind` before reaching for raw HTML
- **Tailwind v4**: uses `@tailwindcss/vite` plugin — no `purge` config needed. Design tokens: `bg-primary-bg`, `bg-secondary-bg`, `text-primary-text`, `text-secondary-text`, `text-tertiary-text`, `border-primary-border`
- **TypeScript strict mode** is on — no `any`, no implicit nulls
- State hooks (`useChat`, `useSessions`) own all async logic; components are presentational

---

## NEVER DO

- **Do not commit `.env`** — API keys must stay out of git (`.gitignore` already excludes it)
- **Do not import `github.com/sashabaranov/go-openai`** — it was intentionally removed; use the `Provider` interface
- **Do not change `MaxOpenConns` to > 1** on the SQLite DB — SQLite is single-writer; this will cause locking errors
- **Do not rename keys in `pricing.json`** without updating `pricing.go` — they are loaded by exact key name
- **Do not add `node_modules/` or `*.db` files to git** — covered by `.gitignore`
- **`llm_platform_v0/`** is pending deletion — do not build on it or reference it
