# 09 — Authentication and RBAC

## How identity works

The platform uses **JWT (JSON Web Tokens)** for authentication, stored in an **HttpOnly cookie**.

The flow:
1. User selects their account from the demo user list (`GET /auth/demo-users`) and submits their `user_id`.
2. The server looks up the user in the user store by ID.
3. If found, the server creates a JWT, signs it with `JWT_SECRET`, and sends it back as a cookie.
4. Every subsequent request includes that cookie automatically (the browser sends cookies with every request).
5. The `RequireAuth` middleware validates the JWT on every protected route.

No session stored on the server. The JWT itself contains the user's identity and role.

> **Note on the demo SSO:** This is a development stand-in, not a real authentication system. There is no password — the login flow trusts whoever knows the `user_id`. A production deployment would replace the `DemoStore` with a real IdP integration (OAuth2, SAML, internal Meesho SSO) and the login handler would redirect to that IdP instead of accepting a bare ID.

---

## What is a JWT?

A JWT is a self-contained token made of three parts separated by dots:

```
eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9   ← header (base64-encoded)
.
eyJzdWIiOiJ1c2VyMTIzIiwicm9sZSI6ImNyZWF0b3IiLCJleHAiOjE2OTk5OTk5OTl9  ← payload (base64-encoded)
.
SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c  ← signature (HMAC-SHA256)
```

The **payload** (middle part), when decoded, contains:
```json
{
  "sub": "user123",
  "email": "alice@meesho.com",
  "name": "Alice",
  "role": "creator",
  "iss": "llm-platform-demo",
  "exp": 1699999999
}
```

The **signature** is computed as:
```
HMAC-SHA256(base64(header) + "." + base64(payload), JWT_SECRET)
```

> **🔤 Go concept: HMAC**
> HMAC (Hash-based Message Authentication Code) is a way to create an unforgeable signature. You give it data and a secret key; it gives you a fixed-size hash. If anyone changes even one character of the data, the hash changes completely. Without the secret key, you can't compute the correct hash.

Why does this work for auth? The server checks: "does the signature match what I'd compute with `JWT_SECRET` for this header+payload?" If the token was tampered with (someone changed `"role": "caller"` to `"role": "admin"`), the signature won't match and the token is rejected.

**Nobody can forge a valid token unless they know `JWT_SECRET`.** This is why `JWT_SECRET` must be kept secret and rotated if compromised.

---

## HttpOnly cookies: why not localStorage?

The token is stored in an **HttpOnly cookie**:
- `HttpOnly`: JavaScript code cannot access this cookie (`document.cookie` returns nothing for HttpOnly cookies). This prevents cross-site scripting (XSS) attacks from stealing the token.
- `SameSite=Lax`: The cookie is only sent on same-site requests, preventing cross-site request forgery (CSRF) attacks.
- `Secure` (in production): The cookie is only sent over HTTPS.

> **Why not store the JWT in `localStorage`?** `localStorage` is accessible to JavaScript. An XSS vulnerability (injected script) could read the token from `localStorage` and send it to an attacker. HttpOnly cookies can't be read by JavaScript at all — even a successful XSS can't steal the token.

---

## The auth endpoints

### `GET /auth/demo-users` (public)

Returns the list of seeded demo users so the login screen can render one-click login buttons. This endpoint only exists because the demo store exposes its entire user directory — a real SSO-backed deployment would remove this and redirect to the IdP instead.

```json
Response: {"users": [{"id": "u-admin", "email": "admin@demo.local", "name": "Admin", "role": "admin"}]}
```

### Login flow

```
POST /auth/login
Body: {"user_id": "u-admin"}

1. userStore.GetByID("u-admin")
   → If not found: 401 Unauthorized

2. Issue JWT:
   Claims{
     Subject: user.ID,
     Email:   user.Email,
     Name:    user.Name,
     Role:    user.Role,
     Issuer:  "llm-platform-demo",
     Expires: now + 12h,
   }
   token = sign(claims, JWT_SECRET)

3. Set cookie:
   Set-Cookie: llm_platform_token=<token>; Path=/; HttpOnly; SameSite=Lax

4. Return: 200 OK {"user": {...}}
```

No password verification — the demo store is a development convenience, not a real credential system.

---

## The demo user store

The current `users.DemoStore` is a single hardcoded user in memory. It's not a real database — it's a **swap seam**: a placeholder that demonstrates the interface a real user store needs to implement.

The seeded demo user:
```
ID: "u-admin" | Email: admin@demo.local | Name: Admin | Role: admin
```

The interface the platform depends on:
```go
type Store interface {
    GetByID(ctx context.Context, id string) (*User, error)
    List(ctx context.Context) ([]*User, error)
}
```

To connect to a real user system (LDAP, OAuth2 + Google SSO, internal Meesho IdP), you write a new struct that implements these two methods and swap it in at `cmd/server/main.go`. Nothing else in the platform changes — the interface is the contract. A real SSO-backed store would typically not expose `List` (the login screen would redirect to the IdP instead), but the interface is satisfied either way.

---

## Roles and permissions

Currently one role is implemented:

```go
const (
    RoleAdmin = "admin" // superuser: every capability
)
```

`RoleAdmin` grants all six permissions. Any user with an unknown role (or an empty role claim) falls back to `RoleAdmin` via `DefaultRole`.

### The six permissions

| Permission | Admin | What it gates |
|-----------|:-----:|--------------|
| `task:read` | ✅ | View task config, stats, run history, shadow reports |
| `task:predict` | ✅ | Call the predict endpoint |
| `task:write` | ✅ | Create/update tasks, save prompt drafts, run Studio tests, shadow comparisons |
| `task:deploy` | ✅ | Activate a prompt version into production |
| `task:delete` | ✅ | Delete prompt versions (irreversible) |
| `task:view_prompt` | ✅ | See prompt template + system prompt text |

### Planned roles (not yet implemented)

The permission model is designed for a multi-role workflow where authoring and publishing are held by different people. The planned role split — `creator`, `approver`, `caller`, `viewer` — is documented here as intent; the `rolePermissions` map in `internal/auth/rbac.go` is the single place to add them when ready.

| Permission | Creator | Approver | Caller | Viewer |
|-----------|:-------:|:--------:|:------:|:------:|
| `task:read` | ✅ | ✅ | ✅ | ✅ |
| `task:predict` | ✅ | ✅ | ✅ | ❌ |
| `task:write` | ✅ | ❌ | ❌ | ❌ |
| `task:deploy` | ❌ | ✅ | ❌ | ❌ |
| `task:delete` | ❌ | ❌ | ❌ | ❌ |
| `task:view_prompt` | ✅ | ✅ | ❌ | ✅ |

**Design intent:** `task:write` and `task:deploy` are deliberately separate. A Creator can write drafts but cannot make them live; an Approver can publish but cannot author. This enforces a review gate — the person who changes the prompt is different from the person who approves it.

**Why would Callers not see the prompt?** The Caller role is for backend services and integrations. Their contract is "send these inputs, get this output." Hiding the prompt means the product team can improve it without touching the caller's code (the interface is the schema, not the prompt text).

---

## Middleware: how auth is enforced

Two middleware functions wrap protected routes:

### `RequireAuth`

```
Request arrives → 
  "Does it have a cookie named 'llm_platform_token'?" 
  OR "Does the Authorization header say 'Bearer <token>'?"
  → Parse JWT → Verify signature → Check expiry
  → If OK: attach user to request context, continue
  → If not OK: 401 Unauthorized
```

> **🔤 Go concept: middleware**
> HTTP middleware is a function that wraps a handler. The pattern in Go is:
> ```go
> func RequireAuth(next http.Handler) http.Handler {
>     return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
>         // ... validate auth ...
>         next.ServeHTTP(w, r)  // call the actual handler
>     })
> }
> ```
> `next` is the handler that would normally handle the request. The middleware decides whether to call `next` (auth OK) or short-circuit with an error (auth fail).

### `RequirePermission`

```
User is already authenticated (RequireAuth ran first) →
  "Does user.Role grant this permission?"
  → If YES: continue to handler
  → If NO: 403 Forbidden
```

Both are applied via the router's middleware stack. Routes that need auth wrap their handlers in `RequireAuth`. Routes that need specific permissions additionally wrap in `RequirePermission(auth.PermTaskWrite)`, etc.

---

## The `issue-token` CLI tool

`cmd/issue-token/main.go` is a separate program for minting long-lived service tokens outside the web login flow. It reads `JWT_SECRET` from the environment (or a `.env` file in the repo root).

```bash
# Mint a 1-year token for a machine caller (service principal)
JWT_SECRET=<secret> go run ./cmd/issue-token \
  -sub svc:my-service \
  -email my-service@svc.local \
  -name "My Service" \
  -ttl 8760h
# → eyJhbGciOiJ...  (token on stdout)
```

| Flag | Required | Default | Description |
|------|----------|---------|-------------|
| `-sub` | yes | — | Principal subject, e.g. `svc:cis`. Prefix `svc:` by convention for machine callers. |
| `-email` | yes | — | Email for display in run attribution. |
| `-name` | no | `""` | Display name. |
| `-role` | no | `admin` | RBAC role. Only `admin` is valid currently. |
| `-ttl` | no | `8760h` (1 year) | Token lifetime. |
| `-issuer` | no | env `AUTH_ISSUER` or `llm-platform-demo` | JWT issuer claim. |

This is useful for:
- Minting service-principal tokens for backend integrations (`svc:` prefix subjects show up distinctly in run attribution and dashboards).
- Testing API endpoints directly with `curl` or Postman using a `Bearer` token.

---

## Why JWTs instead of session tokens stored in a database?

> **Alternative:** Store session tokens in a database table. On every request, look up the token in the DB to get the user.

**Database sessions advantages:**
- Can revoke a token instantly (delete the row).
- Can list all active sessions.

**JWT advantages (why this platform uses them):**
- **No DB lookup on every request.** The token is self-contained and verifiable with just the secret key. Every prediction call goes through auth middleware — a DB lookup on every prediction call would add ~1ms latency and one DB read per request.
- **Horizontal scaling.** Any server instance can verify any JWT without shared state. Database session tokens require all instances to share the same DB.

**Tradeoff:** JWTs can't be revoked before expiry (12 hours by default). If a token is compromised, it's valid until it expires. For this platform (internal tool, 12h expiry), this is acceptable. For a public consumer app, you'd add a token revocation list.
