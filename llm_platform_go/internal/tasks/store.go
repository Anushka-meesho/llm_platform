package tasks

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrNotFound is returned when no task matches the id.
var ErrNotFound = errors.New("task not found")

// configCacheTTL bounds how long a cached task config can serve without a DB
// read. Writes through this Store invalidate immediately; the TTL only covers
// out-of-band changes (another instance, manual SQL) converging.
const configCacheTTL = 5 * time.Second

type cachedTask struct {
	task      *Task
	fetchedAt time.Time
}

// Store persists tasks in the platform's SQLite database (table created in
// db.Migrate). All access goes through this type so a future Postgres move is
// contained here.
//
// Get is served from an in-memory config cache: task configs are read on every
// prediction but change rarely, so the hot path must not touch the database.
// Callers must treat returned *Task values as immutable — copy before mutating
// (the update handler already does).
type Store struct {
	db *sql.DB

	mu    sync.RWMutex
	cache map[string]cachedTask
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db, cache: map[string]cachedTask{}}
}

// invalidate drops a task from the config cache after any write to it.
func (s *Store) invalidate(id string) {
	s.mu.Lock()
	delete(s.cache, id)
	s.mu.Unlock()
}

const taskColumns = `id, name, description, input_schema, output_schema,
	prompt_template, system_prompt, prompt_version, model, fallback_models,
	temperature, max_tokens, daily_budget_usd, cache_enabled, cache_ttl_seconds,
	active, created_at, updated_at`

// Create inserts a new task. Fails if the id already exists.
func (s *Store) Create(t *Task) error {
	t.applyDefaults()
	if err := t.Validate(); err != nil {
		return err
	}
	now := time.Now().UTC()
	t.CreatedAt, t.UpdatedAt = now, now
	t.Active = true

	_, err := s.db.Exec(`
		INSERT INTO tasks (`+taskColumns+`)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.Name, t.Description,
		nullableJSON(t.InputSchema), nullableJSON(t.OutputSchema),
		t.PromptTemplate, t.SystemPrompt, t.PromptVersion, t.Model,
		marshalStrings(t.FallbackModels),
		t.Temperature, t.MaxTokens, t.DailyBudgetUSD,
		boolToInt(t.CacheEnabled), t.CacheTTLSeconds, boolToInt(t.Active),
		fmtTime(t.CreatedAt), fmtTime(t.UpdatedAt),
	)
	if err != nil {
		return err
	}
	s.invalidate(t.ID)
	// Record the initial prompt in the version history.
	return s.appendVersion(t.ID, t.PromptVersion, t.PromptTemplate, t.SystemPrompt,
		"initial version", "")
}

// Get returns one task by id, or ErrNotFound. Served from the config cache
// when fresh — the prediction hot path must not pay a DB read per request.
func (s *Store) Get(id string) (*Task, error) {
	s.mu.RLock()
	if c, ok := s.cache[id]; ok && time.Since(c.fetchedAt) < configCacheTTL {
		s.mu.RUnlock()
		return c.task, nil
	}
	s.mu.RUnlock()

	row := s.db.QueryRow(`SELECT `+taskColumns+` FROM tasks WHERE id = ?`, id)
	t, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.cache[id] = cachedTask{task: t, fetchedAt: time.Now()}
	s.mu.Unlock()
	return t, nil
}

// List returns all tasks ordered by id.
func (s *Store) List() ([]*Task, error) {
	rows, err := s.db.Query(`SELECT ` + taskColumns + ` FROM tasks ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []*Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Update overwrites a task's mutable fields. If the prompt (template or system
// prompt) changed, the prompt version is bumped so runs attribute correctly.
func (s *Store) Update(t *Task) error {
	existing, err := s.Get(t.ID)
	if err != nil {
		return err
	}

	t.applyDefaults()
	t.PromptVersion = existing.PromptVersion
	promptChanged := t.PromptTemplate != existing.PromptTemplate ||
		t.SystemPrompt != existing.SystemPrompt
	if promptChanged {
		// Next version goes past every recorded version (drafts included) so
		// numbers never collide with saved-but-undeployed drafts.
		maxV, err := s.maxVersion(t.ID)
		if err != nil {
			return err
		}
		if maxV < existing.PromptVersion {
			maxV = existing.PromptVersion // safety on un-backfilled rows
		}
		t.PromptVersion = maxV + 1
	}
	if err := t.Validate(); err != nil {
		return err
	}
	t.CreatedAt = existing.CreatedAt
	t.UpdatedAt = time.Now().UTC()

	_, err = s.db.Exec(`
		UPDATE tasks SET
			name = ?, description = ?, input_schema = ?, output_schema = ?,
			prompt_template = ?, system_prompt = ?, prompt_version = ?, model = ?,
			fallback_models = ?, temperature = ?, max_tokens = ?,
			daily_budget_usd = ?, cache_enabled = ?, cache_ttl_seconds = ?,
			active = ?, updated_at = ?
		WHERE id = ?`,
		t.Name, t.Description,
		nullableJSON(t.InputSchema), nullableJSON(t.OutputSchema),
		t.PromptTemplate, t.SystemPrompt, t.PromptVersion, t.Model,
		marshalStrings(t.FallbackModels),
		t.Temperature, t.MaxTokens, t.DailyBudgetUSD,
		boolToInt(t.CacheEnabled), t.CacheTTLSeconds, boolToInt(t.Active),
		fmtTime(t.UpdatedAt), t.ID,
	)
	if err != nil {
		return err
	}
	s.invalidate(t.ID)
	if promptChanged {
		return s.appendVersion(t.ID, t.PromptVersion, t.PromptTemplate, t.SystemPrompt,
			"updated via task update", "")
	}
	return nil
}

// Upsert creates the task if absent, otherwise updates it. Used by YAML seeding.
// YAML configs don't model activation state, so re-seeding preserves whatever
// active flag the task currently has (a fresh create starts active).
func (s *Store) Upsert(t *Task) error {
	existing, err := s.Get(t.ID)
	if errors.Is(err, ErrNotFound) {
		return s.Create(t)
	}
	if err != nil {
		return err
	}
	t.Active = existing.Active
	return s.Update(t)
}

// ── scanning helpers ─────────────────────────────────────────────────────────

type rowScanner interface{ Scan(dest ...any) error }

func scanTask(r rowScanner) (*Task, error) {
	var t Task
	var inSchema, outSchema sql.NullString
	var fallbacks string
	var cacheEnabled, active int
	var createdAt, updatedAt string

	err := r.Scan(
		&t.ID, &t.Name, &t.Description, &inSchema, &outSchema,
		&t.PromptTemplate, &t.SystemPrompt, &t.PromptVersion, &t.Model, &fallbacks,
		&t.Temperature, &t.MaxTokens, &t.DailyBudgetUSD,
		&cacheEnabled, &t.CacheTTLSeconds, &active,
		&createdAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}

	if inSchema.Valid && inSchema.String != "" {
		t.InputSchema = json.RawMessage(inSchema.String)
	}
	if outSchema.Valid && outSchema.String != "" {
		t.OutputSchema = json.RawMessage(outSchema.String)
	}
	if err := json.Unmarshal([]byte(fallbacks), &t.FallbackModels); err != nil {
		return nil, fmt.Errorf("task %s: bad fallback_models: %w", t.ID, err)
	}
	t.CacheEnabled = cacheEnabled == 1
	t.Active = active == 1
	t.CreatedAt = parseTime(createdAt)
	t.UpdatedAt = parseTime(updatedAt)
	return &t, nil
}

func nullableJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}

func marshalStrings(ss []string) string {
	if ss == nil {
		ss = []string{}
	}
	b, _ := json.Marshal(ss)
	return string(b)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func fmtTime(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04:05")
}

func parseTime(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04:05", s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}
