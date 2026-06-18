# 09 — Authentication and RBAC

## How identity works

The platform uses **JWT (JSON Web Tokens)** for authentication, stored in an **HttpOnly cookie**.

The flow:
1. User logs in via `POST /auth/login` with email + password.
2. The server validates credentials against the user store.
3. If valid, the server creates a JWT, signs it with `JWT_SECRET`, and sends it back as a cookie.
4. Every subsequent request includes that cookie automatically (the browser sends cookies with every request).
5. The `RequireAuth` middleware validates the JWT on every protected route.

No session stored on the server. The JWT itself contains the user's identity and role.

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

## The login flow

```
POST /auth/login
Body: {"email": "alice@meesho.com", "password": "..."}

1. userStore.GetByEmail("alice@meesho.com")
   → If not found: 401 Unauthorized

2. Verify password hash
   → If wrong: 401 Unauthorized

3. Issue JWT:
   Claims{
     Subject: user.ID,
     Email:   user.Email,
     Name:    user.Name,
     Role:    user.Role,
     Issuer:  "llm-platform-demo",
     Expires: now + 12h,
   }
   token = sign(claims, JWT_SECRET)

4. Set cookie:
   Set-Cookie: llm_platform_token=<token>; Path=/; HttpOnly; SameSite=Lax

5. Return: 200 OK {"user": {...}}
```

---

## The demo user store

The current `users.DemoStore` is a hardcoded list of users in memory. It's not a real database — it's a **swap seam**: a placeholder that demonstrates the interface a real user store needs to implement.

```go
type Store interface {
    GetByEmail(email string) (*User, error)
    GetByID(id string)      (*User, error)
}
```

To connect to a real user system (LDAP, OAuth2 + Google SSO, internal Meesho IdP), you write a new struct that implements these two methods. Nothing else in the platform changes — the interface is the contract.

---

## The five roles

```go
const (
    RoleAdmin    = "admin"    // everything
    RoleCreator  = "creator"  // author and iterate prompts, but cannot publish
    RoleApprover = "approver" // own the publish gate
    RoleCaller   = "caller"   // service principal — call predict only
    RoleViewer   = "viewer"   // read-only access
)
```

### What each role can do

| Permission | Admin | Creator | Approver | Caller | Viewer |
|-----------|:-----:|:-------:|:--------:|:------:|:------:|
| `task:read` — view task config, stats, run history | ✅ | ✅ | ✅ | ✅ | ✅ |
| `task:predict` — call the predict endpoint | ✅ | ✅ | ✅ | ✅ | ❌ |
| `task:write` — create/update tasks, save drafts, run Studio tests | ✅ | ✅ | ❌ | ❌ | ❌ |
| `task:deploy` — activate a prompt version into production | ✅ | ❌ | ✅ | ❌ | ❌ |
| `task:delete` — delete prompt versions | ✅ | ❌ | ❌ | ❌ | ❌ |
| `task:view_prompt` — see prompt template + system prompt text | ✅ | ✅ | ✅ | ❌ | ✅ |

**Key design insight:** `task:write` and `task:deploy` are deliberately separate. A Creator can write new prompt drafts all day but cannot make them live. An Approver can make things live but cannot write new prompts. This enforces a review gate: a person who changes the prompt is different from the person who approves it.

**Why can't Callers see the prompt?** The Caller role is for backend services and integrations. Their contract is "send these inputs, receive this output." The prompt text is implementation detail — intellectual property of the product team. Hiding it also means the product team can improve prompts without updating the caller's code (the interface is the schema, not the prompt).

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

`cmd/issue-token/main.go` is a separate program for minting tokens outside the web flow. Usage:

```bash
go run ./cmd/issue-token -email admin@meesho.com -role admin -secret $JWT_SECRET
# → eyJhbGciOiJ...  (copy this token)
```

This is useful for:
- Creating admin tokens for initial setup.
- Minting service-principal tokens (Caller role) for backend integrations.
- Testing API endpoints directly with tools like `curl` or Postman.

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
