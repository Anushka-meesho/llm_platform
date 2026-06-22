package db

import (
	"database/sql"
	"fmt"
)

// migratePostgres applies the schema on Postgres. Every statement is idempotent
// (CREATE … IF NOT EXISTS, ADD COLUMN IF NOT EXISTS) so it is safe to run on
// every boot / via cmd/migrate. The column set mirrors the SQLite schema after
// all its guarded ALTERs, with Postgres-native types:
//
//	INTEGER PRIMARY KEY AUTOINCREMENT  -> BIGINT GENERATED ALWAYS AS IDENTITY
//	REAL                               -> DOUBLE PRECISION
//	DATETIME                           -> TEXT (canonical "YYYY-MM-DD HH:MM:SS",
//	                                      same as SQLite — see dialect.go)
//
// Booleans stay INTEGER 0/1 to match boolToInt and the `== 1` scans shared with
// SQLite, so no scanning code is dialect-specific.
//
// NOTE: This path is implemented but has not yet been validated against a live
// Postgres instance in CI. Run the suite with DB_DRIVER=postgres against a real
// server before trusting it in production (see the deploy README).
func migratePostgres(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS runs (
			id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			run_id        TEXT NOT NULL,
			session_id    TEXT,
			prompt        TEXT NOT NULL,
			system_prompt TEXT,
			model         TEXT NOT NULL,
			response      TEXT,
			latency_ms    BIGINT NOT NULL DEFAULT 0,
			input_tokens  BIGINT NOT NULL DEFAULT 0,
			output_tokens BIGINT NOT NULL DEFAULT 0,
			total_tokens  BIGINT NOT NULL DEFAULT 0,
			cost_usd      DOUBLE PRECISION NOT NULL DEFAULT 0.0,
			success       INTEGER NOT NULL DEFAULT 0,
			error         TEXT,
			created_at    TEXT NOT NULL DEFAULT to_char((now() at time zone 'utc'), 'YYYY-MM-DD HH24:MI:SS')
		)`,
		`ALTER TABLE runs ADD COLUMN IF NOT EXISTS user_id TEXT`,
		`ALTER TABLE runs ADD COLUMN IF NOT EXISTS user_email TEXT`,
		`ALTER TABLE runs ADD COLUMN IF NOT EXISTS task_id TEXT`,
		`ALTER TABLE runs ADD COLUMN IF NOT EXISTS prompt_version INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE runs ADD COLUMN IF NOT EXISTS provider TEXT`,
		`ALTER TABLE runs ADD COLUMN IF NOT EXISTS fallback_used INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE runs ADD COLUMN IF NOT EXISTS cache_hit INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE runs ADD COLUMN IF NOT EXISTS is_test INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE runs ADD COLUMN IF NOT EXISTS image TEXT`,
		`CREATE INDEX IF NOT EXISTS idx_runs_run_id     ON runs(run_id)`,
		`CREATE INDEX IF NOT EXISTS idx_runs_session_id ON runs(session_id)`,
		`CREATE INDEX IF NOT EXISTS idx_runs_user_id    ON runs(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_runs_task_id    ON runs(task_id)`,

		`CREATE TABLE IF NOT EXISTS feedback (
			id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			run_id     TEXT    NOT NULL,
			model      TEXT    NOT NULL,
			user_id    TEXT    NOT NULL,
			rating     INTEGER NOT NULL,
			created_at TEXT NOT NULL DEFAULT to_char((now() at time zone 'utc'), 'YYYY-MM-DD HH24:MI:SS')
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_feedback_unique ON feedback(run_id, model, user_id)`,

		`CREATE TABLE IF NOT EXISTS tasks (
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
			temperature      DOUBLE PRECISION NOT NULL DEFAULT 0.2,
			max_tokens       INTEGER NOT NULL DEFAULT 1000,
			daily_budget_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
			max_prompt_chars INTEGER NOT NULL DEFAULT 0,
			max_image_kb     INTEGER NOT NULL DEFAULT 0,
			max_images       INTEGER NOT NULL DEFAULT 0,
			active           INTEGER NOT NULL DEFAULT 1,
			created_at       TEXT NOT NULL DEFAULT to_char((now() at time zone 'utc'), 'YYYY-MM-DD HH24:MI:SS'),
			updated_at       TEXT NOT NULL DEFAULT to_char((now() at time zone 'utc'), 'YYYY-MM-DD HH24:MI:SS')
		)`,
		`ALTER TABLE tasks ADD COLUMN IF NOT EXISTS cache_enabled     INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE tasks ADD COLUMN IF NOT EXISTS cache_ttl_seconds INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE tasks ADD COLUMN IF NOT EXISTS max_prompt_chars  INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE tasks ADD COLUMN IF NOT EXISTS max_image_kb      INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE tasks ADD COLUMN IF NOT EXISTS max_images        INTEGER NOT NULL DEFAULT 0`,

		`CREATE TABLE IF NOT EXISTS prompt_versions (
			id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			task_id         TEXT NOT NULL,
			version         INTEGER NOT NULL,
			prompt_template TEXT NOT NULL,
			system_prompt   TEXT NOT NULL DEFAULT '',
			note            TEXT NOT NULL DEFAULT '',
			created_by      TEXT NOT NULL DEFAULT '',
			created_at      TEXT NOT NULL DEFAULT to_char((now() at time zone 'utc'), 'YYYY-MM-DD HH24:MI:SS'),
			UNIQUE(task_id, version)
		)`,

		`CREATE TABLE IF NOT EXISTS shadow_reports (
			id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			task_id        TEXT NOT NULL,
			created_by     TEXT NOT NULL DEFAULT '',
			items          INTEGER NOT NULL,
			match_rate     DOUBLE PRECISION NOT NULL,
			avg_latency_ms DOUBLE PRECISION NOT NULL,
			p95_latency_ms DOUBLE PRECISION NOT NULL,
			total_cost_usd DOUBLE PRECISION NOT NULL,
			details        TEXT NOT NULL DEFAULT '{}',
			created_at     TEXT NOT NULL DEFAULT to_char((now() at time zone 'utc'), 'YYYY-MM-DD HH24:MI:SS')
		)`,
		`CREATE INDEX IF NOT EXISTS idx_shadow_task ON shadow_reports(task_id)`,

		`CREATE TABLE IF NOT EXISTS model_health_events (
			id                   BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			task_id              TEXT NOT NULL,
			model                TEXT NOT NULL,
			provider             TEXT NOT NULL DEFAULT '',
			event                TEXT NOT NULL,
			reason               TEXT NOT NULL DEFAULT '',
			consecutive_failures INTEGER NOT NULL DEFAULT 0,
			cooldown_ms          BIGINT NOT NULL DEFAULT 0,
			state                TEXT NOT NULL DEFAULT '',
			created_at           TEXT NOT NULL DEFAULT to_char((now() at time zone 'utc'), 'YYYY-MM-DD HH24:MI:SS')
		)`,
		`CREATE INDEX IF NOT EXISTS idx_health_task_model ON model_health_events(task_id, model)`,
		`CREATE INDEX IF NOT EXISTS idx_health_created_at ON model_health_events(created_at)`,

		`CREATE TABLE IF NOT EXISTS gateway_attempts (
			id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			run_id            TEXT NOT NULL,
			task_id           TEXT,
			seq               INTEGER NOT NULL DEFAULT 0,
			model             TEXT NOT NULL,
			provider          TEXT NOT NULL DEFAULT '',
			outcome           TEXT NOT NULL,
			fallback_used     INTEGER NOT NULL DEFAULT 0,
			fallback_reason   TEXT NOT NULL DEFAULT '',
			response          TEXT,
			error             TEXT NOT NULL DEFAULT '',
			http_status       INTEGER NOT NULL DEFAULT 0,
			infra_failure     INTEGER NOT NULL DEFAULT 0,
			retry_count       INTEGER NOT NULL DEFAULT 0,
			latency_ms        BIGINT NOT NULL DEFAULT 0,
			input_tokens      BIGINT NOT NULL DEFAULT 0,
			output_tokens     BIGINT NOT NULL DEFAULT 0,
			total_tokens      BIGINT NOT NULL DEFAULT 0,
			cost_usd          DOUBLE PRECISION NOT NULL DEFAULT 0.0,
			is_test           INTEGER NOT NULL DEFAULT 0,
			created_at        TEXT NOT NULL DEFAULT to_char((now() at time zone 'utc'), 'YYYY-MM-DD HH24:MI:SS')
		)`,
		`ALTER TABLE gateway_attempts ADD COLUMN IF NOT EXISTS response TEXT`,
		`CREATE INDEX IF NOT EXISTS idx_attempts_run_id     ON gateway_attempts(run_id)`,
		`CREATE INDEX IF NOT EXISTS idx_attempts_task_id    ON gateway_attempts(task_id)`,
		`CREATE INDEX IF NOT EXISTS idx_attempts_model      ON gateway_attempts(model)`,
		`CREATE INDEX IF NOT EXISTS idx_attempts_outcome    ON gateway_attempts(outcome)`,
		`CREATE INDEX IF NOT EXISTS idx_attempts_created_at ON gateway_attempts(created_at)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return fmt.Errorf("migrate postgres: %w", err)
		}
	}

	// Backfill: every task's active prompt gets a prompt_versions row, so version
	// history starts populated on upgraded databases (mirrors the SQLite backfill).
	if _, err := db.Exec(`
		INSERT INTO prompt_versions
			(task_id, version, prompt_template, system_prompt, note, created_at)
		SELECT id, prompt_version, prompt_template, system_prompt,
		       'backfilled from active task config',
		       to_char((now() at time zone 'utc'), 'YYYY-MM-DD HH24:MI:SS')
		FROM tasks
		ON CONFLICT (task_id, version) DO NOTHING`); err != nil {
		return fmt.Errorf("migrate postgres backfill prompt_versions: %w", err)
	}
	return nil
}
