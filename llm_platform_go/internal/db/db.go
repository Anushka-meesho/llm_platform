package db

import (
	"database/sql"
	"fmt"

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
	`)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	// Additive migrations — SQLite has no ADD COLUMN IF NOT EXISTS, so ignore
	// "duplicate column name" errors on repeated startups.
	db.Exec("ALTER TABLE runs ADD COLUMN rating INTEGER")
	db.Exec("ALTER TABLE runs ADD COLUMN note TEXT")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_runs_run_id_model ON runs(run_id, model)")

	return nil
}
