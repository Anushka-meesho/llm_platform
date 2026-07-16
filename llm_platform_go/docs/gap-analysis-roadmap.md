# Gap Analysis & Roadmap — Current Backend vs. Prediction Factory Vision

**Status:** Living document, updated against the current `llm_platform_go` implementation.
**Scope:** Backend platform capabilities, what has shipped, what is deliberately deferred, and where future work should land.

---

## What has shipped now

| Capability | Status | Key files |
|------------|--------|-----------|
| Task registry + CRUD | Shipped | `internal/tasks/store.go`, `internal/tasks/task.go`, `internal/api/task_handlers.go` |
| Built-in task seeding | Shipped | `internal/tasks/seed.go`, `cmd/server/main.go` |
| Production predict endpoint | Shipped | `internal/api/predict_core.go`, `internal/api/router.go` |
| Request-body schema registry | Shipped | `internal/schema/registry.go`, `internal/schema/requests/*.yaml` |
| Input schema validation | Shipped | `internal/tasks/validate.go` |
| Output schema coercion + validation | Shipped | `internal/tasks/validate.go`, `internal/llm/fallback.go` |
| Prompt template rendering | Shipped | `internal/tasks/render.go` |
| Prompt versions, drafts, deploy, delete | Shipped | `internal/tasks/versions.go` |
| Fallback chain | Shipped | `internal/llm/fallback.go` |
| Per-task/model health tracker | Shipped | `internal/health/tracker.go` |
| Gateway attempt tracing | Shipped | `internal/db/attemptwriter.go`, `gateway_attempts` |
| Async writers | Shipped | `internal/db/runwriter.go`, `attemptwriter.go`, `healthwriter.go` |
| Per-task rate limiter | Shipped | `internal/ratelimit/limiter.go` |
| Daily budget gate + spend cache | Shipped | `internal/api/budget_cache.go`, `internal/api/predict_core.go` |
| Prediction cache | Shipped | `internal/cache/` |
| Eval datasets/checks/runs | Shipped foundation | `internal/tasks/eval.go`, `internal/api/eval_handlers.go` |
| Shadow compare summaries | Shipped foundation | `shadow_reports` table and shadow routes |
| JWT auth + HttpOnly cookie | Shipped | `internal/auth/auth.go`, `internal/api/auth_handlers.go` |
| RBAC permission seam | Shipped admin-only | `internal/auth/rbac.go` |
| Multimodal requests | Shipped | `internal/llm/client.go`, `internal/api/predict_core.go` |
| Gemini thinking response handling | Shipped | `internal/llm/client.go` |
| Admin run/model-health APIs | Shipped | `internal/api/admin_handlers.go`, `internal/api/router.go` |

Important correction: there is **no provider-wide circuit breaker** in the current backend. The live safety mechanism is the per-`(task_id, model)` health tracker.

---

## Executive Summary

The backend is no longer just a playground skeleton. It is a working task-based prediction gateway:

- product traffic calls `/v1/tasks/{id}/predict`;
- the task owns prompt, schemas, model chain, cache settings, and budget;
- the fallback chain records every model touched;
- output schemas are enforced as part of routing;
- eval datasets and run summaries exist, although deploy gating is still manual/advisory.

The largest remaining gaps are production hardening and enterprise workflow depth: real SSO, real multi-role RBAC, Postgres for multi-instance deployments, graceful shutdown/timeouts, audit logs, hard eval deploy gates, async/batch modes, RAG, Kafka/warehouse export, and alerting.

Build-vs-buy remains decided for the current repo: **pure Go, single binary, in-house gateway logic**. No LiteLLM/Langfuse service is in the hot path.

---

## Current Maturity by Plane

### API Gateway

| Function | Current state | Gap |
|----------|---------------|-----|
| UI auth | Demo JWT login with cookie | Replace `users.DemoStore` with real IdP/SSO |
| Service auth | Bearer JWT accepted | Service-principal lifecycle and rotation policy |
| RBAC | Permission middleware wired; admin role only | Add creator/approver/caller/viewer roles |
| Request validation | Embedded YAML JSON Schemas for request bodies | Keep schema coverage tests as endpoints grow |
| Rate limiting | Per-task rolling request/token/input gates | Distributed limiter if multiple instances need strict global limits |
| Sync predict | Implemented | Add async/callback mode if callers need long-running jobs |

### Prompt Plane

| Function | Current state | Gap |
|----------|---------------|-----|
| Task registry | DB-backed CRUD + 5s config cache | Approval workflow and audit log |
| Prompt versions | Draft/save/deploy/delete implemented | Hard deploy gate tied to eval thresholds |
| Template renderer | Go `text/template` | More guardrails for template preview/diff UX |
| Built-in tasks | `playground`, `attribute-extraction` seeded | More reusable starter templates if needed |

### Execution Plane

| Function | Current state | Gap |
|----------|---------------|-----|
| Provider abstraction | OpenAI-compatible provider for Groq + Meesho gateway | Native provider clients only if OpenAI-compatible wire is insufficient |
| Fallback chain | Ordered model walk with cache, health, output validation | Optional policy tuning per task |
| Health tracker | Per-task/model circuit breaker | Provider-wide breaker only if broad outages become noisy |
| Prediction cache | Redis/memory/off, per-model key | Cache metrics and distributed invalidation observability |
| Rate/budget safety | Rolling limiter + daily budget | Distributed strict budget counter for multi-instance prod |

### Eval Plane

| Function | Current state | Gap |
|----------|---------------|-----|
| CSV/XLSX upload | Implemented with mappings and schema validation | UI polish and larger dataset strategy |
| Prism SQL registration | Metadata registered as `pending_import` | Prism connector execution/import |
| Prompt-version checks | Implemented; exact/deep comparison summary | More scorers beyond structural/exact matching |
| Eval run records | Persisted in `eval_runs` | Hard deploy gate and threshold policy |

### Observe Plane

| Function | Current state | Gap |
|----------|---------------|-----|
| Runs | Async write with task/user/model/cost metadata | Postgres/Kafka when write volume grows |
| Gateway attempts | Full fallback trace per prediction | Retention policy and redaction controls |
| Health events | Async event write | Alerting on trip rates |
| Dashboard/admin filters | Implemented | Grafana/warehouse export |

### Feedback Plane

| Function | Current state | Gap |
|----------|---------------|-----|
| Explicit rating | 1-5 star feedback per run/model/user | Add verdict, corrected output, source, reviewer |
| Implicit feedback | Not implemented | Caller integration and business outcome joins |
| Fine-tuning pipeline | Not implemented | Phase 3+ only |

---

## Roadmap

### Phase 0/1 — Core Prediction Gateway: mostly complete

- Task CRUD and DB-backed registry: complete.
- Production predict API: complete.
- Prompt versioning and deploy: complete.
- Fallback chain and schema-valid output routing: complete.
- Per-task/model health tracker: complete.
- Per-task limiter and budget gate: complete.
- Async run/attempt/health persistence: complete.
- Eval foundation: implemented, but not a hard release gate.

### Phase 2 — Production Hardening

1. Replace demo login with real SSO/IdP-backed `users.Store`.
2. Add real roles: creator, approver, caller, viewer.
3. Add append-only audit log for task/prompt/deploy/delete/admin actions.
4. Add graceful shutdown and HTTP server timeouts.
5. Add hard eval deploy gate: deploy only if a configured eval run passes thresholds.
6. Move from SQLite to Postgres before multi-instance production.
7. Add alerting for budget, health trips, p95 latency, and schema-invalid spikes.

### Phase 3 — Scale and Advanced Product Capabilities

1. Async prediction mode with callback, polling, retry, and DLQ.
2. Batch execution integration for offline jobs.
3. RAG service and retrieval contracts.
4. Kafka/warehouse sink for long-term analytics.
5. Implicit/automated feedback pipeline.
6. Priority lanes and queuing policy.
7. Fine-tuning dataset export and model-improvement workflow.

---

## Decisions Taken

| Decision | Chosen | Why |
|----------|--------|-----|
| Backend language | Go | Single binary, low memory, strong concurrency, simple deployment |
| Storage now | SQLite WAL | Zero local infra, simple migrations, enough for one instance |
| Storage later | Postgres | Required before multi-instance write-heavy production |
| Provider protocol | OpenAI-compatible HTTP | One client handles Meesho gateway, Groq, Gemini/Claude via gateway |
| Task source of truth | Database/API | Runtime self-serve task management; no startup-only `tasks.d` loader |
| Prompt safety | Versioned deploy | Prompt changes are code changes and need rollback |
| Output reliability | Schema-valid fallback contract | Bad structured output is treated as an unusable attempt |
| Cache key | Per model, not per chain | Chain changes should not serve stale fallback answers as primary answers |
| Observability | Async best-effort writers | User latency beats perfect trace persistence |
| Auth now | Demo JWT admin | Good local seam; not production identity |

---

## Checkable Success Criteria

- `attribute-extraction` predict path works with input/output schema enforcement.
- A schema-invalid primary response advances to fallback and records `schema_invalid` in `gateway_attempts`.
- Rate-limit violations return `413` or `429` with stable error codes and `Retry-After` where appropriate.
- Daily budget violations return `429 budget_exhausted`.
- Cache hits return zero cost and mark `cache_hit`.
- Eval datasets can be uploaded and prompt versions can be checked against them.
- Prompt deploy changes `tasks.prompt_version` and can be rolled back by deploying an older version.
- Admin run/debug APIs can explain why a prediction used a fallback.
