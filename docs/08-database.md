# Database

Sources: [llm_platform_go/internal/db/db.go](../llm_platform_go/internal/db/db.go), [llm_platform_go/internal/db/queries.go](../llm_platform_go/internal/db/queries.go), [llm_platform_go/internal/db/runwriter.go](../llm_platform_go/internal/db/runwriter.go), [llm_platform_go/internal/db/healthwriter.go](../llm_platform_go/internal/db/healthwriter.go)

## SQLite configuration

Three pragmas are set at every connection open:

```sql
PRAGMA journal_mode=WAL;
PRAGMA busy_timeout=5000;
```

Combined with:
```go
db.SetMaxOpenConns(1)
```

**WAL mode** (Write-Ahead Logging) allows multiple readers to proceed concurrently while one writer is active. Without WAL, a write would lock the entire file and readers would block.

**Single writer** (`MaxOpenConns(1)`) ensures only one goroutine writes at a time, preventing `SQLITE_BUSY` (database locked) errors under concurrent requests. Readers use separate connections from the pool and are not affected.

**Busy timeout** makes readers wait up to 5 seconds for the writer to finish before returning a lock error. This smooths over brief write spikes without returning errors to callers.

## Schema

Six tables, created idempotently at boot with `CREATE TABLE IF NOT EXISTS`. New columns added in later versions use guarded `ALTER TABLE ... ADD COLUMN IF NOT EXISTS` statements.

### `runs` — prediction history

The core observability table. One row per prediction attempt.

| Column | Type | Notes |
|--------|------|-------|
| `run_id` | TEXT PK | UUID |
| `session_id` | TEXT | Client-supplied or auto-generated |
| `prompt` | TEXT | Rendered prompt text |
| `system_prompt` | TEXT | Task system prompt |
| `image` | TEXT | JSON array of image data URLs (null if none) |
| `model` | TEXT | Model that answered |
| `response` | TEXT | Raw response text (null on error) |
| `latency_ms` | INTEGER | Wall-clock duration of live call |
| `input_tokens` | INTEGER | |
| `output_tokens` | INTEGER | |
| `total_tokens` | INTEGER | |
| `cost_usd` | REAL | Calculated cost |
| `success` | INTEGER | 1 = success, 0 = error |
| `error` | TEXT | Error message (null on success) |
| `user_id` | TEXT | From JWT `sub` claim |
| `user_email` | TEXT | From JWT claims |
| `task_id` | TEXT | Task slug |
| `prompt_version` | INTEGER | Task's prompt version at call time |
| `provider` | TEXT | `openai`, `groq`, `gemini`, or `anthropic` |
| `fallback_used` | INTEGER | 1 if served by non-primary model |
| `cache_hit` | INTEGER | 1 if served from cache |
| `is_test` | INTEGER | 1 if Studio test run |
| `created_at` | DATETIME | UTC |

Indexes: `run_id` (PK), `session_id`, `user_id`, `task_id`, `created_at`.

### `feedback` — model ratings

User ratings within a session (1–5 stars per model per run).

| Column | Notes |
|--------|-------|
| `run_id` | FK → runs |
| `model` | Which model's response is being rated |
| `user_id` | Rater |
| `rating` | 1–5 |

Unique index on `(run_id, model, user_id)` — one rating per (run, model, user).

### `tasks` — task registry

Persisted task definitions. The in-memory registry is populated from this table at boot and kept in sync on writes.

Columns mirror the `Task` struct: `id`, `name`, `description`, `input_schema`, `output_schema`, `prompt_template`, `system_prompt`, `prompt_version`, `model`, `fallback_models` (JSON array), `temperature`, `max_tokens`, `daily_budget_usd`, `cache_enabled`, `cache_ttl_seconds`, `active`, `created_at`, `updated_at`.

### `prompt_versions` — version history

One row per deployed version of each task's prompt.

| Column | Notes |
|--------|-------|
| `id` | Auto-increment PK |
| `task_id` | FK → tasks |
| `version` | Version number |
| `prompt_template` | Full template text at this version |
| `system_prompt` | System prompt at this version |
| `note` | Optional deploy note |
| `created_by` | User ID of deployer |
| `created_at` | UTC |

Unique index on `(task_id, version)`.

### `shadow_reports` — comparison test results

Results of shadow-mode A/B tests run through Studio.

| Column | Notes |
|--------|-------|
| `id` | Auto-increment PK |
| `task_id` | |
| `created_by` | User who ran the test |
| `items` | Number of test inputs run |
| `match_rate` | Fraction of outputs matching (0–1) |
| `avg_latency_ms` | |
| `p95_latency_ms` | |
| `total_cost_usd` | |
| `details` | JSON: per-item breakdown |
| `created_at` | |

### `model_health_events` — circuit breaker history

One row per state transition in the per-(task, model) health tracker.

| Column | Notes |
|--------|-------|
| `id` | Auto-increment PK |
| `task_id` | |
| `model` | Model routing key |
| `provider` | Provider name |
| `event` | `failure`, `tripped`, `recovered`, `manual_reset` |
| `reason` | Human-readable cause |
| `consecutive_failures` | At time of event |
| `cooldown_ms` | Cooldown applied (for `tripped`) |
| `state` | Resulting state |
| `created_at` | |

Indexes: `(task_id, model)`, `created_at`.

## Async write pattern

Prediction hot-path code never blocks on DB writes. Two channel-backed writers run as background goroutines:

**RunWriter** (buffer: 1024):
```
prediction handler
    │ (non-blocking send)
    ▼
channel [1024]
    │ (drain goroutine)
    ▼
DB INSERT into runs
```

**HealthEventWriter** (buffer: 256):
```
health tracker
    │ (non-blocking send)
    ▼
channel [256]
    │ (drain goroutine)
    ▼
DB INSERT into model_health_events
```

**Drop semantics:** If the channel is full (drain is falling behind), the sender increments a drop counter and discards the row. Predictions never block waiting for a DB write. The drop counter is observable via metrics but does not affect correctness — it just means some history rows are missing.

**Synchronous fallback:** If no writer is configured (tests, simple setups), `insertRun` calls the DB directly. This is safe in tests where there is no concurrent drain goroutine.

## Key queries

| Function | What it does |
|----------|-------------|
| `TaskSpendToday(taskID)` | `SUM(cost_usd)` where `created_at >= today UTC` — used by the budget gate |
| `TaskDailyStats(taskID, days)` | Per-day aggregates (cost, tokens, run count) for the last N days |
| `ListSessions(userID, page, pageSize)` | Paginated session list for a user, newest first |
| `GetSession(userID, sessionID)` | Full conversation: all turns with per-model results |
| `GetLeaderboard(userID, sessionID)` | Average ratings by model for a session |
| `AdminListRuns(filters, page, pageSize)` | Cross-tenant run history with optional filters (task, user, date range) |
| `AdminGetRun(runID)` | Full run details including images (admin only) |
