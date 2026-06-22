<!--
  Ready-to-paste PR description for the current backend changes. Copy everything
  below into the PR body. It is pre-filled to satisfy the PR-description
  validator (Release Type, Testing Environments, Metrics, Downstream, Rollback).
  Adjust the UAT compare link to your actual base...head before submitting.
-->

## Summary

Adds per-task input guardrails, typed image inputs, and stable admin run-history
pagination to the prediction gateway, and folds in the Gemini-2.5 thinking
support, per-task rate limiter, and gateway attempt tracing from upstream `main`.
No breaking API changes — all additions are backward-compatible.

## Release Type

- [x] Feature
- [ ] Bugfix
- [ ] Hotfix
- [ ] Refactor / tech-debt
- [ ] Config / infra
- [ ] Docs only
- [x] Release

## Changes

- **Per-task input limits** — `max_prompt_chars`, `max_image_kb`, `max_images` on
  the Task config (0 = no limit), enforced as `413` on production predicts *and*
  Studio test runs; editable per task in the UI.
- **Typed image inputs** — image fields are an array of `format:"image"` strings
  recognised by the schema marker (any property name), with the legacy
  `image`/`images` names kept as a fallback. `RenderPrompt` exposes image fields
  to the template as a count, so base64 is never inlined into the prompt.
- **Snapshot run history** — `GET /v1/admin/runs` accepts `anchor_id` to pin a
  point-in-time snapshot so pages don't shift as new runs arrive, plus a
  `has_task` filter to exclude playground/compare runs.
- **Gemini-2.5 thinking** — `minOutputTokens` floor and array-content unmarshal.
- **Per-task rate limiter** — request/token windows (`RATE_*`), `429`/`413`.
- **Gateway attempt tracing** — `gateway_attempts` table + per-model fallback trace.
- New DB columns added via additive guarded `ALTER`s (SQLite + Postgres).

## Testing Environments

- [x] Local
- [x] Dev
- [ ] Staging / QA
- [ ] UAT
- [ ] Production

## How to test / UAT

- `go test ./...` — full suite green (unit + integration in `tests/`), including
  new coverage: `TestPredictEnforcesInputLimits`, `TestPredictForwardsCustomNamedImageField`,
  `TestRenderPromptDoesNotInlineImageBytes`.
- Manual: create a task with an image field + input limits, then exercise
  `POST /v1/tasks/{id}/predict` and the Studio test panel; verify `413` past the
  caps and that the image is attached (not inlined).
- UAT diff: https://github.com/Meesho/cataloging_llm_platform-backend/compare/main...<release-head>
  <!-- Use `main` as the base. A 404 means the base/head pair is wrong (e.g. a `develop` branch that doesn't exist here). -->

## Metrics impacted

- Predict **latency** (`gateway_latency_ms` vs model latency) — unchanged path; watch for regressions.
- Token **cost** per predict — should *drop* for image tasks (base64 no longer inlined into the prompt text).
- **413 rate** — new, expected to be non-zero where per-task size limits are configured.
- **429 rate** — from the per-task rate limiter (already shipped upstream).
- Gateway **fallback / circuit-breaker** rates — unchanged.

## Downstream impacts

- **API:** additive only — new optional task fields (`max_prompt_chars`,
  `max_image_kb`, `max_images`) and new `GET /v1/admin/runs` query params
  (`anchor_id`, `has_task`). No existing field/route changed.
- **Frontend:** `cataloging_llm_platform-frontend` consumes the new task fields
  and admin params; ship the matching frontend change together.
- **DB:** additive guarded `ALTER TABLE` columns on `tasks` (and the
  `gateway_attempts` table from upstream) — no destructive migration; existing
  rows default to `0`/no-limit.

## Rollback plan

- Revert this PR's merge commit and redeploy the previous image tag.
- DB is safe to leave as-is: the new columns are additive and default to `0`
  (no limit), so an older binary simply ignores them — no down-migration needed.
- To disable the new size limits without a deploy, set every task's
  `max_prompt_chars`/`max_image_kb`/`max_images` back to `0`.

## Checklist

- [x] `go build ./...` and `go vet ./...` are clean
- [x] `go test ./...` passes
- [x] DB migrations are additive / backward-compatible (or none)
- [x] Config/env changes are reflected in `.env.example`
- [x] No secrets or `.env` committed
- [x] Docs updated if behaviour/contract changed
