# 08 — The Database

## Why SQLite?

> **Alternatives considered:** PostgreSQL, MySQL, MongoDB

SQLite is an embedded database — it's a library that runs inside the Go process, and stores data in a single file. There's no separate "database server" process to start, configure, or monitor.

| | SQLite | PostgreSQL |
|-|--------|-----------|
| Setup | Zero (it's a file) | Install Postgres, create DB, configure connection |
| Backups | `cp llm_platform.db backup.db` | `pg_dump`, replicas, WAL shipping |
| Concurrent writers | 1 at a time (serialize with mutex) | Many concurrent writers |
| Scale | ~50 writes/sec comfortably with WAL | Thousands of writes/sec |
| Production readiness | Fine for one server instance | Required for multiple server instances |

The platform is currently designed for a single server instance. SQLite is the right choice here. When you need to scale to multiple instances (or need more than ~50 writes/second), migrate to PostgreSQL — the code change is minimal because all DB interactions go through `internal/db/queries.go`.

---

## WAL mode: concurrent reads without conflicts

By default, SQLite uses a journal mode that locks the entire database file for every write. Any concurrent read while a write is in progress fails with "database is locked".

**Write-Ahead Logging (WAL)** fixes this. Here's the analogy:

> Imagine a shared whiteboard in an office. The default mode: you erase the board, write your changes, then let others read. Nobody can look while you're writing.
>
> WAL mode: instead of erasing and rewriting, you append your changes to a separate logbook. Others can still read the original whiteboard. Periodically, the logbook changes are copied to the main whiteboard (a "checkpoint"). Readers see a consistent snapshot; writers keep writing.

The practical result: **one writer + unlimited concurrent readers**, no lock contention.

```go
// db.Open sets this up:
db.Exec("PRAGMA journal_mode=WAL")
db.Exec("PRAGMA busy_timeout=5000")  // Wait up to 5s if locked, then error
db.SetMaxOpenConns(1)                 // Only one writer at a time
```

`SetMaxOpenConns(1)` ensures Go's connection pool never opens two connections simultaneously. If two goroutines try to write at the same time, the second one waits (up to 5 seconds) for the first to finish.

---

## The tables

### `runs` — every prediction call

The most important table. One row per model per call. If you call the playground with 3 models, that's 3 rows with the same `run_id`.

| Column | Type | Purpose |
|--------|------|---------|
| `id` | INTEGER PRIMARY KEY | Auto-incrementing row ID (internal) |
| `run_id` | TEXT | UUID shared across all models in one `/run` or `/predict` call |
| `session_id` | TEXT | Optional: groups multiple turns in a conversation |
| `task_id` | TEXT | Which task (e.g. "classify-ticket") — "playground" for /run |
| `prompt` | TEXT | The full rendered prompt sent to the model |
| `system_prompt` | TEXT | The system prompt (if any) |
| `images` | TEXT | JSON array of image URLs/data-URLs (for vision calls) |
| `model` | TEXT | Friendly model name ("gpt-4o") |
| `response` | TEXT | The model's response text |
| `latency_ms` | INTEGER | Time from API call to response in milliseconds |
| `input_tokens` | INTEGER | Prompt token count |
| `output_tokens` | INTEGER | Response token count |
| `total_tokens` | INTEGER | Sum of input + output |
| `cost_usd` | REAL | Calculated dollar cost |
| `success` | INTEGER | 0 or 1 (SQLite has no boolean type) |
| `error` | TEXT | Error message if success=0 |
| `user_id` | TEXT | Who made the call |
| `user_email` | TEXT | Their email (denormalized for fast display) |
| `task_id` | TEXT | Which task |
| `prompt_version` | INTEGER | Which prompt version was active |
| `provider` | TEXT | "openai", "groq", "gemini", "anthropic" |
| `fallback_used` | INTEGER | 0 or 1: was this served by a non-primary model? |
| `cache_hit` | INTEGER | 0 or 1: was this served from cache? |
| `is_test` | INTEGER | 0 or 1: Studio test panel run (filtered from dashboards) |
| `created_at` | TEXT | ISO8601 timestamp |

> **🔤 Go concept: no boolean in SQLite**
> SQLite doesn't have a native boolean type. The code uses `INTEGER` columns (0 or 1) for booleans. When reading, `boolToInt()` converts Go `bool` → `0` or `1` for storage, and scan code converts `0`/`1` back to `bool` when reading.

---

### `tasks` — task configurations

Stores every task created via the API or seeded from YAML. All the fields from the `Task` struct, including the full `prompt_template` and `system_prompt`.

Indexes: `id` (primary key), `active` (for listing active tasks).

---

### `prompt_versions` — version history

Each row is one historical snapshot of a task's prompt.

| Column | Purpose |
|--------|---------|
| `task_id` | Which task this version belongs to |
| `version` | Monotonic integer (always increasing) |
| `prompt_template` | The prompt text at that version |
| `system_prompt` | The system prompt at that version |
| `note` | Optional description ("Fixed JSON format issue") |
| `created_by` | Who saved this version |
| `active` | Is this the currently deployed version? |

---

### `feedback` — user ratings

One row per (run_id, model, user_id) triplet. If a user re-rates an output, the row is updated (UPSERT).

| Column | Purpose |
|--------|---------|
| `run_id` | Which run |
| `model` | Which model's response was rated |
| `rating` | 1–5 stars |
| `user_id` | Who rated it |
| `created_at` | When |

---

### `model_health_events` — circuit breaker audit trail

Every time the per-(task, model) circuit breaker changes state, a row is inserted here.

| Column | Purpose |
|--------|---------|
| `task_id` | Which task |
| `model` | Which model |
| `provider` | Which provider (openai, groq, etc.) |
| `event` | "failure", "tripped", "recovered", "manual_reset" |
| `consecutive_failures` | How many failures were recorded when this event fired |
| `reason` | The error message (for failures) |
| `state` | The state after this event |
| `created_at` | When |

---

## Migrations: why idempotent?

The migration function runs **every time the server starts**. It uses `CREATE TABLE IF NOT EXISTS` everywhere:

```sql
CREATE TABLE IF NOT EXISTS runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id TEXT NOT NULL,
    -- ...
);
```

If the table already exists, `IF NOT EXISTS` makes it a no-op. If it doesn't exist (first launch), it creates it. Safe to run every time.

**For adding columns to existing tables:**
```go
_, err := db.Exec(`ALTER TABLE runs ADD COLUMN images TEXT DEFAULT ''`)
if err != nil {
    // "duplicate column name" error is acceptable — column already exists
    if !strings.Contains(err.Error(), "duplicate column name") {
        return err
    }
}
```

> **Why not just check if the column exists first?** Checking for column existence in SQLite requires a query (`PRAGMA table_info(runs)`), parsing the result, and then deciding. It's more code and more error-prone than "try to add it, ignore the 'already exists' error". The try-and-ignore approach is idiomatic for Go + SQLite migrations.

---

## Async writers: keeping predictions fast

### The problem

Writing a run record to SQLite takes 1–5 milliseconds. Under concurrent load, the single-writer lock means writes queue up. If 100 concurrent predictions all try to write at the same time, the 100th one waits 99–500ms just for the DB write — after already spending 500ms waiting for the LLM response.

### The solution: channels and goroutines

> **🔤 Go concept: channels**
> A **channel** in Go is a typed queue that safely passes values between goroutines. Writing to a channel (`ch <- value`) puts a value in; reading from it (`value := <-ch`) takes one out. Channels are safe to use from multiple goroutines simultaneously — no mutex needed.

The `RunWriter` works like this:

```
Handler goroutine:
    runWriter.Write(row)  →  [puts row in channel]

Background RunWriter goroutine:
    loop:
        accumulate rows from channel
        when batch is full OR time has passed:
            INSERT INTO runs VALUES (...), (...), (...)
```

The handler returns the prediction result to the user and drops the row into the channel. It doesn't wait for the INSERT. The RunWriter drains the channel in batches.

### What if the buffer fills up?

The channel has a finite capacity. If predictions arrive faster than the writer can drain them, the channel fills up. At that point, `runWriter.Write(row)` immediately drops the row (doesn't block) and increments a "dropped rows" counter.

**Is this okay?** Yes. Losing observability data is acceptable — you might miss some rows in the dashboard. Blocking a prediction because the DB is slow is not acceptable — it would increase user-visible latency. The tradeoff is explicit.

---

## Indexes: finding data fast

SQLite uses indexes to find rows without scanning the whole table. The platform creates indexes on the most common query patterns:

| Index on | Used by |
|----------|---------|
| `runs.run_id` | `GetRunByID` — fetch all models' results for one run |
| `runs.session_id` | `ListSessions` — list all runs in a session |
| `runs.user_id` + `created_at` | Dashboard queries for one user's history |
| `runs.task_id` + `created_at` | Budget tracking — today's spend for a task |
| `prompt_versions.task_id` | `ListVersions` — all versions of a task |
| `model_health_events.task_id` + `model` | Health history for a (task, model) pair |

Without these indexes, every query would scan every row — fine for 1,000 rows, very slow for 1,000,000.
