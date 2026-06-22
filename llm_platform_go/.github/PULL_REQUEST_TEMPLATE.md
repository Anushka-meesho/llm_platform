<!--
  PR description template for cataloging_llm_platform-backend.
  The PR-description validator requires: a Release Type, at least one Testing
  Environment, and the Metrics Impacted / Downstream Impacts / Rollback Plan
  sections. Fill every section — write "None" where a section does not apply
  rather than deleting it. Keep at least one checkbox ticked under Release Type
  and Testing Environments.
-->

## Summary

<!-- What does this PR change and why? 2–4 sentences. -->

## Release Type

<!-- Tick at least one. -->

- [ ] Feature
- [ ] Bugfix
- [ ] Hotfix
- [ ] Refactor / tech-debt
- [ ] Config / infra
- [ ] Docs only
- [ ] Release

## Changes

<!-- Bullet the notable changes (handlers, schema/migrations, config, etc.). -->

-

## Testing Environments

<!-- Tick every environment where this change was validated. At least one is required. -->

- [ ] Local
- [ ] Dev
- [ ] Staging / QA
- [ ] UAT
- [ ] Production

## How to test / UAT

<!--
  Steps a reviewer can follow to validate the change. For a Release, also paste
  the UAT comparison link between the release base and head, e.g.
  https://github.com/Meesho/cataloging_llm_platform-backend/compare/<base>...<head>
  Make sure both branches exist and the base is correct — a 404 here means the
  base/head branch pair is wrong (e.g. comparing against a `develop` that does
  not exist on this repo; use `main` as the base unless told otherwise).
-->

-

## Metrics impacted

<!--
  Required. Which dashboards / alerts / SLOs to watch after this ships —
  predict latency, token cost, error rate, gateway fallback rate, circuit-breaker
  trips, rate-limit 429s. Write "None" if this change cannot move any metric.
-->

## Downstream impacts

<!--
  Required. Services / consumers affected, API or response-contract changes,
  DB migrations (and whether they are backward-compatible), config/env changes,
  and the frontend repo if its types must change in lock-step. Write "None" if
  nothing downstream is affected.
-->

## Rollback plan

<!--
  Required for releases. How to revert safely if this misbehaves in production:
  the revert commit / previous image tag, whether any DB migration is reversible
  (this service uses additive, guarded ALTERs — see internal/db), and any config
  flag that disables the new behaviour. Write "N/A" only for docs-only PRs.
-->

## Checklist

- [ ] `go build ./...` and `go vet ./...` are clean
- [ ] `go test ./...` passes
- [ ] DB migrations are additive / backward-compatible (or none)
- [ ] Config/env changes are reflected in `.env.example`
- [ ] No secrets or `.env` committed
- [ ] Docs updated if behaviour/contract changed
