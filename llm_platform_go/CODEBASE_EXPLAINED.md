# llm_platform_go — Complete Codebase Explained

This document walks through every file in `llm_platform_go` line by line.
No Go experience assumed. Each section introduces the Go concepts you need just before you need them.

---

## Table of Contents

1. [How Go programs are structured](#1-how-go-programs-are-structured)
2. [cmd/server/main.go — the entry point](#2-cmdservermaingothe-entry-point)
3. [internal/config/config.go — reading environment variables](#3-internalconfigconfiggo--reading-environment-variables)
4. [internal/types/request.go — request types](#4-internaltypesrequestgo--request-types)
5. [internal/types/response.go — response types](#5-internaltypesresponsego--response-types)
6. [internal/db/db.go — opening SQLite](#6-internaldbdbgo--opening-sqlite)
7. [internal/db/queries.go — all SQL operations](#7-internaldbqueriesgo--all-sql-operations)
8. [internal/llm/client.go — the Provider interface and HTTP client](#8-internalllmclientgo--the-provider-interface-and-http-client)
9. [internal/llm/models.go — the ModelResult type](#9-internalllmmodelsgo--the-modelresult-type)
10. [internal/llm/pricing.go — cost calculation](#10-internalllmpricinggo--cost-calculation)
11. [internal/llm/runner.go — concurrent fan-out](#11-internalllmrunnergo--concurrent-fan-out)
12. [internal/api/middleware.go — HTTP helpers](#12-internalapimiddlewarego--http-helpers)
13. [internal/api/router.go — URL routing](#13-internalapirottergo--url-routing)
14. [internal/api/handlers.go — HTTP handlers](#14-internalapihandlersgo--http-handlers)
15. [pricing.json — token pricing data](#15-pricingjson--token-pricing-data)
16. [internal/llm/provider_test.go — provider unit tests](#16-internalllmprovider_testgo--provider-unit-tests)
17. [internal/llm/runner_test.go — runner unit tests](#17-internalllmrunner_testgo--runner-unit-tests)
18. [tests/handlers_test.go — integration tests](#18-testshandlers_testgo--integration-tests)

---

## 1. How Go programs are structured

Before reading any file, you need three mental models.

### Packages

Every `.go` file starts with `package somename`. A package is just a folder — all `.go` files in the same directory share the same package and can see each other's unexported names. The word `package` at the top is just Go saying "this file belongs to this group."

```
llm_platform_go/
  cmd/server/         → package main   (the runnable binary)
  internal/config/    → package config
  internal/db/        → package db
  internal/llm/       → package llm
  internal/api/       → package api
  internal/types/     → package types
```

`internal/` is a Go convention meaning "only code inside this module can import these packages." Nothing outside `llm_platform_go` can use them.

### Exported vs unexported

In Go there are no `public` / `private` keywords. Instead:

- Name starts with a **capital letter** → exported (visible outside the package). Example: `Config`, `LoadPricing`, `BuildClients`
- Name starts with a **lowercase letter** → unexported (only visible inside the package). Example: `registry`, `bifrostProvider`, `parseTime`

This applies to functions, types, struct fields, and variables.

### Error handling

Go functions that can fail return an `error` as their last return value. The caller always checks it:

```go
result, err := someFunction()
if err != nil {
    // something went wrong
}
```

There are no exceptions. If a function cannot fail, it returns only its result. This pattern repeats everywhere.

---

## 2. [cmd/server/main.go](cmd/server/main.go) — the entry point

```go
package main
```

`package main` is special. A file with this package declaration that also contains a `func main()` becomes a runnable binary. Without both of these, `go run` won't work.

```go
import (
    "log"
    "net/http"

    "llm_platform_go/internal/api"
    "llm_platform_go/internal/config"
    "llm_platform_go/internal/db"
    "llm_platform_go/internal/llm"

    "github.com/joho/godotenv"
)
```

`import` lists every package this file uses. Go splits imports into groups by convention:
- First group: standard library packages (`log`, `net/http`). These ship with Go itself.
- Second group: internal packages within this module (`llm_platform_go/internal/...`).
- Third group: third-party packages installed via `go get` (`github.com/joho/godotenv`).

Go will refuse to compile if you import something you don't use — no dead imports allowed.

```go
func main() {
```

`func` declares a function. `main` is the entry point — the first function Go runs when you execute the binary.

```go
    _ = godotenv.Load()
```

`godotenv.Load()` reads a `.env` file in the current directory and injects its contents as environment variables. It returns an `error`.

The `_` is Go's **blank identifier** — it means "I am deliberately throwing away this value." We discard the error here because if there's no `.env` file (e.g., in production where env vars are set by the OS), that's fine. We don't want the server to refuse to start just because `.env` is absent.

```go
    cfg, err := config.Load()
    if err != nil {
        log.Fatalf("config error: %v", err)
    }
```

`:=` is Go's short variable declaration. It creates new variables `cfg` and `err` and assigns to them in one line. On the right side, `config.Load()` returns two values: a `*Config` and an `error`. Go supports multiple return values natively.

`log.Fatalf` prints the error message and then calls `os.Exit(1)` — it kills the process immediately. The `%v` is a format verb meaning "print this value in its default format." This is the standard pattern for fatal startup errors: if config is missing, there's no point continuing.

```go
    if err := llm.LoadPricing(cfg.PricingPath); err != nil {
        log.Fatalf("pricing error: %v", err)
    }
```

Here `:=` is used inside the `if` condition itself. Go allows this: declare a variable, assign it, and test it in one line. `cfg.PricingPath` accesses the `PricingPath` field of the `cfg` struct — the same dot-notation you'd use in Python, Java, or TypeScript.

```go
    database, err := db.Open(cfg.DBPath)
    if err != nil {
        log.Fatalf("database error: %v", err)
    }
    defer database.Close()
```

`defer` is one of Go's most distinctive features. A deferred call runs **when the surrounding function returns**, regardless of how it returns (normal exit, panic, early return). Think of it as a "cleanup hook." Here, `database.Close()` will always run when `main()` exits, ensuring the SQLite file is properly flushed and closed.

```go
    if err := db.Migrate(database); err != nil {
        log.Fatalf("migration error: %v", err)
    }
```

`db.Migrate` creates the database schema (tables, indexes) if they don't already exist. It's safe to call every time the server starts.

```go
    clients := llm.BuildClients(cfg)
```

`BuildClients` constructs the single HTTP client that talks to the Bifrost LLM gateway. All models route through it.

```go
    for name, id := range llm.RegisteredModels() {
        log.Printf("registered model: %s → %s", name, id)
    }
```

`for ... range` iterates over a map. On each iteration, `name` gets the key and `id` gets the value. `log.Printf` prints to stdout with a timestamp. This is just a startup log so you can see which models are registered.

```go
    router := api.NewRouter(database, clients)

    addr := ":" + cfg.Port
    log.Printf("LLM Platform Go server listening on %s", addr)
    if err := http.ListenAndServe(addr, router); err != nil {
        log.Fatalf("server error: %v", err)
    }
}
```

`api.NewRouter` wires up all HTTP routes and returns an `http.Handler`. `http.ListenAndServe` starts the server and blocks forever. The `addr` is `":8000"` — the colon means "listen on all network interfaces on port 8000." If the server exits (which it shouldn't under normal operation), `log.Fatalf` reports why.

**Key takeaway:** `main.go` is the wiring diagram for the entire application. It creates dependencies in order and hands them to the components that need them.

---

## 3. [internal/config/config.go](internal/config/config.go) — reading environment variables

```go
package config
```

This file belongs to the `config` package. Any other package that does `import "llm_platform_go/internal/config"` can use the exported names from here.

```go
type Config struct {
    BifrostURL        string
    BifrostVirtualKey string
    DBPath            string
    Port              string
    PricingPath       string
}
```

`type Config struct { ... }` defines a new type called `Config`. A **struct** is a collection of named fields, similar to an object or a data class. All fields are `string` here. There are no methods yet — structs in Go are just data holders until you add methods to them.

```go
func Load() (*Config, error) {
```

This function returns two things: `*Config` (a pointer to a Config) and `error`. The `*` before `Config` means "a pointer to Config, not a copy of Config." This is important because it lets the caller receive `nil` as a signal for "no config was produced" (on error), and avoids copying the struct.

```go
    cfg := &Config{
        BifrostURL:        strings.TrimRight(getEnvOrDefault("BIFROST_URL", "http://llm-gateway.prd.meesho.int"), "/"),
        BifrostVirtualKey: os.Getenv("BIFROST_VIRTUAL_KEY"),
        DBPath:            getEnvOrDefault("DB_PATH", "./llm_platform.db"),
        Port:              getEnvOrDefault("PORT", "8000"),
        PricingPath:       getEnvOrDefault("PRICING_PATH", "./pricing.json"),
    }
```

`&Config{...}` creates a Config struct and immediately takes its address (the `&`). The result is a `*Config`. Each field is initialized inline using the `Field: value` syntax.

`strings.TrimRight(..., "/")` strips trailing slashes from the URL. This prevents URLs like `http://gateway//v1/chat` if someone accidentally adds a slash to the env var.

```go
    if cfg.BifrostVirtualKey == "" {
        return nil, fmt.Errorf("missing required environment variable: BIFROST_VIRTUAL_KEY")
    }
```

If the required env var is empty, return `nil` (no config) and a descriptive error. `fmt.Errorf` creates a new error value with a formatted message — it's like `fmt.Sprintf` but for errors.

```go
    return cfg, nil
```

On success, return the config and `nil` for the error. `nil` error = success in Go.

```go
func getEnvOrDefault(key, fallback string) string {
    if v := os.Getenv(key); v != "" {
        return v
    }
    return fallback
}
```

A helper function. `os.Getenv(key)` returns the env var's value, or an empty string if it isn't set. The `if v := ...; v != ""` pattern declares `v` in the if-scope (it's not accessible outside the `if`). If the env var exists and is non-empty, return it; otherwise return the `fallback`.

---

## 4. [internal/types/request.go](internal/types/request.go) — request types

```go
package types
```

The `types` package holds all shared data structures that cross the HTTP boundary. Both the API layer and the LLM layer import this package.

```go
type Message struct {
    Role    string      `json:"role"`
    Content interface{} `json:"content"`
}
```

The backtick string after each field is a **struct tag**. The `json:"role"` tag tells Go's `encoding/json` package: "when marshaling/unmarshaling JSON, use the key `role` for this field." Without the tag, Go would default to the field name (`Role`), which would produce JSON with capital letters — wrong for our API.

`Content interface{}` is Go's escape hatch for "any type." A regular API message has a `string` content, but multimodal messages (with image arrays) have a more complex structure. Using `interface{}` lets us accept both without writing two separate structs. In modern Go you'll also see `any` as an alias for `interface{}`.

```go
type RunRequest struct {
    Prompt             string               `json:"prompt"`
    Models             []string             `json:"models,omitempty"`
    ModelConversations map[string][]Message `json:"model_conversations,omitempty"`
    Temperature        *float64             `json:"temperature,omitempty"`
    MaxTokens          *int                 `json:"max_tokens,omitempty"`
    SessionID          string               `json:"session_id,omitempty"`
    SystemPrompt       string               `json:"system_prompt,omitempty"`
}
```

`[]string` is a **slice** (Go's dynamic array) of strings. `map[string][]Message` is a map where each key is a model name string and each value is a slice of `Message`. This stores per-model conversation history.

The `,omitempty` part of the struct tag means: when marshaling this struct to JSON, skip this field if it's the zero value (empty string, nil, empty slice, etc.). This keeps JSON responses clean.

`*float64` and `*int` — the `*` makes these **pointers**. A `float64` field can never be "absent" — if not provided, it would be `0.0`, which looks like a valid temperature. A `*float64` can be `nil`, which means "the caller didn't provide this field." This is Go's idiom for optional/nullable fields.

```go
type DeleteSessionsRequest struct {
    SessionIDs []string `json:"session_ids"`
}

type RatingRequest struct {
    RunID     string `json:"run_id"`
    Model     string `json:"model"`
    SessionID string `json:"session_id"`
    Rating    int    `json:"rating"`
    Note      string `json:"note"`
}
```

Two more request types, simpler than `RunRequest`. `int` for `Rating` since ratings are whole numbers 1–5.

---

## 5. [internal/types/response.go](internal/types/response.go) — response types

```go
type ModelResultResponse struct {
    Model           string  `json:"model"`
    Response        *string `json:"response"`
    LatencyMs       int     `json:"latency_ms"`
    InputTokens     int     `json:"input_tokens"`
    OutputTokens    int     `json:"output_tokens"`
    TotalTokens     int     `json:"total_tokens"`
    CostUSD         float64 `json:"cost_usd"`
    Success         bool    `json:"success"`
    Error           *string `json:"error"`
    ContextWindow   int     `json:"context_window"`
    MaxOutputTokens int     `json:"max_output_tokens"`
}
```

`Response *string` — a pointer to a string. On success, this points to the model's text. On failure, it's `nil`. In JSON, a nil pointer serializes as `null`.

`Error *string` — same idea. On success it's `nil` (serializes as `null`). On failure it points to the error message string.

`bool` is Go's boolean type. `true`/`false`.

```go
type RunResponse struct {
    RunID            string                `json:"run_id"`
    Prompt           string                `json:"prompt"`
    SystemPrompt     *string               `json:"system_prompt"`
    Results          []ModelResultResponse `json:"results"`
    TotalWallClockMs int                   `json:"total_wall_clock_ms"`
    ModelsSucceeded  int                   `json:"models_succeeded"`
    ModelsFailed     int                   `json:"models_failed"`
}
```

A slice (`[]ModelResultResponse`) containing all model results. This is the full response shape returned at the end of a `/run` call.

```go
type SessionSummary struct {
    SessionID   string    `json:"session_id"`
    FirstPrompt string    `json:"first_prompt"`
    TurnCount   int       `json:"turn_count"`
    CreatedAt   time.Time `json:"created_at"`
}
```

`time.Time` is Go's built-in time type from the `time` package. When serialized to JSON it becomes an RFC 3339 timestamp string like `"2025-01-15T10:30:00Z"`.

```go
type SessionListResponse struct {
    Page          int              `json:"page"`
    PageSize      int              `json:"page_size"`
    TotalSessions int              `json:"total_sessions"`
    TotalPages    int              `json:"total_pages"`
    Sessions      []SessionSummary `json:"sessions"`
}
```

Pagination envelope. `[]SessionSummary` is a slice of session summary structs.

```go
type TurnResult struct {
    Model        string  `json:"model"`
    Response     *string `json:"response"`
    LatencyMs    int     `json:"latency_ms"`
    InputTokens  int     `json:"input_tokens"`
    OutputTokens int     `json:"output_tokens"`
    TotalTokens  int     `json:"total_tokens"`
    CostUSD      float64 `json:"cost_usd"`
    Success      bool    `json:"success"`
    Error        *string `json:"error"`
    Rating       *int    `json:"rating"`
    Note         *string `json:"note"`
}
```

`Rating *int` — a pointer to int. A rating of 0 is valid (if someone submitted a 0? actually no — but more importantly: *no rating* (nil) is different from *rating of 0*). Using `*int` lets us distinguish "never rated" (`nil`) from any actual integer value.

```go
type SessionTurn struct {
    RunID        string       `json:"run_id"`
    Prompt       string       `json:"prompt"`
    SystemPrompt *string      `json:"system_prompt"`
    CreatedAt    time.Time    `json:"created_at"`
    Results      []TurnResult `json:"results"`
}

type SessionDetailResponse struct {
    SessionID string        `json:"session_id"`
    Turns     []SessionTurn `json:"turns"`
}
```

Nested structure: one session has many turns (`[]SessionTurn`), each turn has many results (`[]TurnResult`, one per model).

```go
type LeaderboardEntry struct {
    Model       string  `json:"model"`
    AvgScore    float64 `json:"avg_score"`
    RatingCount int     `json:"rating_count"`
}

type LeaderboardResponse struct {
    SessionID string             `json:"session_id"`
    Entries   []LeaderboardEntry `json:"entries"`
}
```

The leaderboard — per-model average ratings within a session.

```go
type RunRow struct {
    ID           int
    RunID        string
    SessionID    *string
    ...
    Rating       *int
    Note         *string
}
```

`RunRow` is the **internal** database representation — one row from the `runs` table. Notice it has **no JSON struct tags**. This type is never sent over the wire; it's only used inside the Go code to pass DB data between the `db` package and the `api` package. The absence of JSON tags is intentional.

`SessionID *string` — nullable because not every run belongs to a session.

---

## 6. [internal/db/db.go](internal/db/db.go) — opening SQLite

```go
package db

import (
    "database/sql"
    "fmt"

    _ "modernc.org/sqlite"
)
```

The blank import `_ "modernc.org/sqlite"` is a **side-effect import**. The SQLite driver registers itself with Go's `database/sql` package via an `init()` function that runs when the package is imported. We don't call anything from `modernc.org/sqlite` directly — we just need it to register. The `_` tells Go "I know I'm not using any exported names from this package; import it only for its side effects."

```go
func Open(path string) (*sql.DB, error) {
    db, err := sql.Open("sqlite", path)
    if err != nil {
        return nil, fmt.Errorf("open sqlite: %w", err)
    }
```

`sql.Open("sqlite", path)` opens a database connection pool using the driver named `"sqlite"` (registered by the blank import above) and the file at `path`. It doesn't actually connect yet — it's lazy.

`fmt.Errorf("...: %w", err)` creates a new error that **wraps** the original. The `%w` verb (unlike `%v`) marks the error as wrapped, allowing callers to use `errors.Is` or `errors.As` to inspect the original cause. This is Go's error-wrapping idiom.

```go
    db.SetMaxOpenConns(1)
```

SQLite is a file-based database with a single-writer rule. If two goroutines try to write simultaneously, one gets a "database is locked" error. Setting `MaxOpenConns(1)` means only one goroutine can hold a connection at a time — they queue up instead of racing. This is a hard constraint, not a performance choice.

```go
    if _, err = db.Exec("PRAGMA journal_mode=WAL"); err != nil {
        return nil, fmt.Errorf("set WAL mode: %w", err)
    }
    if _, err = db.Exec("PRAGMA busy_timeout=5000"); err != nil {
        return nil, fmt.Errorf("set busy_timeout: %w", err)
    }
```

`db.Exec` runs a SQL statement that doesn't return rows. It returns `(sql.Result, error)`. The `_` discards the `sql.Result` (which would tell us how many rows were affected — not relevant for PRAGMA).

`PRAGMA journal_mode=WAL` switches SQLite to Write-Ahead Logging mode. WAL allows concurrent reads while a write is in progress — better for a web server that serves read requests while occasionally writing.

`PRAGMA busy_timeout=5000` tells SQLite to wait up to 5000ms before returning a "database is locked" error instead of failing immediately. This provides a buffer for the rare case where two writes queue up.

```go
func Migrate(db *sql.DB) error {
    _, err := db.Exec(`
        CREATE TABLE IF NOT EXISTS runs (
            id            INTEGER PRIMARY KEY AUTOINCREMENT,
            run_id        TEXT NOT NULL,
            session_id    TEXT,
            prompt        TEXT NOT NULL,
            system_prompt TEXT,
            model         TEXT NOT NULL,
            response      TEXT,
            latency_ms    INTEGER NOT NULL DEFAULT 0,
            input_tokens  INTEGER NOT NULL DEFAULT 0,
            output_tokens INTEGER NOT NULL DEFAULT 0,
            total_tokens  INTEGER NOT NULL DEFAULT 0,
            cost_usd      REAL    NOT NULL DEFAULT 0.0,
            success       INTEGER NOT NULL DEFAULT 0,
            error         TEXT,
            created_at    DATETIME NOT NULL DEFAULT (datetime('now'))
        );
        CREATE INDEX IF NOT EXISTS idx_runs_run_id     ON runs(run_id);
        CREATE INDEX IF NOT EXISTS idx_runs_session_id ON runs(session_id);
    `)
```

A raw string literal in Go uses backticks (`` ` ``) and can span multiple lines without escaping. `CREATE TABLE IF NOT EXISTS` means: create this table only if it doesn't already exist — safe to re-run on every startup.

`INTEGER PRIMARY KEY AUTOINCREMENT` — SQLite auto-increments this column. `NOT NULL DEFAULT 0` means the column can't be null and defaults to 0 if not provided. Columns without `NOT NULL` (like `session_id`, `response`, `error`, `system_prompt`) are nullable — they can be `NULL` in the database.

```go
    db.Exec("ALTER TABLE runs ADD COLUMN rating INTEGER")
    db.Exec("ALTER TABLE runs ADD COLUMN note TEXT")
    db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_runs_run_id_model ON runs(run_id, model)")

    return nil
}
```

These are **additive migrations** — columns added after the initial table creation. SQLite doesn't support `ADD COLUMN IF NOT EXISTS`, so if you run `ALTER TABLE ADD COLUMN rating` twice, the second call returns an error "duplicate column name." We intentionally ignore these errors (by not checking `err` on these `db.Exec` calls). The first time the server starts with new code, the column gets added. Every subsequent startup the error is silently swallowed. This is an intentional simplification — a production app would use a proper migration tool.

---

## 7. [internal/db/queries.go](internal/db/queries.go) — all SQL operations

```go
func InsertRun(db *sql.DB, r *types.RunRow) error {
    _, err := db.Exec(`
        INSERT INTO runs
            (run_id, session_id, prompt, system_prompt, model, response,
             latency_ms, input_tokens, output_tokens, total_tokens,
             cost_usd, success, error, created_at)
        VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
        r.RunID, r.SessionID, r.Prompt, r.SystemPrompt, r.Model, r.Response,
        r.LatencyMs, r.InputTokens, r.OutputTokens, r.TotalTokens,
        r.CostUSD, boolToInt(r.Success), r.Error,
        r.CreatedAt.UTC().Format("2006-01-02 15:04:05"),
    )
    return err
}
```

`db.Exec` with `?` placeholders is Go's way of doing parameterized queries (prevents SQL injection). Each `?` corresponds to one argument after the SQL string — Go matches them positionally.

`r.CreatedAt.UTC().Format("2006-01-02 15:04:05")` — Go's time formatting is unusual. Instead of `%Y-%m-%d`, Go uses a **reference time**: `Mon Jan 2 15:04:05 MST 2006`. The specific date and time values in that reference are fixed (they're the time Go was created, in the timezone GMT-7). You format time by showing what the reference time would look like in your desired format. `"2006-01-02 15:04:05"` produces `"2025-06-13 10:30:00"`.

```go
func ListSessions(db *sql.DB, page, pageSize int) ([]types.SessionSummary, int, error) {
```

Go functions can return multiple values. This one returns three: a slice of session summaries, a total count integer, and an error.

```go
    total, err := countSessions(db)
    if err != nil {
        return nil, 0, err
    }
```

When returning early on error, return zero values for all other return values: `nil` for the slice, `0` for the int, and the real error.

```go
    rows, err := db.Query(`
        SELECT
            session_id,
            MIN(prompt)      AS first_prompt,
            COUNT(DISTINCT run_id) AS turn_count,
            MIN(created_at)  AS created_at
        FROM runs
        WHERE session_id IS NOT NULL
        GROUP BY session_id
        ORDER BY MAX(created_at) DESC
        LIMIT ? OFFSET ?`, pageSize, offset)
    if err != nil {
        return nil, 0, err
    }
    defer rows.Close()
```

`db.Query` executes a SELECT and returns a `*sql.Rows` cursor — it doesn't load all results into memory at once. `defer rows.Close()` is essential: if you forget to close rows, you leak the database connection. Deferring it guarantees cleanup even if the loop below panics or returns early.

```go
    var sessions []types.SessionSummary
    for rows.Next() {
        var s types.SessionSummary
        var firstPrompt, createdAtStr string
        if err := rows.Scan(&s.SessionID, &firstPrompt, &s.TurnCount, &createdAtStr); err != nil {
            return nil, 0, err
        }
```

`rows.Next()` advances the cursor to the next row. It returns `false` when there are no more rows.

`rows.Scan(...)` copies the current row's columns into Go variables. The `&` operator takes the **address** of a variable (creates a pointer to it). `Scan` needs pointers so it can write into the variables. The order must match the SELECT column order exactly.

`var s types.SessionSummary` declares a new zero-valued `SessionSummary` on each iteration.

```go
        if len(firstPrompt) > 80 {
            firstPrompt = firstPrompt[:80] + "..."
        }
        s.FirstPrompt = firstPrompt
        s.CreatedAt = parseTime(createdAtStr)
        sessions = append(sessions, s)
    }
    if sessions == nil {
        sessions = []types.SessionSummary{}
    }
    return sessions, total, rows.Err()
```

`append(sessions, s)` adds `s` to the slice. Go slices grow automatically.

`if sessions == nil` — if no rows matched, `sessions` is still `nil` (its zero value). A nil slice marshals to JSON as `null`, but we want `[]` (empty array). Converting to an empty slice fixes this.

`rows.Err()` — after the loop, check if iteration stopped due to an error rather than normal end-of-results.

```go
func UpsertRating(db *sql.DB, runID, model, sessionID string, rating int, note string) error {
    res, err := db.Exec(
        "UPDATE runs SET rating = ?, note = ? WHERE run_id = ? AND model = ? AND session_id = ?",
        rating, note, runID, model, sessionID,
    )
    if err != nil {
        return err
    }
    n, _ := res.RowsAffected()
    if n == 0 {
        return fmt.Errorf("no run found for run_id=%s model=%s session_id=%s", runID, model, sessionID)
    }
    return nil
}
```

`res.RowsAffected()` returns how many rows the UPDATE touched. If `n == 0`, the WHERE clause matched nothing — we treat that as an error.

```go
func DeleteSessions(db *sql.DB, sessionIDs []string) (int64, error) {
    if len(sessionIDs) == 0 {
        return 0, nil
    }
    placeholders := strings.TrimRight(strings.Repeat("?,", len(sessionIDs)), ",")
    args := make([]interface{}, len(sessionIDs))
    for i, id := range sessionIDs {
        args[i] = id
    }
    res, err := db.Exec(
        fmt.Sprintf("DELETE FROM runs WHERE session_id IN (%s)", placeholders),
        args...,
    )
```

`IN (?,?,?)` requires a dynamic number of placeholders matching the number of IDs. `strings.Repeat("?,", n)` produces `"?,?,?,"` and `strings.TrimRight(..., ",")` removes the trailing comma.

`make([]interface{}, n)` creates a slice of `n` empty interface values. We need `[]interface{}` because `db.Exec` accepts `...interface{}` (a variadic of any type).

`args...` is Go's **spread** operator — it unpacks the slice into individual arguments, equivalent to Python's `*args`.

```go
func boolToInt(b bool) int {
    if b {
        return 1
    }
    return 0
}
```

SQLite has no native boolean type. We store booleans as `0` (false) and `1` (true). This helper converts.

```go
func parseTime(s string) time.Time {
    t, err := time.Parse("2006-01-02 15:04:05", s)
    if err != nil {
        t, err = time.Parse("2006-01-02 15:04:05.999999999", s)
        if err != nil {
            return time.Time{}
        }
    }
    return time.Date(t.Year(), t.Month(), t.Day(),
        t.Hour(), t.Minute(), t.Second(),
        t.Nanosecond(), time.UTC)
}
```

SQLite returns DATETIME columns as plain strings. `time.Parse(layout, s)` parses a string into a `time.Time`. The layout uses the same reference-time format as `Format`. `time.Time{}` is a zero-value time (year 1, January 1) — returned as a fallback if parsing fails.

The final `time.Date(...)` call rebuilds the time in UTC regardless of what timezone SQLite stored it in.

```go
func TotalPages(total, pageSize int) int {
    if pageSize <= 0 {
        return 0
    }
    return int(math.Ceil(float64(total) / float64(pageSize)))
}
```

Integer division truncates: `7 / 3 = 2`, but we want `3` pages. `math.Ceil` rounds up, but it operates on `float64`. We must convert both ints to `float64` first (Go never implicitly converts numeric types), then convert the result back to `int`.

---

## 8. [internal/llm/client.go](internal/llm/client.go) — the Provider interface and HTTP client

```go
type Provider interface {
    Call(ctx context.Context, req *chatRequest) (*chatResponse, error)
}
```

An **interface** in Go defines a set of methods. Any type that has a `Call` method with exactly this signature **automatically** implements `Provider` — no `implements` keyword needed. This is Go's "duck typing" approach to interfaces.

Why have an interface here? Because in tests, we can create a fake `Provider` (a mock) that returns whatever we want, without making real HTTP calls. The `runner.go` code only knows about `Provider` — it doesn't care if it's a real HTTP client or a test double.

```go
type chatRequest struct {
    Model       string        `json:"model"`
    Messages    []chatMessage `json:"messages"`
    MaxTokens   int           `json:"max_tokens,omitempty"`
    Temperature float32       `json:"temperature,omitempty"`
}

type chatMessage struct {
    Role    string      `json:"role"`
    Content interface{} `json:"content"`
}

type chatResponse struct {
    Choices []chatChoice `json:"choices"`
    Usage   chatUsage    `json:"usage"`
}

type chatChoice struct {
    Message chatMessage `json:"message"`
}

type chatUsage struct {
    PromptTokens     int `json:"prompt_tokens"`
    CompletionTokens int `json:"completion_tokens"`
}
```

These are all lowercase (unexported) — they're implementation details of this package, not meant to be used by callers. They model the OpenAI-compatible JSON format that the Bifrost gateway uses.

```go
type APIError struct {
    HTTPStatusCode int
    Message        string
}

func (e *APIError) Error() string {
    return fmt.Sprintf("API error %d: %s", e.HTTPStatusCode, e.Message)
}
```

`APIError` is a custom error type. In Go, any type that has a method `Error() string` satisfies the built-in `error` interface. By making `APIError` satisfy `error`, we can return it as an `error` value. But callers can also use `errors.As(err, &apiErr)` to extract the typed `*APIError` and access `HTTPStatusCode` or `Message` directly — something you can't do with a plain string error.

`func (e *APIError) Error() string` — the `(e *APIError)` part is called a **receiver**. It binds this function as a method on the `*APIError` type. `e` is like `self` in Python.

```go
type errorBody struct {
    Error struct {
        Message string `json:"message"`
    } `json:"error"`
}
```

An **anonymous struct** nested inside another struct. This mirrors the JSON shape `{"error": {"message": "..."}}` returned by OpenAI-compatible APIs when something goes wrong.

```go
type bifrostProvider struct {
    baseURL    string
    virtualKey string
    client     *http.Client
}
```

`bifrostProvider` (lowercase = unexported) is the concrete implementation of the `Provider` interface. It has three fields: the gateway URL, the virtual key for authentication, and an HTTP client.

```go
func (p *bifrostProvider) Call(ctx context.Context, req *chatRequest) (*chatResponse, error) {
    body, err := json.Marshal(req)
    if err != nil {
        return nil, fmt.Errorf("marshal request: %w", err)
    }
```

`json.Marshal(req)` converts the `chatRequest` struct into a JSON byte slice. `[]byte` is Go's type for a slice of raw bytes.

```go
    httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/chat/completions", bytes.NewReader(body))
```

`http.NewRequestWithContext` creates an HTTP request **bound to a context**. The context carries a deadline or cancellation signal — if the context is cancelled (e.g., the 10-second timeout fires), the HTTP call is automatically aborted.

`bytes.NewReader(body)` wraps the `[]byte` in an `io.Reader` interface, which is what the HTTP library expects for the request body.

```go
    httpReq.Header.Set("Content-Type", "application/json")
    httpReq.Header.Set("x-bf-vk", p.virtualKey)
```

Setting request headers. Critically, this uses `x-bf-vk` (Bifrost's virtual key header) instead of `Authorization: Bearer`. The Bifrost gateway is NOT the direct OpenAI API — it has its own auth mechanism.

```go
    resp, err := p.client.Do(httpReq)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()
```

`p.client.Do(httpReq)` executes the HTTP request. The response body is a stream (an `io.ReadCloser`). `defer resp.Body.Close()` ensures it's always closed after we're done reading, preventing connection leaks.

```go
    respBody, err := io.ReadAll(resp.Body)
```

`io.ReadAll` reads the entire response body into a `[]byte`. We read it all at once so we can either parse it as JSON (success) or parse it as an error body (failure).

```go
    if resp.StatusCode != http.StatusOK {
        var eb errorBody
        _ = json.Unmarshal(respBody, &eb)
        msg := eb.Error.Message
        if msg == "" {
            raw := string(respBody)
            if len(raw) > 200 {
                raw = raw[:200] + "..."
            }
            msg = fmt.Sprintf("%s — raw: %s", http.StatusText(resp.StatusCode), raw)
        }
        return nil, &APIError{HTTPStatusCode: resp.StatusCode, Message: msg}
    }
```

Non-200 = error. First attempt to parse the body as `{"error":{"message":"..."}}`. If that fails (the body isn't that JSON shape), fall back to including the raw body text in the error message (truncated to 200 chars).

`http.StatusText(resp.StatusCode)` converts a status code like `429` to its human name `"Too Many Requests"`.

`&APIError{...}` creates an `APIError` and takes its address — returning a `*APIError`, which satisfies the `error` interface via the `Error()` method we defined.

```go
    var chatResp chatResponse
    if err := json.Unmarshal(respBody, &chatResp); err != nil {
        return nil, fmt.Errorf("decode response: %w", err)
    }
    return &chatResp, nil
}
```

`json.Unmarshal(data, &chatResp)` parses JSON bytes into the struct. The `&` is needed because `Unmarshal` needs a pointer so it can write into the struct.

```go
var sharedHTTPClient = &http.Client{
    Transport: &http.Transport{
        DialContext: (&net.Dialer{
            Timeout: 5 * time.Second,
        }).DialContext,
        TLSHandshakeTimeout: 5 * time.Second,
    },
}
```

A **package-level variable** — created once when the package is loaded, shared across all calls. Using a shared client (rather than `http.Get()`) means Go reuses TCP connections via keep-alive instead of creating a new connection for every request.

`5 * time.Second` — Go's `time.Duration` type makes time arithmetic readable. `time.Second` is a constant, and multiplying by `5` gives 5 seconds.

```go
type Clients struct {
    Gateway Provider
}

func BuildClients(cfg *config.Config) *Clients {
    return &Clients{
        Gateway: &bifrostProvider{
            baseURL:    cfg.BifrostURL,
            virtualKey: cfg.BifrostVirtualKey,
            client:     sharedHTTPClient,
        },
    }
}
```

`Clients` holds the single `Provider`. All models route through `Gateway` — there's only one door into the LLM infrastructure. `BuildClients` creates the concrete `bifrostProvider` and returns it wrapped in `Clients`, but the field type is the interface `Provider`. This is the dependency injection pattern: `runner.go` accepts `*Clients` and uses `clients.Gateway.Call(...)` without knowing or caring that it's talking to `bifrostProvider`.

---

## 9. [internal/llm/models.go](internal/llm/models.go) — the ModelResult type

```go
var DefaultModels = []string{"gpt-4o-mini", "gemini-flash", "claude-sonnet"}
```

Package-level exported variable. Any package can read `llm.DefaultModels`. This is the set of models used when a request doesn't specify which models to query.

```go
type ModelResult struct {
    Model           string
    Response        *string
    LatencyMs       int
    InputTokens     int
    OutputTokens    int
    TotalTokens     int
    CostUSD         float64
    Success         bool
    Error           *string
    ContextWindow   int
    MaxOutputTokens int
}
```

`ModelResult` is the **internal** result type — it travels between `runner.go` and `handlers.go` but is never serialized to JSON directly. Notice there are no JSON struct tags.

`Response *string` and `Error *string` — exactly one of these will be non-nil for any given result. If the call succeeded, `Response` points to the model's answer and `Error` is nil. If it failed, `Error` points to the error message and `Response` is nil.

---

## 10. [internal/llm/pricing.go](internal/llm/pricing.go) — cost calculation

```go
type Rate struct {
    InputPer1M      float64 `json:"input_per_1m"`
    OutputPer1M     float64 `json:"output_per_1m"`
    ContextWindow   int     `json:"context_window"`
    MaxOutputTokens int     `json:"max_output_tokens"`
}
```

`Rate` is exported (capital R) because the test file needs to create `Rate` values directly when injecting test pricing data.

```go
var pricingTable map[string]Rate
```

A package-level variable with type `map[string]Rate` — a map from model name strings to `Rate` structs. It's `nil` until `LoadPricing` runs. Only this package can write to it directly.

```go
func LoadPricing(path string) error {
    data, err := os.ReadFile(path)
    if err != nil {
        return fmt.Errorf("read pricing file %q: %w", path, err)
    }
    if err := json.Unmarshal(data, &pricingTable); err != nil {
        return fmt.Errorf("parse pricing file: %w", err)
    }
    return nil
}
```

`os.ReadFile` reads the entire file into a `[]byte`. `json.Unmarshal` then parses it into `pricingTable`. Since `pricingTable` is a `map[string]Rate`, Go's JSON parser expects a JSON object where each key maps to a Rate-shaped value — matching exactly the structure of `pricing.json`.

`%q` in the format string adds quotes around the path: `read pricing file "/path/to/pricing.json"`.

```go
func LoadPricingFromMap(m map[string]Rate) error {
    pricingTable = m
    return nil
}
```

Test-only injection — replaces the global `pricingTable` directly without reading from disk. Tests call this instead of `LoadPricing`. The function signature returns `error` even though it always succeeds, for API consistency.

```go
func GetMaxOutputTokens(model string) int {
    r, ok := pricingTable[model]
    if !ok {
        return 0
    }
    return r.MaxOutputTokens
}
```

Map lookup in Go returns two values: the value and a boolean `ok` indicating whether the key existed. If `ok` is `false`, the key wasn't in the map. This is the standard Go map-lookup idiom.

```go
func CalculateCost(model string, inputTokens, outputTokens int) float64 {
    r, ok := pricingTable[model]
    if !ok {
        return 0.0
    }
    cost := (float64(inputTokens)/1_000_000)*r.InputPer1M +
        (float64(outputTokens)/1_000_000)*r.OutputPer1M
    return math.Round(cost*1_000_000) / 1_000_000
}
```

`float64(inputTokens)` is an explicit **type conversion** (not a cast — Go never silently converts between types). `1_000_000` uses underscores as digit separators for readability (Go 1.13+).

`math.Round(cost*1_000_000) / 1_000_000` — multiply by 1M, round to nearest integer, divide back. This gives 6 decimal places of precision while avoiding floating-point artifacts like `0.00014999999999`.

```go
func GetAllRates() map[string]Rate {
    out := make(map[string]Rate, len(pricingTable))
    for k, v := range pricingTable {
        out[k] = v
    }
    return out
}
```

Returns a **copy** of the full pricing table as a `map[string]Rate`. We copy rather than return the private `pricingTable` variable directly for two reasons: (1) the caller can't mutate the internal table, and (2) it's safe to read concurrently since we hand them a snapshot. This is used by the `GET /pricing` handler to expose pricing data to the frontend, so the frontend doesn't need to hardcode rates that are already defined here.

---

## 11. [internal/llm/runner.go](internal/llm/runner.go) — concurrent fan-out

```go
var registry = map[string]string{
    "gpt-4o-mini":   "openai/gpt-4o-mini",
    "gemini-flash":  "vertex/gemini-2.5-flash",
    "claude-sonnet": "anthropic/claude-3-5-sonnet-20241022",
}
```

Unexported package-level map. The keys are "friendly names" used throughout the codebase and in the frontend. The values are "Bifrost model IDs" — the format the gateway expects: `provider/model-id`. The Bifrost gateway uses this prefix to know which provider (OpenAI, Google Vertex, Anthropic) to route the request to.

```go
func RegisteredModels() map[string]string {
    out := make(map[string]string, len(registry))
    for k, v := range registry {
        out[k] = v
    }
    return out
}
```

`make(map[string]string, len(registry))` creates a new map with an initial capacity hint. We copy the registry instead of returning it directly to prevent callers from modifying the internal registry map.

```go
func StreamAll(ctx context.Context, clients *Clients, req *types.RunRequest) (<-chan ModelResult, int) {
    models := req.Models
    if len(models) == 0 {
        models = DefaultModels
    }
```

`<-chan ModelResult` is the return type: a **receive-only channel** of `ModelResult` values. The arrow `<-` on the left of `chan` means the caller can only receive from this channel, not send to it. This is a safety constraint enforced at compile time.

```go
    ch := make(chan ModelResult, len(models))
    for _, name := range models {
        go func(modelName string) {
            ch <- callSingleModel(ctx, clients, modelName, req)
        }(name)
    }
    return ch, len(models)
}
```

`make(chan ModelResult, len(models))` creates a **buffered channel** — it can hold up to `len(models)` items without blocking. Without buffering, each `ch <- result` would block until someone reads from the channel, causing a deadlock since all goroutines would be blocked waiting to send.

`go func(modelName string) { ... }(name)` launches a **goroutine** — a lightweight thread. The goroutine runs the anonymous function concurrently. All model calls happen in parallel.

The `(name)` at the end is crucial. Without it, the anonymous function would close over the loop variable `name` by reference. By the time the goroutine runs, the loop may have advanced and `name` could have the last iteration's value. Passing `name` as an argument copies its value at this moment, giving each goroutine its own snapshot.

`ch <- value` sends a value into the channel. The goroutines send their results as they finish. Because the channel is buffered, they can all send without waiting for the receiver.

The caller (in `handlers.go`) reads `count` items from the channel. The results arrive in completion order — the fastest model's result arrives first.

```go
func callSingleModel(ctx context.Context, clients *Clients, modelName string, req *types.RunRequest) ModelResult {
    start := time.Now()
```

`time.Now()` records when this call started. We subtract it from `time.Since(start)` at the end to measure latency.

```go
    bifrostModelID, ok := registry[modelName]
    if !ok {
        return errResult(modelName, start, fmt.Sprintf("unknown model: %s", modelName))
    }

    if clients.Gateway == nil {
        return errResult(modelName, start, "LLM client not configured")
    }
```

Validate inputs before attempting the call. `errResult` (defined below) builds a failed `ModelResult`.

```go
    temp := float32(0.7)
    if req.Temperature != nil {
        temp = float32(*req.Temperature)
    }
```

`req.Temperature` is a `*float64`. Check if it's nil (not provided) before dereferencing. `*req.Temperature` dereferences the pointer to get the actual float value. Then convert it to `float32` (the API expects float32).

```go
    maxTok := 1000
    if req.MaxTokens != nil {
        maxTok = *req.MaxTokens
    }
    if modelMax := GetMaxOutputTokens(modelName); modelMax > 0 && maxTok > modelMax {
        maxTok = modelMax
    }
```

Similar nil check for `MaxTokens`. Then cap `maxTok` at the model's maximum — you can't ask a model for more tokens than it supports.

```go
    apiReq := chatRequest{
        Model:       bifrostModelID,
        Messages:    buildMessages(modelName, req),
        MaxTokens:   maxTok,
        Temperature: temp,
    }
```

Constructs the `chatRequest` struct (the actual API payload). Notice `bifrostModelID` is used here, not `modelName` — the gateway needs the prefixed format like `"openai/gpt-4o-mini"`.

```go
    const attemptTimeout = 10 * time.Second
    attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
    resp, err := clients.Gateway.Call(attemptCtx, &apiReq)
    cancel()
```

`context.WithTimeout` creates a child context with a 10-second deadline. When 10 seconds pass, `attemptCtx` is automatically cancelled, aborting the HTTP call.

`cancel()` is called immediately after the call returns — even on success. This releases the resources associated with the timeout context. If you forget to call `cancel()`, Go leaks a timer. In real code you'd usually `defer cancel()`, but here we call it inline because we're done with `attemptCtx` after the call.

```go
    latencyMs := int(time.Since(start).Milliseconds())
```

`time.Since(start)` returns a `time.Duration`. `.Milliseconds()` extracts it as an `int64`. `int(...)` converts to plain `int` for storage.

```go
    if err != nil {
        return errResult(modelName, start, err.Error())
    }
    if len(resp.Choices) == 0 {
        return errResult(modelName, start, "empty response from model")
    }

    text, ok := resp.Choices[0].Message.Content.(string)
    if !ok || text == "" {
        return errResult(modelName, start, "model returned empty content")
    }
```

`resp.Choices[0].Message.Content` has type `interface{}` because it was defined that way. To use it as a string, we need a **type assertion**: `value.(string)`. It returns two values: the string and a boolean. If the underlying value is actually a string, `ok` is `true`. If it's something else (e.g., an array for multimodal content), `ok` is `false`.

```go
    return ModelResult{
        Model:           modelName,
        Response:        &text,
        LatencyMs:       latencyMs,
        ...
        Success:         true,
        ...
    }
}
```

`&text` takes the address of the local `text` variable — creating a `*string`. This is fine because Go allocates the variable on the heap when you take its address (the variable outlives the function call).

```go
func buildMessages(modelName string, req *types.RunRequest) []chatMessage {
    var msgs []chatMessage

    if req.SystemPrompt != "" {
        msgs = append(msgs, chatMessage{Role: "system", Content: req.SystemPrompt})
    }

    if hist, ok := req.ModelConversations[modelName]; ok && len(hist) > 0 {
        for _, m := range hist {
            msgs = append(msgs, chatMessage{Role: m.Role, Content: m.Content})
        }
    } else {
        msgs = append(msgs, chatMessage{Role: "user", Content: req.Prompt})
    }

    return msgs
}
```

`var msgs []chatMessage` declares a nil slice. You can `append` to a nil slice.

`req.ModelConversations[modelName]` looks up the history for this specific model. If `ModelConversations` is nil or doesn't have this model as a key, `ok` is `false` and we fall back to the single `req.Prompt`. This is multi-turn: each model gets its own conversation history.

```go
func errResult(model string, start time.Time, msg string) ModelResult {
    latencyMs := int(time.Since(start).Milliseconds())
    return ModelResult{
        Model:     model,
        LatencyMs: latencyMs,
        Success:   false,
        Error:     &msg,
    }
}
```

Helper to build a failed `ModelResult` consistently. `&msg` takes the address of the `msg` parameter — safe because the parameter is a copy.

---

## 12. [internal/api/middleware.go](internal/api/middleware.go) — HTTP helpers

```go
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(status)
    json.NewEncoder(w).Encode(v)
}
```

`http.ResponseWriter` is an interface representing the HTTP response. You must set headers before calling `WriteHeader`, and call `WriteHeader` before writing the body — Go's HTTP library enforces this order.

`json.NewEncoder(w).Encode(v)` streams JSON directly into the response writer. More efficient than `json.Marshal` (which first writes to a buffer) for large responses.

```go
func writeError(w http.ResponseWriter, status int, msg string) {
    writeJSON(w, status, map[string]string{"detail": msg})
}
```

`map[string]string{"detail": msg}` creates a map literal inline. This produces JSON like `{"detail": "prompt must not be empty"}`.

```go
func writeSSE(w http.ResponseWriter, event string, data interface{}) {
    b, _ := json.Marshal(data)
    fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
}
```

**Server-Sent Events (SSE)** format. Each event looks like:
```
event: result
data: {"model":"gpt-4o-mini","response":"..."}

```
The blank line (`\n\n`) at the end signals the end of one event. The browser/client reads these as a stream. `fmt.Fprintf(w, ...)` writes formatted text to the response writer. `json.Marshal` (not `Unmarshal`) converts a Go value to JSON bytes — `b` has type `[]byte`.

---

## 13. [internal/api/router.go](internal/api/router.go) — URL routing

```go
func NewRouter(db *sql.DB, clients *llm.Clients) http.Handler {
    h := &Handler{DB: db, Clients: clients}
```

The router receives the database and clients by pointer and stores them in a `Handler` struct. This is **dependency injection** — the router doesn't create these resources itself; they're handed in from `main.go`.

```go
    r := chi.NewRouter()
    r.Use(middleware.RequestID)
    r.Use(middleware.Logger)
    r.Use(middleware.Recoverer)
```

`chi` is a lightweight HTTP router. `r.Use(...)` registers **middleware** — functions that run for every request before the actual handler. They form a pipeline.

- `RequestID`: adds a unique ID to each request (useful for log correlation)
- `Logger`: logs every request's method, path, status, and duration
- `Recoverer`: catches panics in handlers and returns a 500 instead of crashing the server

```go
    r.Use(cors.Handler(cors.Options{
        AllowedOrigins: []string{"*"},
        AllowedMethods: []string{"GET", "POST", "DELETE", "OPTIONS"},
        AllowedHeaders: []string{"Accept", "Content-Type"},
    }))
```

CORS middleware that allows the frontend (running on `localhost:5173`) to make requests to the backend (running on `localhost:8000`). `"*"` means any origin is allowed.

```go
    r.Get("/health", h.HealthCheck)
    r.Get("/models", h.ModelsCheck)
    r.Get("/pricing", h.GetPricing)
    r.Post("/run", h.RunEndpoint)
    r.Post("/ratings", h.SaveRating)
    r.Get("/sessions", h.ListSessions)
    r.Get("/sessions/{session_id}", h.GetSession)
    r.Get("/sessions/{session_id}/leaderboard", h.GetLeaderboard)
    r.Delete("/sessions", h.DeleteSessions)
```

Route registration. `{session_id}` is a URL parameter — chi extracts whatever is in that position and makes it available via `chi.URLParam(r, "session_id")`.

`GET /pricing` is a lightweight endpoint — it just serializes `pricingTable` to JSON with no I/O. The frontend fetches it on mount so it can display live token pricing and context window sizes without hardcoding those values in TypeScript.

`h.HealthCheck` is a **method value** — it's the `HealthCheck` method bound to the specific `h` receiver. In Go, this can be passed as a function value since it matches the `http.HandlerFunc` signature `func(http.ResponseWriter, *http.Request)`.

```go
    return r
}
```

`r` has the type `*chi.Mux`, which implements `http.Handler`. By returning `http.Handler` instead of `*chi.Mux`, callers only see the interface — they can't depend on chi-specific behavior.

---

## 14. [internal/api/handlers.go](internal/api/handlers.go) — HTTP handlers

```go
type Handler struct {
    DB      *sql.DB
    Clients *llm.Clients
}
```

All handlers are methods on this `Handler` struct. It holds the shared dependencies. This is Go's way of grouping related handlers while giving them access to shared state.

### RunEndpoint — the main handler

```go
func (h *Handler) RunEndpoint(w http.ResponseWriter, r *http.Request) {
```

All HTTP handlers have this exact signature: `func(http.ResponseWriter, *http.Request)`. The `(h *Handler)` receiver binds it to the `Handler` type.

```go
    var req types.RunRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        writeError(w, http.StatusUnprocessableEntity, "invalid request body: "+err.Error())
        return
    }
    if req.Prompt == "" {
        writeError(w, http.StatusUnprocessableEntity, "prompt must not be empty")
        return
    }
```

`json.NewDecoder(r.Body).Decode(&req)` parses the JSON request body into the `req` struct. `r.Body` is an `io.Reader` stream. `422 Unprocessable Entity` is the correct HTTP status for "I understood the request but the data is wrong."

After `writeError`, we `return` immediately — Go doesn't stop executing a handler after writing a response.

```go
    flusher, ok := w.(http.Flusher)
    if !ok {
        writeError(w, http.StatusInternalServerError, "streaming not supported")
        return
    }
```

SSE requires the ability to flush partial writes immediately. `w.(http.Flusher)` is a **type assertion** — it checks whether `w` also implements the `http.Flusher` interface. Not all `http.ResponseWriter` implementations support flushing (e.g., test recorders). If it doesn't, we can't stream.

```go
    w.Header().Set("Content-Type", "text/event-stream")
    w.Header().Set("Cache-Control", "no-cache")
    w.Header().Set("Connection", "keep-alive")
    w.Header().Set("X-Accel-Buffering", "no")
```

SSE-specific headers:
- `text/event-stream` — tells the client this is an SSE stream
- `no-cache` — don't cache the stream
- `keep-alive` — keep the HTTP connection open
- `X-Accel-Buffering: no` — tells nginx (if used as a reverse proxy) not to buffer the response

```go
    ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
    defer cancel()
```

A 90-second outer deadline for the entire request. `r.Context()` is the request's existing context (it gets cancelled if the client disconnects). We wrap it with our own timeout. `defer cancel()` — clean up the timer when the handler returns.

```go
    runID := uuid.New().String()
    wallStart := time.Now()
    now := time.Now().UTC()
```

Generate a unique ID for this run. `wallStart` tracks total wall-clock time for the "done" event. `now` is the timestamp we'll stamp all DB rows with (same time for all models in this run).

```go
    writeSSE(w, "meta", map[string]string{"run_id": runID})
    flusher.Flush()
```

Send the first SSE event immediately — the client knows the `run_id` before any model responds. `flusher.Flush()` pushes this event to the client right now, without waiting for the response to finish.

```go
    ch, count := llm.StreamAll(ctx, h.Clients, &req)
```

Kicks off all model calls concurrently. Returns a channel and the count of models. We'll read exactly `count` results from the channel.

```go
    var succeeded, failed int
    for range count {
        mr := <-ch
```

`for range count` is equivalent to `for i := 0; i < count; i++` but we don't need the index. `mr := <-ch` **receives** one result from the channel, blocking until one is available. Results arrive as models complete — fastest first.

```go
        _ = db.InsertRun(h.DB, &types.RunRow{
            RunID:        runID,
            ...
        })
```

Insert every result into the database. We ignore the error (the `_`) because a DB write failure shouldn't stop the SSE stream — the client should still receive the result.

```go
        writeSSE(w, "result", types.ModelResultResponse{
            Model:      mr.Model,
            Response:   mr.Response,
            ...
        })
        flusher.Flush()
    }
```

After saving to DB, immediately stream the result to the client. `flusher.Flush()` pushes it to the network buffer so the client sees it without delay.

```go
    writeSSE(w, "done", map[string]int{
        "total_wall_clock_ms": int(time.Since(wallStart).Milliseconds()),
        "models_succeeded":    succeeded,
        "models_failed":       failed,
    })
    flusher.Flush()
```

Final event. The frontend uses this to know the stream is complete.

### GetSession — grouping flat rows into nested structure

```go
func (h *Handler) GetSession(w http.ResponseWriter, r *http.Request) {
    sessionID := chi.URLParam(r, "session_id")
    rows, err := db.GetSession(h.DB, sessionID)
    ...
    orderMap := make(map[string]int)
    var turns []types.SessionTurn

    for _, row := range rows {
        idx, exists := orderMap[row.RunID]
        if !exists {
            idx = len(turns)
            orderMap[row.RunID] = idx
            turns = append(turns, types.SessionTurn{
                RunID:   row.RunID,
                Prompt:  row.Prompt,
                Results: []types.TurnResult{},
            })
        }
        turns[idx].Results = append(turns[idx].Results, types.TurnResult{...})
    }
```

The DB returns one row per (run_id, model) combination — if a turn had 3 models, there are 3 rows with the same run_id. We need to group them.

`orderMap` tracks "which index in `turns` does this run_id correspond to?" The first time we see a run_id, we create a new `SessionTurn` and record its index. Subsequent rows with the same run_id just append to `turns[idx].Results`.

### GetPricing — serving the pricing table

```go
func (h *Handler) GetPricing(w http.ResponseWriter, r *http.Request) {
    writeJSON(w, http.StatusOK, map[string]interface{}{"models": llm.GetAllRates()})
}
```

Calls `llm.GetAllRates()` and wraps the result in a `{"models": {...}}` envelope — the same shape the frontend expects. This endpoint exists so the frontend can stay in sync with `pricing.json` without duplicating its contents in TypeScript. If pricing rates or context window limits change, only `pricing.json` needs to be edited; both the backend cost calculation and the frontend display pick up the change automatically.

### queryIntOrDefault

```go
func queryIntOrDefault(r *http.Request, key string, def int) int {
    s := r.URL.Query().Get(key)
    if s == "" {
        return def
    }
    n, err := strconv.Atoi(s)
    if err != nil {
        return def
    }
    return n
}
```

`r.URL.Query().Get(key)` reads a query string parameter (e.g., `?page=2`). `strconv.Atoi` converts a string to int — returns an error if the string isn't a valid integer. In both error cases (missing, invalid), return the default.

---

## 15. [pricing.json](pricing.json) — token pricing data

```json
{
  "gpt-4o-mini":   { "input_per_1m": 0.150, "output_per_1m": 0.600,  "context_window": 128000,  "max_output_tokens": 16384 },
  "gemini-flash":  { "input_per_1m": 0.150, "output_per_1m": 0.600,  "context_window": 1048576, "max_output_tokens": 65536 },
  "claude-sonnet": { "input_per_1m": 3.000, "output_per_1m": 15.000, "context_window": 200000,  "max_output_tokens": 64000 }
}
```

Each key (`"gpt-4o-mini"`, `"gemini-flash"`, `"claude-sonnet"`) must **exactly match** the keys in `runner.go`'s `registry` map. The keys are what `CalculateCost`, `GetContextWindow`, and `GetMaxOutputTokens` look up.

- `input_per_1m` / `output_per_1m`: USD cost per 1 million tokens
- `context_window`: maximum total tokens (input + output) the model supports
- `max_output_tokens`: maximum tokens the model can generate in a single response

To add a new model: add a key to `registry` in `runner.go`, then add a matching key here. To change pricing: edit the numbers here (no code change needed).

**This file is the single source of truth for pricing.** The `GET /pricing` endpoint (added in `handlers.go`) serializes this data to JSON and serves it to the frontend on startup. The frontend previously duplicated these values in `tokens.ts`; it now fetches them dynamically via that endpoint instead, so you only need to update this one file when rates change.

---

## 16. [internal/llm/provider_test.go](internal/llm/provider_test.go) — provider unit tests

```go
package llm
```

The test file is in the same package as the code it tests (`package llm`, not `package llm_test`). This is called a **white-box test** — it can access unexported names like `bifrostProvider`, `chatRequest`, `chatResponse`.

```go
func okResponse(content string, promptTok, completionTok int) chatResponse {
    return chatResponse{
        Choices: []chatChoice{{Message: chatMessage{Role: "assistant", Content: content}}},
        Usage:   chatUsage{PromptTokens: promptTok, CompletionTokens: completionTok},
    }
}
```

A test helper that constructs a valid `chatResponse`. The `[]chatChoice{{...}}` syntax creates a slice literal with one element, and the element is an anonymous struct literal.

```go
func newProvider(srv *httptest.Server, vk string) *bifrostProvider {
    return &bifrostProvider{
        baseURL:    srv.URL,
        virtualKey: vk,
        client:     srv.Client(),
    }
}
```

Creates a `bifrostProvider` pointing at a **test server** (`httptest.Server`) instead of the real gateway. `srv.Client()` returns an HTTP client pre-configured to trust the test server's certificate.

```go
func TestProvider_Success(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        json.NewEncoder(w).Encode(okResponse("world", 5, 10))
    }))
    defer srv.Close()
```

`httptest.NewServer` starts a real HTTP server in-process, on a random port. The handler function defines what it returns. `defer srv.Close()` shuts it down after the test.

```go
    resp, err := newProvider(srv, "test-vk").Call(context.Background(), minimalReq())
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
```

`t.Fatalf` fails the test immediately with a message. `t.Errorf` (used elsewhere) marks the test as failed but continues running.

```go
func TestProvider_ContextCancelled(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        <-r.Context().Done()
    }))
    defer srv.Close()

    ctx, cancel := context.WithCancel(context.Background())
    cancel()

    _, err := newProvider(srv, "vk").Call(ctx, minimalReq())
    if err == nil {
        t.Fatal("expected error for cancelled context, got nil")
    }
}
```

The test server **hangs forever** (blocks on `<-r.Context().Done()`). We pre-cancel the context before calling. The call should return immediately with an error instead of hanging.

---

## 17. [internal/llm/runner_test.go](internal/llm/runner_test.go) — runner unit tests

```go
type mockProvider struct {
    calls   atomic.Int32
    results []callResult
}

type callResult struct {
    resp *chatResponse
    err  error
}

func (m *mockProvider) Call(ctx context.Context, req *chatRequest) (*chatResponse, error) {
    n := int(m.calls.Add(1)) - 1
    ...
    r := m.results[n]
    return r.resp, r.err
}
```

`mockProvider` implements the `Provider` interface. Each call consumes the next entry from `results`. `atomic.Int32` is a thread-safe integer — since goroutines call `Call` concurrently, a plain `int` would be a data race.

`m.calls.Add(1)` atomically increments the counter and returns the new value. Subtracting 1 gives the 0-based index.

```go
func TestCallSingleModel_Success(t *testing.T) {
    mock := successMock("hello back")
    result := callSingleModel(context.Background(), clientsWith(mock), "gpt-4o-mini", simpleReq("hi"))

    if !result.Success {
        t.Fatalf("expected success, got error: %v", result.Error)
    }
```

`callSingleModel` is tested directly (it's unexported but accessible because the test is in `package llm`). No HTTP involved — the mock intercepts at the `Provider` interface level.

```go
func TestBifrostModelID_HasProviderPrefix(t *testing.T) {
    for friendlyName, bifrostID := range registry {
        if !strings.Contains(bifrostID, "/") {
            t.Errorf("model %q has Bifrost ID %q without provider prefix", friendlyName, bifrostID)
        }
    }
}
```

A property test — validates an invariant across all entries in the registry. Every Bifrost ID must contain a `/` (the `provider/model` format).

```go
func TestBuildMessages_UsesConversationHistory(t *testing.T) {
    req := &types.RunRequest{
        Prompt: "ignored when history present",
        ModelConversations: map[string][]types.Message{
            "gpt-4o-mini": {
                {Role: "user", Content: "first turn"},
                {Role: "assistant", Content: "first reply"},
                {Role: "user", Content: "second turn"},
            },
        },
    }
    msgs := buildMessages("gpt-4o-mini", req)

    if len(msgs) != 3 {
        t.Fatalf("len: got %d, want 3", len(msgs))
    }
```

Tests the multi-turn conversation path. When `ModelConversations` contains history for the requested model, `buildMessages` should use that instead of `req.Prompt`.

---

## 18. [tests/handlers_test.go](tests/handlers_test.go) — integration tests

```go
package tests
```

`package tests` — a different package from `api`. This is a **black-box test** — it can only access exported names. It tests the HTTP API as an external client would.

```go
func newTestServer(t *testing.T) (*httptest.Server, *sql.DB) {
    t.Helper()
    database, err := sql.Open("sqlite", ":memory:")
```

`t.Helper()` marks this as a test helper function. When it calls `t.Fatalf`, the error is reported at the *caller's* line number, not inside this function — making test failures easier to trace.

`":memory:"` is a special SQLite path meaning "create this database entirely in RAM." It's destroyed when the connection closes, giving each test a clean slate.

```go
    _ = llm.LoadPricingFromMap(map[string]llm.Rate{
        "gpt-4o-mini": {InputPer1M: 0.15, OutputPer1M: 0.60},
    })
```

Inject test pricing without a file. Without this, `CalculateCost` would use a nil `pricingTable` and potentially panic.

```go
    clients := &llm.Clients{}
    router := api.NewRouter(database, clients)
    srv := httptest.NewServer(router)
    t.Cleanup(func() {
        srv.Close()
        database.Close()
    })
```

`&llm.Clients{}` creates a `Clients` with a nil `Gateway`. Every model call will fail with "LLM client not configured." That's fine for tests that verify the HTTP shape of the response, not the LLM content.

`t.Cleanup` registers a function to run when the test ends — like `defer` but scoped to the test function, not the current function call stack. This is preferred over `defer` in helpers.

```go
func TestRunEndpointReturnsShape(t *testing.T) {
    ...
    scanner := bufio.NewScanner(resp.Body)
    for scanner.Scan() {
        line := scanner.Text()
        switch {
        case strings.HasPrefix(line, "event: "):
            eventType = strings.TrimPrefix(line, "event: ")
        case strings.HasPrefix(line, "data: "):
            data = strings.TrimPrefix(line, "data: ")
        case line == "" && eventType != "":
            // blank line = end of SSE event, process it
```

Manually parsing SSE from the response body. `bufio.NewScanner` reads line by line. SSE format: `event: X`, then `data: Y`, then blank line = end of event.

```go
func TestGetSessionFound(t *testing.T) {
    srv, database := newTestServer(t)

    sessID := "test-session"
    row := &types.RunRow{
        RunID:     "run-xyz",
        SessionID: &sessID,
        ...
    }
    if err := appdb.InsertRun(database, row); err != nil {
        t.Fatalf("InsertRun: %v", err)
    }
```

Instead of going through the LLM (which would fail with nil clients), this test writes directly to the database and then verifies the HTTP GET response. A clean way to test read endpoints without needing a working LLM.

```go
func strPtr(s string) *string {
    return &s
}
```

A tiny helper at the bottom of the file. You can't write `&"literal"` in Go — you can only take the address of a variable, not a literal. This helper wraps a string in a variable and returns its address.

---

## Summary: How it all connects

```
Request arrives at :8000
        │
        ▼
router.go (chi) — matches URL, applies middleware
        │
        ▼
handlers.go — decodes JSON, validates
        │
        ├──► llm/runner.go — StreamAll()
        │         │
        │         ├──► goroutine 1: callSingleModel("gpt-4o-mini")
        │         │         └──► llm/client.go: bifrostProvider.Call()
        │         │                   └──► HTTP POST to Bifrost gateway
        │         ├──► goroutine 2: callSingleModel("gemini-flash")
        │         └──► goroutine 3: callSingleModel("claude-sonnet")
        │
        │    results arrive on channel as each model completes
        │
        ├──► db/queries.go: InsertRun() — save to SQLite
        │
        └──► writeSSE() + Flush() — stream result to client
```

**Key Go concepts you encountered:**
- `package`, imports, exported/unexported names
- `struct` with JSON tags, pointer fields (`*string`, `*int`)
- `interface{}` for dynamic values, type assertions (`x.(string)`)
- `interface` as contract; implicit implementation
- Multiple return values; `(value, error)` pattern
- `defer` for cleanup
- Goroutines (`go func()`) and buffered channels (`make(chan T, n)`)
- `context.WithTimeout` for deadlines that propagate through call chains
- Package-level variables as singletons
- `map[K]V` with two-value lookup (`v, ok := m[key]`)
- `append` for growing slices
- `errors.As` for typed error unwrapping
- `httptest.Server` for in-process HTTP testing
