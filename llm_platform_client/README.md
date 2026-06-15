# LLM Platform — Client Portal

Consumer-facing frontend for teams that **call** the platform (the Studio app in
`llm_platform_frontend/` is for teams that **operate** it). It talks only to the
product API (`/v1/tasks/*`) plus `/health` and `/pricing`:

- **Task catalog** — every registered task as a callable API product
- **Task detail** — I/O schemas, live **Try it** panel against the real
  `POST /v1/tasks/{id}/predict` endpoint (shows `output_valid`, fallback/degraded
  badges, usage, cost, latency, `task_run_id`), 30-day usage chart, and
  copy-paste integration snippets

## Auth — no login screen, by design

Every request carries a long-lived **service JWT** in the `Authorization: Bearer`
header, exactly like a machine caller (e.g. CIS). A working demo token for the
principal `svc:demo-client` (signed with the dev `JWT_SECRET`, expires 2036) is
baked into `src/auth/token.ts`, so the portal works out of the box with zero
errors and zero setup.

To use a different principal (or after rotating `JWT_SECRET`):

```bash
cd ../llm_platform_go
go run ./cmd/issue-token -sub svc:my-team -email my-team@svc.local -ttl 8760h
# put the printed token in llm_platform_client/.env.local:
# VITE_API_TOKEN=<token>
```

`VITE_API_TOKEN` takes precedence over the baked-in sample token.

## Run

```bash
# backend first (from llm_platform_go): go run ./cmd/server   # :8000
npm install
npm run dev        # :5174 — proxies /v1, /health, /pricing to :8000
```

Verify: `npm run build && npm run lint`.
