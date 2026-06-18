# Phase Workflow — Execution Plan for Claude/Opus Sessions

**Audience:** a future Claude (Opus) session implementing the next phases of the LLM
Platform. Read `docs/repo-guide.md` first (complete current state) and
`docs/gap-analysis-roadmap.md` (design-doc mapping). This file is the work plan.

**Standing decisions (do not relitigate):**
- **Pure Go, single binary.** The design doc's "Langfuse + LiteLLM" buy-position is
  overridden (2026-06-12). Wherever the phase text below says "thin UI (Langfuse +
  task registry wrapper)", read: **custom screens in the existing React app**.
- Task is the unit of everything. Playground API stays separate from `/v1/tasks/*`.
- Seams to build against, never around: `users.Store`, `llm.Provider`,
  `internal/db` query functions, guarded-ALTER migrations.

**How to work each phase (ritual):**
1. Enter plan mode; re-read the relevant sections of repo-guide + this file; write a
   concrete file-level plan; get approval.
2. Implement backend-first with tests at each step (`go build ./... && go vet ./... &&
   go test ./...` must stay green continuously), then frontend (`npm run build && npm
   run lint`), then live-verify with curl against a running server (real Groq key is
   configured in `.env`; OpenAI/Gemini keys may be absent — degrade gracefully).
3. Update `docs/repo-guide.md` (state) and this file (check off items) at phase end.
4. Keep the Compare UI working unchanged — it is the regression canary for auth,
   sessions, and the playground task path.

**Baseline already delivered (Phase 0, done):** task registry (DB-resident, authored via
the Studio API/UI) +
`/v1/tasks/{id}/predict` with input/output schema enforcement + task-keyed runs
(`task_id`, `prompt_version`, `provider`, `fallback_used`, `cache_hit` columns) +
per-task dashboard + playground stamped as a task + `llm.CallModel` single execution
path + provider attribution. One real task live: `attribute-extraction` (Groq).

---

## Phase 1 (4 weeks): Prove It Works

> Ship: llm-gateway + prompt-registry + observability-sink + thin UI. One task deployed:
> attribute-extraction (CIS prefill migration). Shadow mode: CIS calls both Gemini
> directly AND the platform, compares results.
> **Success metric:** platform matches Gemini's accuracy within 2%, latency within
> 200ms, for 100% of shadow traffic. Cost visibility dashboard live.

### 1.1 Fallback chain + circuit breaker (llm-gateway core)

- `internal/llm/breaker.go` (new): per-provider circuit breaker. Sliding error window
  (e.g. trip when ≥3 consecutive failures or >50% errors in 30s), states
  closed → open (reject immediately, `retry-after`) → half-open (probe 1 request).
  Guard with a mutex; key by provider attribution name. Unit-test state transitions
  with a fake clock (inject `now func() time.Time`).
- `internal/llm/fallback.go` (new): `CallWithFallback(ctx, clients, models []string,
  messages, temp, maxTokens) ModelResult` — try primary, then each fallback when the
  failure is *infrastructural* (breaker open, 5xx, timeout, auth) — NOT on
  content-level issues. Set `ModelResult.FallbackUsed=true` (add field) and record
  which model actually served.
- Wire into `Predict`: use `append([]string{task.Model}, task.FallbackModels...)`.
  Stamp `fallback_used` on the run row (column already exists).
- Degraded signaling: when the result came from a fallback or the breaker is open for
  the primary, set response header `X-Platform-Degraded: true` (design doc §6 contract).
- **Accept:** kill the primary provider in a test (fake server returning 500s) → second
  model serves, `fallback_used=1` in runs, header present; breaker opens after the
  threshold and recovers via half-open.

### 1.2 Budget enforcement (per-task daily spend)

- `internal/db/queries.go`: `TaskSpendToday(db, taskID) (float64, error)` — `SUM(cost_usd)
  WHERE task_id=? AND created_at >= date('now')`.
- In `Predict`, before the model call: if `task.DailyBudgetUSD > 0` and spend ≥ budget →
  **429** with `Retry-After: <seconds to UTC midnight>` and detail "daily budget
  exhausted". Alert threshold: when spend crosses 80%, log a warning (real alerting is
  Phase 2's alert engine).
- A `daily_budget_usd: 0` task is budget-exempt (the design doc's escape hatch for
  critical compliance paths).
- **Accept:** test seeds runs totaling > budget → predict returns 429 + Retry-After;
  budget-0 task unaffected.

### 1.3 Service principals (machine JWT auth)

- `internal/auth`: add `Claims.Kind` ("user" | "service") or use a `svc:` subject
  prefix. `RequireAuth` unchanged (it already accepts Bearer).
- `cmd/issue-token/main.go` (new small CLI): `go run ./cmd/issue-token -sub svc:cis
  -email cis@platform -ttl 8760h` → prints a signed long-lived token using
  `JWT_SECRET`. This is how CIS gets credentials in shadow mode. (Full RBAC roles per
  task are Phase 2 — for now any valid principal can invoke.)
- Stamp service runs like user runs (`user_id = "svc:cis"`); the dashboard's per-user
  scoping then gives per-caller views for free.
- **Accept:** token from the CLI authenticates `POST /v1/tasks/.../predict` via
  Authorization header; expired/garbage tokens → 401.

### 1.4 Prompt registry (versions table + deploy flow)

Phase 0 stores one active prompt on the task and bumps a counter. Phase 1 makes
versions first-class:

- Migration: `prompt_versions` table — `(task_id, version, prompt_template,
  system_prompt, created_by, created_at, note)`, unique `(task_id, version)`.
- `tasks.Store`: on every prompt change (via the API), also append to
  `prompt_versions`; backfill version 1 rows for existing tasks at migrate time.
- New endpoints: `GET /v1/tasks/{id}/versions` (history),
  `POST /v1/tasks/{id}/versions` (save draft without activating),
  `POST /v1/tasks/{id}/deploy {version}` (set active prompt — in Phase 2 this gains the
  eval gate; build the handler so a gate check slots in front).
- `POST /v1/tasks/{id}/test {inputs, version?, model?}` — run a prediction **without
  logging it as production traffic** (stamp `task_id` but a `is_test` column or
  `session_id='studio-test'` marker; pick one and keep dashboards filtered). This backs
  the Studio test panel.
- **Accept:** edit → versions accumulate; deploy switches `prompt_version` on new runs;
  history endpoint returns ordered versions.

### 1.5 Thin Studio UI (custom, in the existing React app)

- New page **Tasks** (`src/pages/TasksPage.tsx`): list (id, model, version, today's
  spend/budget, active) → detail view: config (read-only schemas), prompt editor
  (TextArea), **version history with diff** (plain `<pre>` side-by-side is fine; no
  diff lib needed in V1), **Test panel** (inputs form generated from the input schema's
  properties → calls `/test`, shows output + `output_valid` + cost), **Deploy** button
  (calls `/deploy`, confirm dialog), cost estimator strip (reuse `countTokens`/
  `estimateCost` on the draft prompt).
- Add "Tasks" to the `AppShell` nav. Mirror new API types in `src/types/index.ts`.
- **Accept:** full loop in the browser — edit prompt → test against real model →
  deploy → next `/predict` uses the new version (visible in response + dashboard).

### 1.6 Observability sink hardening

- `internal/db/runwriter.go` (new): buffered async writer — `chan *types.RunRow`
  (size ~1024) + single goroutine draining to `InsertRun`; non-blocking send with a
  dropped-rows counter logged on overflow; flush on shutdown (wire context into main).
  Handlers switch from direct `InsertRun` to the writer. (Kafka comes later; this
  removes the synchronous write from the hot path now.)
- Add `GET /v1/tasks/{id}/stats?days=N` — per-task daily cost/tokens/latency/success
  for the cost-visibility metric (dashboard scope today is per-user; this endpoint is
  per-task across callers).
- **Accept:** predict latency unaffected by DB contention test; stats endpoint feeds a
  per-task panel (add a simple task drill-down view to Dashboard).

### 1.7 Shadow-mode harness (CIS comparison)

CIS will call both Gemini-direct and the platform. Give them (and us) the comparison
tooling:

- `POST /v1/shadow/compare {task_id, items: [{inputs, expected_output}]}` — runs each
  item through `/predict` internals, diffs `output` vs `expected_output`
  (per-key exact match for attribute maps), returns + persists a report
  (`shadow_reports` table): match-rate, per-field mismatches, latency p50/p95, cost.
  This directly measures the success metric ("accuracy within 2%, latency within
  200ms").
- **Accept:** feed 20 fixture items → report with match rate + latency percentiles;
  re-run is idempotent and listable (`GET /v1/shadow/reports?task_id=`).

**Phase 1 exit checklist — COMPLETE 2026-06-12:**
- [x] Fallback + breaker live with `X-Platform-Degraded` (`internal/llm/{breaker,fallback}.go`)
- [x] 429 budget enforcement with Retry-After (live-verified)
- [x] Service token CLI (`cmd/issue-token`); `svc:cis` principal predicted live via Groq
- [x] Prompt versions + test + deploy endpoints; Studio (Tasks page) loop verified live:
      draft v2 → test (rendered draft, `is_test` stamped) → deploy → production on v2
- [x] Async run writer; per-task stats endpoint + Studio usage strip
- [x] Shadow comparison verified with real model: match_rate, p50/p95, persisted report —
      immediately surfaced real issues (case-sensitive value mismatches, confidence drift);
      consider a case-insensitive scorer option in Phase 2 eval
- [x] All tests green; Compare UI unchanged; repo-guide updated

**Implementation notes for Phase 2 (deviations/learnings):**
- `PUT /v1/tasks/{id}` uses merge semantics (absent fields keep current values).
- Tasks are DB-resident and authored via `POST /v1/tasks` (the earlier YAML seed layer
  was removed); `DELETE /v1/tasks/{id}` is admin-only and protects the `playground` task.
- Version numbers always advance past drafts (`max(prompt_versions)+1`).
- Shadow scoring is field-level **exact** match (case-sensitive, numeric-exact) —
  the Phase 2 eval scorers should add normalized/threshold variants.

---

## Phase 2 (4 weeks): Quality + Canary

> Ship: eval-service + golden datasets for attribute-extraction + compliance-nsfw.
> Quality gate enforced on deploy. CIS canary: 5% → 25% → 50% of prefill traffic.
> **Success metric:** zero un-evaluated prompt reaches production. No accuracy/latency
> regression in canary.

### 2.1 Golden datasets
- Tables: `datasets (id, task_id, name, version, created_*)` +
  `dataset_items (dataset_id, inputs JSON, expected_output JSON, tags)`.
- `POST /v1/tasks/{id}/datasets` (JSONL upload, one `{inputs, expected_output, tags?}`
  per line), `GET` list/detail. Validate items against the task's input schema on
  ingest. Versioned: re-upload = new dataset version, never mutate.

### 2.2 Eval service (in-process)
- `internal/eval` package: `Run(task, promptVersion, model, dataset) EvalRun` —
  iterate items through the prediction pipeline (concurrent, bounded ~8 workers),
  score with task-configured metrics. Scorers in V1: `exact_match` (whole output),
  `field_match_rate` (per-key for attribute maps), `f1` (token-level), latency stats.
  RAGAS/LLM-as-judge are Phase 4. Persist `eval_runs` + `eval_results` (per item).
- Endpoints: `POST /v1/tasks/{id}/evals {versions[], models[], dataset_id}` (runs the
  matrix), `GET /v1/tasks/{id}/evals` (leaderboard: version × model × metrics).
- Task config gains `eval_thresholds` (e.g. `{field_match_rate: 0.85}`) in the YAML
  contract + tasks table.

### 2.3 Hard deploy gate
- `POST /v1/tasks/{id}/deploy` now requires: an eval run exists for (version, active
  model, latest dataset version) AND all thresholds met. Independent re-verification in
  the handler (read eval_runs; never trust client-supplied scores). Override flag
  `force:true` allowed only for role `admin` + audit-logged.
- Nightly regression: a Go ticker (or external cron hitting an endpoint) re-runs the
  active (version, model) on the latest dataset; alert-log if any metric drops >2%
  from the stored baseline.

### 2.4 RBAC roles + audit log (gate prerequisite)
- `task_roles (task_id, principal, role)` — roles: `viewer | invoker | experimenter |
  deployer | admin`. Middleware `RequireRole(role)` reads the task id from the route.
  Defaults: creators get admin; demo users get experimenter; service principals get
  invoker on their tasks.
- `audit_log (id, ts, principal, action, task_id, detail JSON)` append-only; write on
  create/update/deploy/role-change/budget-change. `GET /v1/audit?task_id=`.
- Studio: hide/disable buttons by role; backend re-validates regardless.

### 2.5 Second task + canary support
- Register `compliance-nsfw` task (YAML): image_url input → verdict + confidence
  output. Requires multimodal content support in `/predict` inputs → extend
  `ChatMessage` content to the parts array (the playground already models this shape
  client-side; mirror it server-side for task inputs declared as image URLs).
- Canary is CIS-side (ABACUS flag). Platform must provide: stable contract (no breaking
  response changes — add only), `X-Platform-Degraded`, per-caller stats
  (`user_id = svc:cis`) to compare canary vs. direct cohorts.

**Phase 2 exit:** deploy without passing eval is impossible (test proves it);
leaderboard in Studio; nightly regression logging; RBAC enforced on mutate + deploy +
invoke; audit trail querying; compliance-nsfw predicts on fixture images.

---

## Phase 3 (4 weeks): Full Migration + Batch

> Ship: CIS 100% on platform for attribute extraction. Add SSCat prediction and
> compliance tasks. Airflow batch DAGs for catalog enrichment. RAG service with
> Taxonomy Kafka integration.
> **Success metric:** CIS removes direct Gemini code. Three V1 use cases live. Batch
> enrichment nightly.

### 3.1 Async mode + callbacks (required for CIS prefill at 100%)
- `task_runs` lifecycle table (or extend runs): status `QUEUED | RUNNING | SUCCEEDED |
  FAILED | CALLBACK_FAILED`. `POST /v1/tasks/{id}/predict?mode=async` (or body field) →
  202 + `task_run_id`; worker pool executes; result stored; `GET /v1/tasks/runs/{id}`
  returns status+result (endpoint already exists — extend with status).
- Callback delivery: request carries `{callback_url, correlation_id, hmac_secret_ref}`;
  POST result with `X-Signature: hex(hmac-sha256(body))`; retries 1s/2s/4s ×3 →
  `CALLBACK_FAILED` + DLQ table. Secrets referenced by name from env/config, never
  stored raw.
- DLQ: `dlq (id, kind, payload, error, created_at)` + `GET /v1/dlq` + manual replay
  endpoint.

### 3.2 Batch
- `POST /v1/tasks/{id}/predict/batch {items[] | dataset_ref}` → `batch_run_id`;
  bounded-concurrency worker over items; results to `batch_results` staging table
  (never directly to caller systems); `GET /v1/batches/{id}` progress + results page.
  Checkpoint = rows already done (resume on restart is a WHERE clause). Airflow DAGs
  live CIS/DE-side and call these endpoints; provide a reference DAG snippet in docs.
  Provider Batch-API discounts: defer until OpenAI/Gemini batch endpoints are wired —
  V1 batch = same sync pipeline at LOW priority.
- Priority lanes: simple two-queue worker pool (HIGH = sync/async requests, LOW =
  batch) so batch never starves the upload path.

### 3.3 Semantic cache — ✅ DELIVERED EARLY (2026-06-12)

Implemented ahead of schedule in `internal/cache` (Redis via go-redis, memory
fallback for dev, `Cache` interface seam). Deviations from the sketch below:
key includes prompt version + system prompt + temperature/max_tokens + output
schema in addition to rendered prompt + model (deploys invalidate predictably);
only primary-served, schema-valid production predicts are cached (fallback
answers and Studio test calls never); hits report zero usage/cost and stamp
`cache_hit=1`. Per-task YAML: `cache: {enabled, ttl}`. Note: the budget gate
runs before the cache lookup, so an exhausted task 429s even on cacheable
requests — revisit if free cache hits during exhaustion become desirable.
Remaining from the sketch: per-task hit-rate on the dashboard.

Original sketch:
- Redis (first external dependency — confirm infra) or embedded LRU fallback:
  key = `SHA256(rendered_prompt + model_id)` (+ image URLs when present), TTL 24h,
  store the successful `ModelResult`. Check after render, before `CallWithFallback`;
  stamp `cache_hit=1` (column exists). Invalidation is automatic: prompt-version bumps
  change the rendered prompt. Config per task: `cache: {enabled, ttl}`.
- Dashboard: hit-rate per task.

### 3.4 RAG service (taxonomy context)
- `internal/rag`: ES 8.x KNN index of taxonomy docs (category → attribute schemas);
  embedder behind an interface (OpenAI embeddings via existing provider key, or
  sidecar). Kafka consumer on Taxonomy events → re-embed changed categories (<5 min
  freshness SLA). Task config gains `rag: {enabled, index, top_k, similarity_threshold}`;
  renderer injects retrieved context into a `{{.rag_context}}` template variable.
  Below-threshold retrieval → proceed without context + confidence penalty flag in the
  response.
- This is the largest new surface — plan-mode it separately with infra owners
  (ES + Kafka access) before building.

### 3.5 Third task
- `sscat-prediction` YAML (RAG-enabled). All three V1 tasks live with datasets +
  thresholds + budgets.

**Phase 3 exit:** async+callback+DLQ proven with a fake callback receiver (incl.
failure/replay); batch over 1k items resumable; cache hit-rate visible; RAG-injected
prediction demonstrably uses taxonomy context; CIS removes Gemini code (their side).

---

## Phase 4+ (ongoing): Scale

- **Feedback loop:** extend `feedback` with `verdict`, `corrected_output`, `source`
  (qc/seller/automated); rolling per-task accuracy from QC samples; drift alerts.
  Fine-tuning pipeline: export corrected examples → JSONL (SFT format) → trigger LoRA
  when N accumulate (training itself is DS-owned; platform owns the data pipeline).
- **Custom DS models:** `llm.Provider` impl for internal model endpoints
  (`NewOpenAICompatProvider` already covers vLLM-style); registry entries via config
  instead of code.
- **New teams:** onboarding = create a task in the Studio (`POST /v1/tasks`, creator/admin)
  + roles + dataset. Document the task contract (repo-guide §3.5) as the onboarding doc.
- **Native Anthropic provider** — *no longer planned.* Claude models were added through
  the Meesho gateway over its OpenAI-compatible wire (`claude-sonnet-4-6`), so the
  existing `openAICompatProvider` covers them and no native Messages-API provider was
  needed. Revisit only if we ever call Anthropic's API directly, bypassing the gateway.
- **Postgres migration** when QPS demands it: `internal/db` + `tasks.Store` are the
  contained surfaces; SQLite remains the dev default.
- **Orchestration:** only revisit if 3+ callers duplicate the same call sequence
  (decided position).

---

## Cross-phase engineering rules

1. **Never break:** `/run` + `/sessions` contracts (Compare UI), `/v1/tasks/{id}/predict`
   response shape (additive changes only once CIS integrates), YAML task contract
   (additive only).
2. **Every schema change** = guarded ALTER in `db.Migrate`, runs on boot, tested against
   a pre-existing DB fixture.
3. **Every new endpoint** ships with: auth coverage, a black-box test in `tests/`, a
   mirrored TS type if the UI consumes it, and a row in repo-guide §3.8's table.
4. **Money paths get tests first** (budget, cost calc, cache-skip billing).
5. Keep secrets in `.env`/env only; `JWT_SECRET` must be real outside dev; HMAC secrets
   by reference.
6. After each phase: update `docs/repo-guide.md`, check off this file, and re-run the
   full verification ritual end-to-end with the real Groq key.
