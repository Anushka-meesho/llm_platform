# Server Startup

Source: [llm_platform_go/cmd/server/main.go](../llm_platform_go/cmd/server/main.go)

The server boots in a fixed sequence. Each step either succeeds and moves on, or stops the process entirely — there is no partial startup. This section explains what each step does, what kills it, and why the order is the way it is.

## Boot sequence

### Step 1 — Load .env file
```
godotenv.Load()
```
Reads `.env` from the working directory if it exists. Real environment variables always take precedence. This step never fails; if there is no `.env` file that is fine.

### Step 2 — Load and validate config
```
config.Load()
```
Reads all environment variables into a `Config` struct. **Fatal if** neither `GROQ_API_KEY` nor `MEESHO_GATEWAY_VK` is set — the server would boot but silently fail every prediction, so it stops early instead.

See [02-config.md](02-config.md) for the full field list.

### Step 3 — Load pricing table
```
llm.LoadPricing(cfg.PricingPath)
```
Parses `pricing.json` once. **Fatal if** the file is missing or malformed — cost calculation would silently return 0 for every prediction.

### Step 4 — Open database and run migrations
```
db.Open(cfg.DBPath)
db.Migrate()
```
Opens SQLite in WAL mode with `MaxOpenConns(1)` and a 5-second busy timeout. Then runs `CREATE TABLE IF NOT EXISTS` for all six tables, plus guarded `ALTER TABLE` statements for columns added in later versions. **Fatal if** the file cannot be opened or a migration fails.

Must happen before clients are built (the RunWriter needs an open DB) and before the task registry is seeded (tasks are read from the DB).

### Step 5 — Build LLM clients
```
llm.BuildClients(cfg)
```
Constructs HTTP clients for Groq (Bearer auth) and the Meesho bifrost gateway (x-bf-vk header). Clients share a 120-second HTTP timeout; context deadlines cancel individual requests. Does not make a network call — client construction never fails.

### Step 6 — Start the recovery prober
```
llm.StartRecoveryProber(clients, 15*time.Second)
```
Launches a background goroutine that pings every unhealthy provider every 15 seconds. A successful ping closes the circuit; a failed ping leaves it open. **This is the only way a provider circuit closes** — production requests never touch an open-circuit provider (see [06-circuit-breaker.md](06-circuit-breaker.md)).

Must start after clients are built. Must start before the router serves traffic, otherwise a tripped circuit at boot could never recover until the first request manually re-checks.

### Step 7 — Seed the task registry
```
tasks.Seed(db, cfg.TasksDir)
```
Loads the built-in playground task and any YAML task configs from `./tasks.d/` (or the directory set by `TASKS_DIR`). **Fatal if** a task config is invalid — prevents silent startup with broken routing.

### Step 8 — Initialise user store
```
users.NewDemoStore()
```
Creates four in-memory demo users (one per role). Never fails. Real deployments swap this for a proper `Store` implementation backed by an identity provider; the auth layer only calls the `Store` interface.

### Step 9 — Start async writers
```
db.NewRunWriter(db, 1024)
db.NewHealthEventWriter(db, 256)
```
Starts two channel-backed goroutines that drain DB inserts off the prediction hot path. The `1024` and `256` are channel buffer sizes — if the drain falls behind and the buffer fills, rows are silently dropped and a counter is incremented. **Predictions never block on DB writes.**

### Step 10 — Create health tracker
```
health.NewTracker(cfg)
```
Initialises the per-(task, model) circuit breaker map. Configured with the threshold, base cooldown, and max cooldown from config. Never fails.

### Step 11 — Initialise prediction cache
```
cache.New(cfg)
```
If `REDIS_ADDR` is set, connects to Redis. If `CACHE_BACKEND=memory`, uses an in-process LRU. Otherwise cache is off. Redis connection failure at this step is **non-fatal** — the server continues without caching (predictions still work, just slower and costlier).

### Step 12 — Build router
```
api.NewRouter(...)
```
Creates the chi HTTP mux, registers CORS, RequestID, and auth middleware, and mounts all route groups. Never fails.

### Step 13 — Listen
```
http.ListenAndServe(":"+cfg.Port, router)
```
Blocks until the process exits. Fatal on bind error (port in use, permission denied).

## Error-handling summary

| Step | Fatal on failure? | Why |
|------|:-----------------:|-----|
| .env load | No | Missing file is normal |
| Config load | Yes | No provider key = every prediction silently fails |
| Pricing load | Yes | Cost calculation would return 0 silently |
| DB open + migrate | Yes | No DB = no observability, no task registry |
| Build clients | No | Construction cannot fail |
| Start prober | No | Goroutine spawn cannot fail |
| Seed tasks | Yes | Invalid task config = silent broken routing |
| User store | No | In-memory, cannot fail |
| Start writers | No | Goroutine spawn cannot fail |
| Health tracker | No | In-memory, cannot fail |
| Cache init | No (Redis) | Predictions work without cache |
| Build router | No | Pure in-memory setup |
| Listen | Yes | Cannot serve without binding the port |
