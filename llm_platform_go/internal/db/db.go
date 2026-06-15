package db

import (
	"database/sql"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// Single writer — prevents "database is locked" under concurrent requests.
	db.SetMaxOpenConns(1)

	if _, err = db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}
	if _, err = db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}

	return db, nil
}

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

		CREATE TABLE IF NOT EXISTS feedback (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			run_id     TEXT    NOT NULL,
			model      TEXT    NOT NULL,
			user_id    TEXT    NOT NULL,
			rating     INTEGER NOT NULL,
			created_at DATETIME NOT NULL DEFAULT (datetime('now'))
		);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_feedback_unique
			ON feedback(run_id, model, user_id);

		CREATE TABLE IF NOT EXISTS tasks (
			id               TEXT PRIMARY KEY,
			name             TEXT NOT NULL,
			description      TEXT NOT NULL DEFAULT '',
			input_schema     TEXT,
			output_schema    TEXT,
			prompt_template  TEXT NOT NULL,
			system_prompt    TEXT NOT NULL DEFAULT '',
			prompt_version   INTEGER NOT NULL DEFAULT 1,
			model            TEXT NOT NULL,
			fallback_models  TEXT NOT NULL DEFAULT '[]',
			temperature      REAL NOT NULL DEFAULT 0.2,
			max_tokens       INTEGER NOT NULL DEFAULT 1000,
			daily_budget_usd REAL NOT NULL DEFAULT 0,
			active           INTEGER NOT NULL DEFAULT 1,
			created_at       DATETIME NOT NULL DEFAULT (datetime('now')),
			updated_at       DATETIME NOT NULL DEFAULT (datetime('now'))
		);

		CREATE TABLE IF NOT EXISTS prompt_versions (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id         TEXT NOT NULL,
			version         INTEGER NOT NULL,
			prompt_template TEXT NOT NULL,
			system_prompt   TEXT NOT NULL DEFAULT '',
			note            TEXT NOT NULL DEFAULT '',
			created_by      TEXT NOT NULL DEFAULT '',
			created_at      DATETIME NOT NULL DEFAULT (datetime('now')),
			UNIQUE(task_id, version)
		);

		CREATE TABLE IF NOT EXISTS shadow_reports (
			id             INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id        TEXT NOT NULL,
			created_by     TEXT NOT NULL DEFAULT '',
			items          INTEGER NOT NULL,
			match_rate     REAL NOT NULL,
			avg_latency_ms REAL NOT NULL,
			p95_latency_ms REAL NOT NULL,
			total_cost_usd REAL NOT NULL,
			details        TEXT NOT NULL DEFAULT '{}',
			created_at     DATETIME NOT NULL DEFAULT (datetime('now'))
		);
		CREATE INDEX IF NOT EXISTS idx_shadow_task ON shadow_reports(task_id);
	`)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	// Per-user columns on runs. Added via guarded ALTER so existing databases
	// upgrade in place; "duplicate column" means already migrated.
	for _, col := range []string{
		"ALTER TABLE runs ADD COLUMN user_id TEXT",
		"ALTER TABLE runs ADD COLUMN user_email TEXT",
		// Phase 0: task keying + observability columns (design doc §3.5).
		"ALTER TABLE runs ADD COLUMN task_id TEXT",
		"ALTER TABLE runs ADD COLUMN prompt_version INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE runs ADD COLUMN provider TEXT",
		"ALTER TABLE runs ADD COLUMN fallback_used INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE runs ADD COLUMN cache_hit INTEGER NOT NULL DEFAULT 0",
		// Phase 1: Studio test calls are flagged so production stats can filter.
		"ALTER TABLE runs ADD COLUMN is_test INTEGER NOT NULL DEFAULT 0",
		// Prediction cache: per-task opt-in + TTL (0 = backend default 24h).
		"ALTER TABLE tasks ADD COLUMN cache_enabled INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE tasks ADD COLUMN cache_ttl_seconds INTEGER NOT NULL DEFAULT 0",
	} {
		if _, err := db.Exec(col); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("migrate alter: %w", err)
		}
	}
	for _, idx := range []string{
		"CREATE INDEX IF NOT EXISTS idx_runs_user_id ON runs(user_id)",
		"CREATE INDEX IF NOT EXISTS idx_runs_task_id ON runs(task_id)",
	} {
		if _, err := db.Exec(idx); err != nil {
			return fmt.Errorf("migrate index: %w", err)
		}
	}

	// Backfill: every task's active prompt gets a prompt_versions row, so
	// version history starts populated on upgraded databases.
	if _, err := db.Exec(`
		INSERT OR IGNORE INTO prompt_versions
			(task_id, version, prompt_template, system_prompt, note, created_at)
		SELECT id, prompt_version, prompt_template, system_prompt,
		       'backfilled from active task config', datetime('now')
		FROM tasks`); err != nil {
		return fmt.Errorf("migrate backfill prompt_versions: %w", err)
	}

	return nil
}
