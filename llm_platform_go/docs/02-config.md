# 02 — Configuration

## How configuration works

The server reads all its settings from **environment variables** — key-value pairs that the operating system makes available to any running process. On your laptop, you set these in a `.env` file. In production (a server or container), they're injected by the deployment system.

> **🔤 Go concept: structs**
> A **struct** in Go is a named collection of fields — like a row in a table, or a JavaScript object with fixed keys. Here, `Config` is a struct that holds all the settings the server needs. When `config.Load()` runs, it creates one `Config` value and fills every field from an env var (or a default).
>
> ```go
> type Config struct {
>     GroqKey  string  // "string" means text
>     Port     string
>     DBPath   string
>     // ... more fields
> }
> ```

---

## Why environment variables?

> **Alternatives considered:** A config file (JSON, YAML, TOML), command-line flags, database-stored config

The [12-Factor App](https://12factor.net/config) methodology says: **separate config from code**. Config changes between environments (your laptop vs. staging vs. production), but the code doesn't. Environment variables make this separation clean:

- The same binary works in every environment without recompilation.
- No secret keys are ever in the source code or config files.
- Deployment systems (Docker, Kubernetes, Heroku) know how to inject env vars natively.

---

## The `.env` file

The `.env` file at `llm_platform_go/.env` is a developer convenience — it lets you set env vars without typing `export GROQ_API_KEY=... && go run ...` every time.

> ⚠️ **This file is gitignored.** It's listed in `.gitignore` so it's never committed. If you clone the repo fresh, there's no `.env` file — you create one yourself. This is intentional: real API keys should never be in version control.

The server loads it with `godotenv.Load()` at startup. If the file doesn't exist (e.g., production environments use real env vars), `godotenv.Load()` silently does nothing.

---

## Every configuration field

### Provider keys

| Env var | Config field | What it does | Required? |
|---------|-------------|--------------|-----------|
| `GROQ_API_KEY` | `GroqKey` | API key for the Groq API (direct). Used for `llama-groq`. | One of Groq or Meesho must be set |
| `MEESHO_GATEWAY_VK` | `MeeshoGatewayVK` | Virtual key for Meesho's bifrost gateway. Used for GPT-4o, Gemini, Claude. | One of Groq or Meesho must be set |
| `MEESHO_GATEWAY_BASE_URL` | `MeeshoGatewayBaseURL` | Gateway base URL | Default: `http://llm-gateway.prd.meesho.int/v1` |
| `GROQ_BASE_URL` | `GroqBaseURL` | Groq API base URL (override to use a proxy) | Default: `https://api.groq.com/openai/v1` |

At startup, if **neither** `GROQ_API_KEY` nor `MEESHO_GATEWAY_VK` is set, the server refuses to start:
```
no provider API keys set: configure at least one of GROQ_API_KEY, MEESHO_GATEWAY_VK
```

If one is missing (but not both), the server starts with a warning: models that use the missing provider will fail at call time.

---

### Storage

| Env var | Config field | What it does | Default |
|---------|-------------|--------------|---------|
| `DB_PATH` | `DBPath` | Path to the SQLite database file | `./llm_platform.db` |
| `PORT` | `Port` | HTTP port to listen on | `8000` |
| `PRICING_PATH` | `PricingPath` | Path to the pricing JSON file | `./pricing.json` |
| `TASKS_DIR` | *(read directly)* | Directory of YAML task configs | `./tasks.d` |

---

### Authentication

| Env var | Config field | What it does | Default |
|---------|-------------|--------------|---------|
| `JWT_SECRET` | `JWTSecret` | Secret key used to sign session tokens | `dev-insecure-secret-change-me` |
| `AUTH_COOKIE_NAME` | `AuthCookieName` | Name of the session cookie | `llm_platform_token` |
| `AUTH_ISSUER` | `AuthIssuer` | JWT `iss` claim (identifies this server) | `llm-platform-demo` |
| `COOKIE_DOMAIN` | `CookieDomain` | Cookie domain (leave empty for localhost) | empty |
| `COOKIE_SECURE` | `CookieSecure` | Set `Secure` flag (HTTPS only) | `false` |
| `TOKEN_EXPIRY` | `TokenExpiry` | How long before a session token expires | `12h` |

> ⚠️ **Change `JWT_SECRET` in production.** The default value is published in this codebase. Anyone who knows it can forge valid session tokens. Set a long, random value like `openssl rand -hex 32`.

---

### Prediction cache

| Env var | Config field | What it does | Default |
|---------|-------------|--------------|---------|
| `REDIS_ADDR` | `RedisAddr` | Redis server address. If set, enables Redis cache. | empty (off) |
| `REDIS_PASSWORD` | `RedisPassword` | Redis auth password (if needed) | empty |
| `REDIS_DB` | `RedisDB` | Redis DB number (0–15) | `0` |
| `CACHE_BACKEND` | `CacheBackend` | Force a specific backend: `"redis"`, `"memory"`, or `"off"` | auto-derived |

**Auto-derivation logic:**
- `REDIS_ADDR` is set → `CacheBackend = "redis"`
- `REDIS_ADDR` is empty → `CacheBackend = "off"`
- Override with `CACHE_BACKEND=memory` for an in-process memory cache (useful for local dev without Redis)

---

### Per-(task, model) circuit breaker

| Env var | Config field | What it does | Default |
|---------|-------------|--------------|---------|
| `HEALTH_BREAKER_ENABLED` | `HealthBreakerEnabled` | Master on/off switch | `true` |
| `HEALTH_FAILURE_THRESHOLD` | `HealthThreshold` | Consecutive failures before a model is marked unhealthy | `3` |
| `HEALTH_BASE_COOLDOWN` | `HealthBaseCooldown` | First unhealthy window after tripping | `30s` |
| `HEALTH_MAX_COOLDOWN` | `HealthMaxCooldown` | Maximum unhealthy window (exponential backoff cap) | `30m` |

With defaults: after 3 consecutive failures, a model is skipped for 30 seconds. If it fails again after recovery, it's skipped for 60s, then 120s, up to 30 minutes.

Set `HEALTH_BREAKER_ENABLED=false` if you want every model called regardless of health (useful for debugging).

---

## How defaults work in Go

```go
func getEnvOrDefault(key, fallback string) string {
    if v := os.Getenv(key); v != "" {
        return v
    }
    return fallback
}
```

> **🔤 Go concept: `os.Getenv`**
> `os.Getenv("KEY")` returns the value of an environment variable as a string. If the variable isn't set, it returns an empty string `""`. The helper `getEnvOrDefault` says: "give me the env var, but if it's not set or empty, use this fallback".

For durations (like `30s`, `12h`), Go's `time.ParseDuration` converts strings like `"30s"`, `"5m"`, `"1h30m"` into Go's internal duration type. You can use any of these formats in env vars.

---

## Example `.env` for local development

```bash
# Required: at least one provider
GROQ_API_KEY=gsk_...your_key_here...
MEESHO_GATEWAY_VK=sk-bf-...your_key_here...

# Optional: auth (keep the default for local dev)
# JWT_SECRET=change-me-for-production

# Optional: use in-memory cache instead of Redis
CACHE_BACKEND=memory

# Optional: circuit breaker tuning
# HEALTH_BREAKER_ENABLED=true
# HEALTH_FAILURE_THRESHOLD=3
# HEALTH_BASE_COOLDOWN=30s
```
