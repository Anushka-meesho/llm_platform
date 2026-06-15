# Gap Analysis & Roadmap — Demo Codebase → LLM Platform Vision

**Status:** Draft for team review
**Scope:** Maps the current repo (`llm_platform_go` + `llm_platform_frontend`) against the platform design doc ("Prediction Factory"), and sequences the build. Section numbers (§) refer to the design doc.

---

## 1. Executive Summary

The current repo is a working **multi-model playground**: demo SSO login, fan-out prompt execution across OpenAI/Groq/Gemini, per-call token/cost/latency capture, star-rating feedback, client-side cost estimation, and a per-user usage dashboard. It was built as a UI-first demo, keyed by **user/session**.

The design doc describes a **prediction factory** keyed by **Task**. That is the single biggest structural gap: today the system answers *"what did this user spend?"*; Phase 1 must answer *"what does this task cost, at which prompt version, with what accuracy?"*

**Verdict in one line:** ~60% of the demo's backend survives as the skeleton of the API Gateway + Observe plane; the chat/session UI survives as the Prompt Studio's test panel; everything else in the doc (task registry, eval, RBAC roles, budgets, async/batch, RAG, cache) is greenfield.

**Build-vs-buy is decided: pure Go, single binary — no LiteLLM, no Langfuse** (see §6.1). All routing, prompt management, tracing, and eval are built in this repo.

---

## 2. Current State (what exists, with file references)

| Capability | Where | Maturity |
|---|---|---|
| JWT issue/parse/cookie auth | `llm_platform_go/internal/auth/auth.go` | Solid; HS256, Bearer + cookie |
| Swappable user store (demo SSO) | `internal/users/store.go`, `internal/users/demo.go` | Interface seam done; in-memory only by design |
| Auth middleware + login endpoints | `internal/api/middleware.go` (`RequireAuth`), `internal/api/auth_handlers.go` | Working; no roles, binary authn only |
| Provider abstraction + routing | `internal/llm/client.go` (`Provider` iface), `internal/llm/runner.go` (registry) | OpenAI-compatible HTTP, retries w/ backoff, error classification. **No fallback chain, no circuit breaker** |
| Concurrent fan-out execution | `internal/llm/runner.go` (`RunAll`) | Works; designed for N-model comparison, not single-task prediction |
| Cost calculation | `internal/llm/pricing.go` + `pricing.json` + `GET /pricing` | Static file; single source of truth shared with frontend |
| Inference logging | `internal/db/queries.go` → `runs` table (tokens, latency, cost, success, user, model) | SQLite. Missing: `task_id`, `prompt_version`, `cache_hit`, `fallback_used`, `provider`, TTFT |
| Feedback ingestion | `feedback` table, `POST /feedback`, upsert per (run, model, user) | 1–5 stars only; `run_id` ≈ doc's `trace_id` |
| Usage dashboard | `GET /dashboard` + `DashboardPage.tsx` | Per-**user**; needs per-**task** |
| Cost estimator | `EstimatePage.tsx` + `src/utils/tokens.ts` (js-tiktoken) | Single + batch, pre-call; maps directly to Prompt Studio's cost estimator |
| Multi-model test panel | Compare UI (`ComparePage`, `useChat`) | Multi-turn chat — richer than the doc needs; doc's unit is single-shot structured prediction |
| Demo SSO login UI | `LoginScreen.tsx`, `AuthContext.tsx` | One-click demo users; real SSO plugs in behind `users.Store` |

**Not started:** Task concept, RBAC roles/permission matrix, output schema enforcement, eval plane, golden datasets, semantic cache, RAG, async/callback/batch modes, rate limiting, budget enforcement, audit log, Kafka/warehouse sink, priority queuing.

---

## 3. Component-by-Component Gap Analysis

### 3.1 API Gateway (doc §3.1)

| Gateway function | Today | Gap | Effort |
|---|---|---|---|
| Auth (UI users, SSO) | ✅ JWT cookie via demo SSO; `users.Store` swap seam | Swap demo store for real IdP — by design a 1-line change in `main.go` | S |
| Auth (service JWT) | ⚠️ `RequireAuth` already accepts Bearer tokens | Need service-principal issuance (long-lived signed tokens or mTLS), distinct from UI sessions | M |
| RBAC per task | ❌ | Needs `task_roles` table + permission matrix (invoker/viewer/experimenter/deployer) checked in middleware. Blocked on Task concept | M |
| Rate limiting (token-aware) | ❌ | Per-task tokens/min counters (Redis). Straightforward once runs are task-keyed | M |
| Task config resolution | ❌ | The heart of the platform: `tasks` table + config cache. **Blocks almost everything else** | M |
| Priority queuing | ❌ | Phase 2+; not needed for shadow mode | L |
| Mode routing (sync/async/batch) | ❌ sync only | Async = run-store + worker + callback w/ HMAC; batch = scheduler integration | L |

### 3.2 Prompt Plane (doc §3.2)

| Sub-component | Today | Gap |
|---|---|---|
| Task Registry | ❌ | New: `tasks` table (id, name, input/output JSON schema, model pref + fallback chain, eval thresholds, budget, RBAC defaults), CRUD API, YAML import. **Phase 1 critical path** |
| Prompt Studio | ⚠️ partial | Compare UI ≈ "sample test panel"; Estimate page ≈ "cost estimator". Missing: prompt versioning, diffs, deploy button — all built in the existing React app (pure-Go/custom-UI decision, §6.1) |
| Template Renderer (Jinja2) | ❌ | Go equivalent: `text/template` or `pongo2` (Jinja2-compatible). Inputs validated against task input schema before render |
| RAG Service | ❌ | Phase 2+. ES KNN per doc decision. No code today; do not start in Phase 1 |

### 3.3 Execution Plane (doc §3.3)

| Sub-component | Today | Gap |
|---|---|---|
| Semantic cache | ❌ | Redis, key = SHA256(rendered_prompt + image + model). Add after task-keyed runs exist (cache key needs the *rendered* prompt) |
| Model Gateway | ⚠️ mini version | `Provider` iface + retries + error classification exist. Missing: fallback chain, circuit breaker, per-task attribution — built in Go (§6.1) |
| Output Parser + Schema Enforcer | ❌ | JSON-schema validation of responses (`santhosh-tekuri/jsonschema` in Go), provider-native structured output mode, one correction-prompt retry, DLQ table, type coercion |
| Batch Scheduler | ❌ | Phase 3. Airflow calls the same API; nothing to build in the hot path now |

### 3.4 Eval Plane (doc §3.4) — entirely greenfield

Nothing exists. Phase 1 needs only the **minimal slice**: golden dataset upload (JSONL), run prompt×model×dataset, exact-match/F1 scoring, leaderboard, threshold check stored per task. The hard deploy-gate (eval service independently verifying before deploy) is Phase 2. Note: the star-rating feedback we built is a *production* quality signal (§3.6), not an *eval* gate — don't conflate them.

### 3.5 Observe Plane (doc §3.5)

The `runs` table is a credible proto-sink. Gaps:

- **Schema:** add `task_id`, `prompt_version`, `provider`, `cache_hit`, `fallback_used`, `confidence`, TTFT. (Guarded-ALTER migration pattern already established in `internal/db/db.go`.)
- **Transport:** doc wants Kafka → warehouse, non-blocking. Today inserts are synchronous SQLite in the request path (`handlers.go` even swallows insert errors). Interim: buffered async writer; Kafka when infra exists.
- **Dashboards:** doc wants Grafana. Our React dashboard is fine for the demo and for the Phase 1 "cost visibility live" success metric — re-cut it per-task, defer Grafana.
- **Alert engine:** ❌ (budget 80%/100%, p95, error-rate). Needs budgets first.

### 3.6 Feedback Plane (doc §3.6)

| Signal | Today | Gap |
|---|---|---|
| Explicit | ⚠️ star ratings on `run_id` | Doc wants verdict (correct/incorrect) + **correction payload** (the right value). Extend `feedback` table: `verdict`, `corrected_output JSON`, `source` enum (qc/seller/automated) |
| Implicit / Automated | ❌ | Phase 2+; requires caller integration |
| Fine-tuning pipeline | ❌ | Phase 3+; explicitly out of early scope |

The `POST /v1/feedback` API shape (trace_id-linked) is a small evolution of what exists.

### Cross-cutting: RBAC (doc §5)

Today auth is binary (logged in or not). The 5-checkpoint model needs, in order: (1) roles on tasks → gateway check; (2) backend re-validation on mutations (frontend hiding is trivial); (3) eval-verified deploy gate; (4) budget enforcer; (5) `audit_log` append-only table. (1), (4), (5) are Phase 1–2; (3) lands with the eval plane.

### Cross-cutting: API surface (doc §4)

Current routes (`/run`, `/sessions`, `/feedback`, `/dashboard`) are playground-shaped. Target routes are task-shaped (`/v1/tasks/{id}/predict`, `/v1/tasks/runs/{run_id}`, `/v1/feedback`). **Keep both**: `/v1/tasks/*` becomes the product API; the existing routes remain as the Studio's internal playground API (the doc's test panel needs exactly what `/run` does).

---

## 4. Keep / Refactor / Replace / Discard

| Asset | Decision | Why |
|---|---|---|
| `internal/auth` + `users.Store` seam | **Keep** | Exactly the SSO/JWT seam the gateway needs; extend with roles |
| `internal/llm` Provider iface, retries, error classification | **Keep → Refactor** | Becomes either the in-house model gateway or the thin client in front of LiteLLM (§6) |
| `RunAll` fan-out | **Keep (Studio only)** | N-model comparison is an eval/Studio feature, not the prediction path. Prediction path is 1 task → 1 model (+fallback) |
| `runs` table + queries | **Refactor** | Re-key by `task_id`; add observability columns; keep user attribution as a secondary dimension |
| `feedback` table | **Refactor** | Add verdict/correction/source; keep upsert semantics |
| `pricing.json` + `/pricing` + Estimate page | **Keep** | Becomes Prompt Studio's cost estimator. Move client tokenizer caveat into per-model tokenizers later |
| Dashboard (API + page) | **Refactor** | Re-cut per-task with per-user drill-down |
| Compare UI (multi-turn chat) | **Keep as playground; do not let it shape the API** | Doc's unit is single-shot structured prediction; multi-turn `model_conversations` should not leak into `/v1/tasks/*` |
| Sessions concept (`/sessions`) | **Keep (Studio only)** | Useful for prompt iteration history; meaningless for service callers |
| SQLite | **Keep for dev; plan Postgres** | All DB access already funnels through `internal/db` functions; swap is contained. Single-writer SQLite cannot take production QPS |
| `llm_platform_v0` (Python) | **Discard / archive** | Superseded by the Go implementation |

---

## 5. Roadmap

### Phase 0 — Re-key around Task (≈1 week, prerequisite to everything)

1. `tasks` table + registry CRUD (`internal/tasks`): name, input/output JSON schemas, prompt template + version, model preference + fallback chain, thresholds, budget. YAML import.
2. Migrate `runs`: add `task_id`, `prompt_version`, `provider`, `fallback_used`, `cache_hit` (guarded ALTERs).
3. `POST /v1/tasks/{id}/predict` (sync): resolve config → validate input vs schema → render template → call provider → validate output vs schema → log → respond.
4. Re-cut dashboard per-task.
5. Register the existing playground as internal task(s) so the Compare UI keeps working unchanged.

### Phase 1 — the doc's "Prove It Works" (4 weeks)

| Doc ship item | Work in this repo |
|---|---|
| llm-gateway | Fallback chain + circuit breaker in `internal/llm` (pure Go — §6.1); service-JWT auth; per-task budget counter w/ 429 + `retry-after` |
| prompt-registry | Prompt versions table + activate/deploy endpoint; minimal "edit → test → deploy" screen in existing React app |
| observability-sink | Async buffered run-writer; per-task cost dashboard = the Phase 1 success metric ("cost visibility live") |
| thin UI | Evolve existing app: task list page, task detail (config + cost + runs), playground scoped to a task |
| One task: `attribute-extraction` | Define real input/output schema; structured-output mode + schema enforcer + correction-prompt retry; shadow-mode comparison report (platform vs. direct-Gemini accuracy/latency deltas) |
| Minimal eval | Golden dataset upload + exact-match scorer + leaderboard. Gate can be advisory (warn) in Phase 1, hard in Phase 2 |

**Explicitly deferred from Phase 1:** RAG, semantic cache, async/callback, batch, priority lanes, Kafka, Grafana, fine-tuning, implicit feedback. (Shadow mode for CIS prefill can run sync server-side; async-callback is needed before *canary*, not before *shadow*.)

### Phase 2 — Canary-readiness

Async mode + HMAC callbacks + DLQ + poll endpoint; hard eval deploy-gate; RBAC roles + audit log; semantic cache; rate limiting; alert engine; Postgres migration.

### Phase 3 — Scale-out

Batch (Airflow + provider Batch APIs), RAG service, priority queuing, Kafka → warehouse, implicit/automated feedback, fine-tuning pipeline.

---

## 6. Decisions Taken / Still Open

1. **✅ DECIDED (2026-06-12): Pure Go — no LiteLLM, no Langfuse.** The design doc's §8 "buy" position is overridden. Rationale: single binary in the hot path, no Python service dependency, full control over routing/fallback/circuit-breaking, and the Go `Provider` seam already exists. Consequences this repo now owns:
   - **Model routing:** extend `internal/llm` with fallback chains (primary → fallback-1 → fallback-2) and a per-provider circuit breaker (error-rate window, auto-open/half-open). Provider normalization (Gemini/OpenAI/Anthropic/vLLM API shapes) is ours forever — the existing OpenAI-compatible `Provider` interface is the seam; Anthropic needs a native implementation.
   - **Prompt management:** prompt versions table + diff/deploy API + Studio UI are **built**, not configured. The existing React app is the Studio.
   - **Tracing/eval:** the `runs` table is the trace store (extend per §3.5); the eval plane is built in Go (Phase 1 minimal slice stands).
   - The doc's §8 revisit-trigger inverts: revisit *adopting* LiteLLM/Langfuse only if provider normalization or eval tooling maintenance becomes a measurable drag.

2. **Self-serve persona inconsistency.** §1's pain-point table promises "business team authors prompts"; §3.2/§8 scope self-serve to DS. Align §1's wording before stakeholder review — reviewers will catch it.

3. **Sync `<1s` SLA realism.** Multimodal gemini-flash p95 is frequently ≥1s before adding render + validation + network. Either soften to p50 <1s / p95 <2.5s, or scope the <1s promise to cache hits + text-only checks. (Cost math in §7 checks out: ≈$0.0048/product ≈ $4.5K/mo at 30K/day ✓.)

4. **Demo data disposition.** Current SQLite runs/feedback are user-keyed demo data. Confirm it's throwaway (consistent with the original "temporary DB" requirement) — Phase 0 migrates schema, not data.

---

## 7. Phase 0/1 Success Criteria (from the doc, made checkable)

- [ ] `attribute-extraction` task registered via YAML; endpoint auto-available at `/v1/tasks/attribute-extraction/predict`
- [ ] Output schema enforced; malformed-output rate <0.5% with structured-output mode + 1 correction retry
- [ ] Shadow report: accuracy within 2% and latency within 200ms of direct-Gemini on 100% of shadow traffic
- [ ] Per-task cost dashboard live; every run attributed to a task from day 1
- [ ] Budget hard-stop returns 429 with `retry-after`
- [ ] Zero un-evaluated prompt deploys (advisory gate logs in Phase 1; hard gate Phase 2)
