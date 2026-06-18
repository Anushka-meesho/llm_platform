# Production Deployment Guide — What Must Change Before Real Deployment

**Audience:** a future Claude (Opus) session asked to "make this deployable" or to ship
the platform to a real environment. Read `docs/repo-guide.md` first. This doc lists
every place the codebase currently makes a **dev/demo assumption**, what to change,
where the seam is, and in what order. The architecture was built so most of these are
contained swaps — do not refactor around the seams, use them.

**Definition of "deployed" here:** running in Meesho infra (k8s or VM), real SSO for
humans, service tokens for machines, Postgres-class storage, HTTPS behind a load
balancer, secrets from a secret manager, and CIS calling it in shadow mode.

---

## 0. The one-page diff (summary table)

| Area | Today (dev/demo) | Production | Seam / files |
|---|---|---|---|
| Human auth | One-click demo users, `DemoStore` | Real SSO (OIDC) | `users.Store` + 2 handlers |
| Machine auth | `cmd/issue-token` shared-secret JWT | Same mechanism, managed issuance + rotation | `internal/auth` |
| JWT secret | Dev default constant | Required from secret manager; fail fast | `internal/config` |
| Cookies | `Secure=false`, no domain | `Secure=true`, real domain, SameSite | env only |
| DB | SQLite, 1 writer, boot-time ALTERs | Postgres + versioned migrations | `internal/db`, `internal/tasks/store.go` |
| Replicas | Implicitly 1 (SQLite) | N replicas (after Postgres) | — |
| HTTP server | `http.ListenAndServe`, no timeouts | Timeouts, body limits, graceful shutdown | `cmd/server/main.go` |
| TLS | None | Terminate at LB/ingress | infra |
| CORS | localhost:5173 | Real frontend origin, or same-origin (none) | env only |
| Frontend | Vite dev server + proxy | Static build served from Go or CDN | `vite.config.ts`, small Go change |
| Provider keys | `.env` file | Secret manager → env at deploy | infra |
| Logging | chi text logger | Structured JSON (`slog`), no secrets/PII policy | `internal/api/router.go` |
| Health | `/health` only | `/healthz` (live) + `/readyz` (DB ping) | 1 handler |
| Breaker/limits | In-memory, per-process | Acceptable at 1 replica; revisit at N | note below |

---

## 1. Identity: swap the demo SSO for real SSO

This was the original design constraint — the demo user DB is throwaway and the swap
is intentionally one seam.

1. **Implement `users.Store` against the IdP** (new file, e.g.
   `internal/users/oidc.go` or `internal/users/ldap.go`). The interface
   (`internal/users/store.go`) needs `GetByID` and `List`; in production `List` should
   return an error or empty (it only exists for the demo login screen).
2. **Replace the login flow** (`internal/api/auth_handlers.go`):
   - Delete `GET /auth/demo-users` (and its route).
   - Replace `POST /auth/login {user_id}` with the OIDC pair:
     `GET /auth/login` → 302 to the IdP authorize URL (state + PKCE), and
     `GET /auth/callback` → exchange code, validate ID token, map claims to
     `auth.User{Subject, Email, Name}`, then **reuse the existing**
     `auth.IssueToken` + `auth.SetAuthCookie` unchanged. The platform session
     mechanism does not change — only who vouches for the user.
   - Keep `POST /auth/logout` and `GET /auth/me` as-is.
3. **Frontend** (`src/components/LoginScreen.tsx`): replace the demo-user buttons with
   a single "Sign in with SSO" button that does `window.location = '/auth/login'`.
   `AuthContext` bootstrap via `/auth/me` already handles the post-callback state —
   no other frontend auth change.
4. **Config:** add `OIDC_ISSUER_URL`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`,
   `OIDC_REDIRECT_URL` to `internal/config`. Use `github.com/coreos/go-oidc/v3` +
   `golang.org/x/oauth2` (well-trodden, no framework).
5. **Do not migrate demo data** — there is none persisted (DemoStore is in-memory by
   design). Runs stamped `u-admin`/`u-analyst` are dev artifacts; production starts
   clean or keeps them as obviously-fake history.

**Service principals** (`cmd/issue-token`) keep working unchanged — HS256 with the
shared `JWT_SECRET`. Production hardening: issue from a controlled admin host only,
record issued tokens (subject, expiry) in the audit log (Phase 2), and prefer short
TTLs + scheduled re-issue over year-long tokens. If the org later wants asymmetric
verification, swap HS256 → RS256/EdDSA inside `internal/auth` (issue with private key,
`RequireAuth` verifies with public) — the call sites don't change.

---

## 2. Secrets & config hardening

`internal/config/config.go` currently defaults `JWT_SECRET` to
`dev-insecure-secret-change-me`. **Add an environment gate:**

```go
// in config.Load()
if os.Getenv("ENV") == "production" {
    if cfg.JWTSecret == "" || cfg.JWTSecret == "dev-insecure-secret-change-me" {
        return nil, errors.New("JWT_SECRET must be set to a strong value in production")
    }
    if !cfg.CookieSecure {
        return nil, errors.New("COOKIE_SECURE=true is required in production")
    }
}
```

Production env (from the secret manager / deployment env, **never a committed file**):

```
ENV=production
JWT_SECRET=<32+ random bytes>
COOKIE_SECURE=true
COOKIE_DOMAIN=llm-platform.meesho.internal     # or leave empty for host-only
TOKEN_EXPIRY=8h                                # shorter than the 12h dev default
ALLOWED_ORIGINS=https://llm-platform.meesho.internal
MEESHO_GATEWAY_VK / GROQ_API_KEY               # from secret manager (gateway VK serves all non-Groq models)
DB_*            # see §3
PRICING_PATH=/etc/llm-platform/pricing.json
```

Tasks are **not** seeded from files — they live in the DB and are authored at runtime
through the Studio (`POST /v1/tasks`, creator/admin). There is no `TASKS_DIR` to mount;
task config is data, carried by the database (back it up / migrate it like any other data).

Delete nothing in `.env` handling — `godotenv.Load()` is a no-op when the file is
absent, which is exactly right in containers.

---

## 3. Database: SQLite → Postgres

SQLite is correct for dev and **forces a single replica** (one writer, local file).
Move to Postgres before horizontal scaling or any HA requirement. The blast radius is
deliberately contained to `internal/db/*` and `internal/tasks/store.go` +
`internal/tasks/versions.go` (the only files with SQL).

1. **Driver:** `github.com/jackc/pgx/v5/stdlib` via `database/sql` — handler code
   keeps using `*sql.DB`.
2. **`db.Open`:** branch on `DB_DRIVER=sqlite|postgres` (keep SQLite for dev/tests).
   For Postgres: drop the PRAGMAs and `SetMaxOpenConns(1)`; set a real pool
   (`SetMaxOpenConns(20)`, `SetMaxIdleConns(10)`, `SetConnMaxLifetime(30m)`).
3. **Migrations:** replace boot-time guarded ALTERs with **versioned migrations**
   (`golang-migrate` or `pressly/goose`, embedded via `embed.FS`, run on startup).
   Translate the current schema (repo-guide §3.7) once as migration 0001. SQL deltas:
   - `INTEGER PRIMARY KEY AUTOINCREMENT` → `BIGINT GENERATED ALWAYS AS IDENTITY`
   - `DATETIME ... datetime('now')` → `TIMESTAMPTZ ... now()`; store `time.Time`
     directly, delete the string `fmtTime`/`parseTime` round-trips
   - `date('now')` / `date('now', '-N days')` (in `TaskSpendToday`,
     `TaskDailyStats`, `DashboardStats`) → `CURRENT_DATE` / `now() - interval 'N days'`
   - `substr(created_at,1,10)` group-bys → `date_trunc('day', created_at)::date`
   - `INSERT OR IGNORE` (prompt_versions backfill) → `ON CONFLICT DO NOTHING`
   - `ON CONFLICT(...) DO UPDATE` (feedback upsert) — identical in Postgres ✓
   - `?` placeholders → `$1, $2, …` (pgx stdlib handles `?` **only** via rewriting —
     don't rely on it; convert the queries)
   - Booleans: replace the `INTEGER` 0/1 columns + `boolToInt` with real `BOOLEAN`
4. **Tests:** keep the suite on in-memory SQLite for speed; add one CI job running the
   same suite against a Postgres service container (the queries are the risk, not the
   handlers). Gate with `DB_DRIVER` env in `newTestServer`.
5. **Data migration:** per the standing decision, dev data is throwaway — migrate
   schema, not rows. If the team wants playground history preserved, a one-off
   `sqlite3 .dump` → transform → `psql` script is fine; do not build tooling.

---

## 4. HTTP server & process hardening — `cmd/server/main.go`

Replace `http.ListenAndServe(addr, router)` with:

```go
srv := &http.Server{
    Addr:              addr,
    Handler:           router,
    ReadHeaderTimeout: 10 * time.Second,
    ReadTimeout:       30 * time.Second,
    WriteTimeout:      120 * time.Second, // ≥ longest model call budget
    IdleTimeout:       120 * time.Second,
}
go func() { /* ListenAndServe, log fatal on unexpected error */ }()

// Graceful shutdown: SIGTERM → stop accepting → drain in-flight (30s) →
// runWriter.Close() (flushes queued trace rows) → database.Close().
```

Use `signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)`.
**Order matters:** shut the HTTP server down before `runWriter.Close()` so no handler
writes to a closed writer (writes after close are dropped+counted, not panics, but
don't lose traces on every deploy).

Also:
- **Body size limit:** wrap with `http.MaxBytesReader` middleware (e.g. 2 MB; shadow
  compare payloads are the largest legitimate bodies).
- **Request timeout middleware:** `middleware.Timeout(110 * time.Second)` inside chi,
  under the WriteTimeout.
- **TLS:** terminate at the LB/ingress. Do not add TLS into the Go server unless
  there's no LB; if so, `srv.ListenAndServeTLS` with mounted certs.
- **Protective rate limit:** the real token-aware limiter is Phase 2; until then add
  `middleware.Throttle(256)` (max concurrent) so a runaway caller can't exhaust
  goroutines waiting on 120s model calls.

---

## 5. Frontend: from Vite dev server to a real deployment

Today the browser talks to Vite (`:5173`) which proxies to Go (`:8000`). In production
choose **same-origin** (recommended — kills CORS and cookie-domain complexity):

1. `npm run build` → `dist/`.
2. Serve `dist/` from the Go binary: in `router.go`, after API routes, add a static
   file server with an SPA fallback (any non-API, non-file path → `index.html`):
   ```go
   r.Handle("/*", spaHandler{staticDir: cfg.StaticDir}) // cfg.StaticDir env: STATIC_DIR
   ```
   Mount API routes first; they win by specificity. Add `STATIC_DIR` to config
   (empty = API-only mode, keeps dev workflow unchanged).
3. With same-origin, `ALLOWED_ORIGINS` can be empty and the CORS middleware can be
   skipped entirely when `len(origins)==0 && STATIC_DIR != ""`.
4. The frontend's `BASE = ''` relative URLs (`src/api/client.ts`) already work
   same-origin — no frontend code change.

Alternative (CDN/separate host): keep CORS with `AllowCredentials`, set
`ALLOWED_ORIGINS` to the exact origin, `COOKIE_DOMAIN` to the shared parent domain,
`SameSite=None; Secure`. More moving parts; only pick this if infra mandates it.

Either way: **the Vite proxy list in `vite.config.ts` is dev-only** and stays.

---

## 6. Observability & operations

- **Structured logs:** replace `middleware.Logger` with a small `slog` JSON middleware
  (request id, method, path, status, duration, user/subject, task_id when resolvable).
  Keep `middleware.RequestID`/`Recoverer`.
- **Log hygiene:** never log `Authorization` headers, cookies, or full prompts at
  info level. Note: the `runs` table stores full prompts/responses by design — agree a
  **retention policy** (e.g. prune `response`/`prompt` bodies after N days, keep
  aggregates) and implement as a nightly `DELETE`/`UPDATE` job before storing real
  seller data.
- **Health endpoints:** keep `/health` (liveness). Add `/readyz`: `database.Ping()` +
  pricing table loaded → 200, else 503. Point k8s probes accordingly.
- **Metrics (cheap win):** `prometheus/client_golang` — request histogram by
  route/status, model-call histogram by provider/model/outcome, breaker state gauge,
  RunWriter buffer depth + dropped counter, budget-rejection counter. `/metrics` on a
  separate internal port or behind auth.
- **Crash visibility:** `Recoverer` already returns 500s; wire panics into the
  structured log with stack traces.

---

## 7. Multi-replica caveats (after Postgres)

These are **per-process in-memory** today and acceptable at 1 replica; with N replicas
they become per-pod and need awareness (fine) or centralization (later):

| Component | At N replicas | Action |
|---|---|---|
| Health breaker (`internal/health`) | Each pod tracks per-(task, model) health independently | Acceptable — document it. Centralize in Redis only if flapping is observed |
| Budget gate (`TaskSpendToday`) | Reads the shared DB — correct, but read-then-spend races can overshoot by in-flight calls | Acceptable overshoot (≤ concurrency × per-call cost). Tighten with `SELECT ... FOR UPDATE`-based reservation only if real overshoot matters |
| RunWriter | Per-pod buffers — fine | Ensure graceful shutdown flush per §4 |
| Schema/template caches (`internal/tasks`) | Per-pod content-hash caches — correct by construction | None |
| Task config reads | Per-request DB read — fine on Postgres | The design doc's Redis 60s cache is a Phase-2+ optimization, not a deploy blocker |

---

## 8. Container & deploy packaging

Pure-Go binary, **no cgo anywhere** (modernc sqlite is pure Go) — a static binary in a
distroless image:

```dockerfile
FROM golang:1.22 AS build
WORKDIR /src
COPY llm_platform_go/ .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /llm-platform ./cmd/server

FROM node:22 AS ui
WORKDIR /ui
COPY llm_platform_frontend/ .
RUN npm ci && npm run build

FROM gcr.io/distroless/static-debian12
COPY --from=build /llm-platform /llm-platform
COPY --from=ui /ui/dist /static
COPY llm_platform_go/pricing.json /etc/llm-platform/pricing.json
ENV STATIC_DIR=/static PRICING_PATH=/etc/llm-platform/pricing.json
ENTRYPOINT ["/llm-platform"]
```

K8s notes: liveness `/health`, readiness `/readyz`; `terminationGracePeriodSeconds`
≥ the drain window (≥ 35s); secrets via env from the secret manager; **replicas: 1 +
PVC while on SQLite**, unlock HPA only after §3. Tasks are DB-resident (authored via the
Studio), so onboarding a task is an API/UI action against the running service, not a config
rollout — nothing task-shaped needs to be mounted.

---

## 9. Things that must NOT change in the move

(Reiterating repo-guide §6 where deployment pressure tends to break them.)

1. `users.Store`, `llm.Provider`, `internal/db` query funcs are the swap seams — swap
   behind them, don't bypass.
2. `/v1/tasks/*` response shapes are now CIS-facing: **additive changes only.**
3. Playground (`/run`, `/sessions`) stays internal-only — never document it to service
   callers; consider requiring a non-`svc:` subject for it.
4. Observability writes never fail or block a prediction (RunWriter semantics).
5. Budget cap 0 = exempt is the documented escape hatch for critical compliance paths.
6. Tasks are DB-resident, authored via `POST /v1/tasks` (no file seeding) — the database
   is the source of truth, so back it up and migrate it like any other stateful data
   through the Postgres port. Only the built-in `playground` task is seeded at boot.

---

## 10. Ordered execution plan (do it in this order)

Each step is independently shippable; stop at any line and the system still runs.

1. **Config hardening** (§2): ENV=production gate, secure cookies, real secret. *(S)*
2. **Server hardening** (§4): timeouts, body limits, throttle, graceful shutdown with
   RunWriter drain. *(S)*
3. **Same-origin static serving** (§5) + Dockerfile (§8) → first deployable image,
   still SQLite, replicas=1 + PVC. *(M)*
4. **Health/readiness + structured logs + metrics** (§6). *(M)*
5. **Real SSO** (§1) — humans on OIDC; demo store deleted from the prod path (keep it
   behind `ENV!=production` for local dev). *(M)*
6. **Postgres** (§3) — versioned migrations, pgx, CI job against real Postgres; then
   raise replicas. *(L)*
7. **Retention job + token issuance process** (§6, §1). *(S/M)*

Verification ritual per step: full Go + frontend suites green, then a smoke pass of
the live checklist in `docs/phase-workflow.md` Phase 1 exit list (login → predict as
svc token → Studio loop → budget 429 → dashboard) against the deployed instance.

**Explicit non-goals of "deploy":** eval gate, RBAC roles, async/callbacks, batch,
RAG, semantic cache — those are Phases 2–3 features, not deployment prerequisites.
Shadow mode for CIS needs only steps 1–5 above plus a service token.
