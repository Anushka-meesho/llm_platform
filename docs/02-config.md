# Config

Source: [llm_platform_go/internal/config/config.go](../llm_platform_go/internal/config/config.go)

All configuration is read from environment variables at boot. A `.env` file in the working directory is loaded first; real environment variables always override it.

## Complete field reference

### Provider keys

| Env var | Type | Default | Notes |
|---------|------|---------|-------|
| `GROQ_API_KEY` | string | — | Groq API key. Sent as `Authorization: Bearer <key>`. At least one of `GROQ_API_KEY` or `MEESHO_GATEWAY_VK` must be set or the server refuses to start. |
| `MEESHO_GATEWAY_VK` | string | — | Meesho bifrost virtual key. Sent as `x-bf-vk: <key>`. Covers OpenAI, Gemini, and Anthropic through the internal gateway. |

### Server

| Env var | Type | Default | Notes |
|---------|------|---------|-------|
| `PORT` | string | `8000` | HTTP listen port. |
| `TASKS_DIR` | string | `./tasks.d` | Directory scanned for YAML task configs at boot. |

### Database

| Env var | Type | Default | Notes |
|---------|------|---------|-------|
| `DB_PATH` | string | `./llm_platform.db` | SQLite file path. Created on first boot. |
| `PRICING_PATH` | string | `./pricing.json` | JSON file with per-model token rates. |

### LLM endpoints

| Env var | Type | Default | Notes |
|---------|------|---------|-------|
| `GROQ_BASE_URL` | string | `https://api.groq.com/openai/v1` | Override to point Groq traffic at a proxy or local mock. |
| `MEESHO_GATEWAY_BASE_URL` | string | `http://llm-gateway.prd.meesho.int/v1` | Override to point gateway traffic at a different environment. |

### Auth and sessions

| Env var | Type | Default | Notes |
|---------|------|---------|-------|
| `JWT_SECRET` | string | `dev-insecure-secret-change-me` | HMAC-SHA256 signing key. **Must be changed in production.** Any string works; longer is better. |
| `AUTH_COOKIE_NAME` | string | `llm_platform_token` | Name of the HttpOnly session cookie. |
| `AUTH_ISSUER` | string | `llm-platform-demo` | JWT `iss` claim. Used to reject tokens minted for other services. |
| `COOKIE_DOMAIN` | string | (empty) | Cookie `Domain` attribute. Empty means the browser uses the current host. Set to `.meesho.internal` for cross-subdomain sessions. |
| `COOKIE_SECURE` | bool | `false` | Set the `Secure` flag on cookies (HTTPS only). Always `true` in production. |
| `TOKEN_EXPIRY` | duration | `12h` | Session token lifetime. Accepts Go duration strings: `8760h` = 1 year. |

### Cache

| Env var | Type | Default | Notes |
|---------|------|---------|-------|
| `REDIS_ADDR` | string | (empty) | Redis address, e.g. `localhost:6379`. Setting this activates the Redis cache backend. |
| `REDIS_PASSWORD` | string | (empty) | Redis AUTH password. Empty = no auth. |
| `REDIS_DB` | int | `0` | Redis database number. |
| `CACHE_BACKEND` | string | (derived) | Explicit backend override: `"redis"`, `"memory"`, or `"off"`. If unset, the server picks `redis` when `REDIS_ADDR` is set, `off` otherwise. Set to `"memory"` for local dev without Redis. |

### Health / circuit breaker

| Env var | Type | Default | Notes |
|---------|------|---------|-------|
| `HEALTH_BREAKER_ENABLED` | bool | `true` | Disable the per-(task, model) circuit breaker entirely. Useful for debugging. |
| `HEALTH_FAILURE_THRESHOLD` | int | `3` | Consecutive failures before a (task, model) pair is marked unhealthy. |
| `HEALTH_BASE_COOLDOWN` | duration | `30s` | Initial unhealthy window before a probe is allowed. Doubles on each re-trip. |
| `HEALTH_MAX_COOLDOWN` | duration | `30m` | Maximum cooldown after repeated failures. Exponential backoff is capped here. |

## 12-factor compliance

- **Config in the environment.** No secrets or environment-specific values are hardcoded. All are read from env vars.
- **.env for local development only.** `godotenv.Load()` is intentionally lenient (missing file is fine). Production deployments inject env vars via their deployment platform; the `.env` file is git-ignored.
- **Sensible defaults for non-sensitive settings.** `PORT`, `DB_PATH`, `AUTH_COOKIE_NAME`, etc. have defaults that work out of the box. Sensitive keys (`JWT_SECRET`, provider API keys) have no default — the server fails fast rather than starting with an insecure value.
- **Fail at boot, not at call time.** Missing provider keys are caught in `config.Load()`, not on the first prediction. The server refuses to start rather than boot and silently error on every request.

## Local dev setup

Copy `.env.example` and fill in your keys:

```bash
cp .env.example .env
# Edit .env: set GROQ_API_KEY or MEESHO_GATEWAY_VK
# Optionally: CACHE_BACKEND=memory for cache without Redis
```

Then start the server:

```bash
go run ./cmd/server
```

The DB file and SQLite schema are created automatically on first boot.
