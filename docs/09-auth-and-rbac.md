# Auth and RBAC

Sources: [llm_platform_go/internal/auth/auth.go](../llm_platform_go/internal/auth/auth.go), [llm_platform_go/internal/auth/rbac.go](../llm_platform_go/internal/auth/rbac.go)

## JWT implementation

Tokens are signed with HMAC-SHA256 (HS256) using the shared secret from `JWT_SECRET`.

**Claims struct:**
```go
type Claims struct {
    Email string
    Name  string
    Role  string
    jwt.RegisteredClaims  // Subject, Issuer, IssuedAt, ExpiresAt
}
```

`IssueToken(user, secret, issuer, expiry)` mints a signed token. `ParseToken(tokenStr, secret)` validates signature, expiry, and issuer — returns a `User` on success, error otherwise.

## HttpOnly cookies

**Reading a token** (`TokenFromRequest`): checks two sources in order:
1. `Authorization: Bearer <token>` header — for service principals calling the API directly.
2. HttpOnly cookie named `AUTH_COOKIE_NAME` — for browser sessions.

**Setting a cookie** (`SetAuthCookie`):
```
Set-Cookie: llm_platform_token=<jwt>
HttpOnly; Secure (prod only); SameSite=Lax; MaxAge=<TOKEN_EXPIRY>
```

- **HttpOnly** — JavaScript cannot read the token. Protects against XSS exfiltrating credentials.
- **Secure** — HTTPS only. Set `COOKIE_SECURE=true` in production.
- **SameSite=Lax** — Cookie is sent on top-level navigation and same-site requests, but not on cross-site form posts. Mitigates CSRF without requiring a token.
- **MaxAge** — Derived from `TOKEN_EXPIRY` (default 12h). Browser deletes the cookie after this duration.
- **Domain** — Set from `COOKIE_DOMAIN` (e.g., `.meesho.internal` for cross-subdomain SSO). Empty means the browser uses the current host.

**Clearing a cookie** (`ClearAuthCookie`): sets `MaxAge=-1`, which tells the browser to delete the cookie immediately.

## Five roles and six permissions

| Permission | admin | creator | approver | caller | viewer |
|-----------|:-----:|:-------:|:--------:|:------:|:------:|
| `PermTaskRead` | ✓ | ✓ | ✓ | ✓ | ✓ |
| `PermTaskPredict` | ✓ | ✓ | ✓ | ✓ | ✗ |
| `PermTaskWrite` | ✓ | ✓ | ✗ | ✗ | ✗ |
| `PermTaskDeploy` | ✓ | ✗ | ✓ | ✗ | ✗ |
| `PermTaskDelete` | ✓ | ✗ | ✗ | ✗ | ✗ |
| `PermTaskViewPrompt` | ✓ | ✓ | ✓ | ✗ | ✓ |

**Permission meanings:**

| Permission | Gates |
|-----------|-------|
| `PermTaskRead` | List tasks, read config, view stats, runs, shadow reports |
| `PermTaskPredict` | `POST /v1/tasks/{id}/predict` |
| `PermTaskWrite` | Create/update tasks, save draft versions, Studio test runs, shadow comparisons |
| `PermTaskDeploy` | Activate a prompt version into production |
| `PermTaskDelete` | Permanently remove a task or prune prompt versions |
| `PermTaskViewPrompt` | See prompt template text and version body |

**Design intent:**
- **creator + approver separation** enforces a two-person review workflow: a creator writes the prompt (`PermTaskWrite`), an approver activates it (`PermTaskDeploy`). Neither can do both.
- **caller lacks `PermTaskViewPrompt`** — callers integrate against the task's input/output schema contract. They don't need to see the prompt, and hiding it lets teams iterate on prompts without callers depending on exact wording.
- **admin is the only role with `PermTaskDelete`** — destructive operations are restricted to reduce accidents.

## Default role

If a JWT's `Role` claim is empty or absent, the user defaults to `RoleCaller`. This means:
- Legacy service tokens minted before RBAC was added continue to work as callers.
- New tokens that forget to set a role still get a usable least-privilege identity.

## Auth middleware

```
RequireAuth          — validates JWT, places User on context.Context
RequireAdmin         — checks role == "admin" (not capability-based)
RequirePermission(p) — checks user.Can(p)
```

`user.Can(perm)` looks up the role in the permission matrix above and returns true/false. The middleware returns **401** if no valid token is present and **403** if the token is valid but the role lacks the required permission.

## Demo SSO

At startup, `users.NewDemoStore()` seeds four in-memory users — one admin, one creator, one approver, one caller:

```
GET /auth/demo-users   → list seeded demo users
POST /auth/login       → { "user_id": "..." } → sets HttpOnly cookie + returns token
DELETE /auth/logout    → clears the cookie
```

This is intentionally simple: the `Store` interface abstracts the user lookup. A real deployment replaces `NewDemoStore()` with an implementation backed by an IdP (LDAP, OAuth2, internal SSO). The JWT minting and cookie mechanics are unchanged.

## Service tokens (non-browser callers)

For services that call `/predict` programmatically (not through the browser), issue a long-lived token with a specific role:

```bash
go run ./cmd/issue-token \
  -sub svc:cis \
  -email cis@svc.local \
  -name "CIS Service" \
  -role caller \
  -ttl 8760h
```

The token is passed as `Authorization: Bearer <token>` in every request. It never touches a cookie.
