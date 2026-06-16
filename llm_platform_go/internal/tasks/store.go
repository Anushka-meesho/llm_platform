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

	// editMu coordinates config edits with reads. Every write to a task's
	// config or version history holds it exclusively for the whole critical
	// section; every read (Get) holds it shared. So a prediction reading routing
	// while an edit is in flight blocks until the edit commits, then sees the
	// new config — readers never observe a half-applied edit, and never keep
	// serving a stale chain once an edit has started. Edits are rare and reads
	// are frequent-but-shared, so the contention cost lands only during an edit.
	editMu sync.RWMutex

	// cacheMu guards only the in-memory config map and is never held across I/O.
	cacheMu sync.Mutex
	cache   map[string]cachedTask
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db, cache: map[string]cachedTask{}}
}

// invalidate drops a task from the config cache after any write to it. Callers
// already hold editMu exclusively; this guards the map alone.
func (s *Store) invalidate(id string) {
	s.cacheMu.Lock()
	delete(s.cache, id)
	s.cacheMu.Unlock()
}

const taskColumns = `id, name, description, input_schema, output_schema,
	prompt_template, system_prompt, prompt_version, model, fallback_models,
	temperature, max_tokens, daily_budget_usd, cache_enabled, cache_ttl_seconds,
	active, created_at, updated_at`

// Create inserts a new task. Fails if the id already exists.
func (s *Store) Create(t *Task) error {
	s.editMu.Lock()
	defer s.editMu.Unlock()
	return s.createLocked(t)
}

// createLocked is Create's body; callers must already hold editMu exclusively.
func (s *Store) createLocked(t *Task) error {
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
// Held under editMu (shared): a read concurrent with a config edit waits for
// that edit to commit, then sees the new config.
func (s *Store) Get(id string) (*Task, error) {
	s.editMu.RLock()
	defer s.editMu.RUnlock()
	return s.getRaw(id)
}

// getRaw is the cache-or-DB read without the editMu gate. Edits call it while
// already holding editMu exclusively (calling Get there would deadlock).
func (s *Store) getRaw(id string) (*Task, error) {
	s.cacheMu.Lock()
	if c, ok := s.cache[id]; ok && time.Since(c.fetchedAt) < configCacheTTL {
		t := c.task
		s.cacheMu.Unlock()
		return t, nil
	}
	s.cacheMu.Unlock()

	row := s.db.QueryRow(`SELECT `+taskColumns+` FROM tasks WHERE id = ?`, id)
	t, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	s.cacheMu.Lock()
	s.cache[id] = cachedTask{task: t, fetchedAt: time.Now()}
	s.cacheMu.Unlock()
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
	s.editMu.Lock()
	defer s.editMu.Unlock()
	return s.updateLocked(t)
}

// updateLocked is Update's body; callers must already hold editMu exclusively.
func (s *Store) updateLocked(t *Task) error {
	existing, err := s.getRaw(t.ID)
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
//
// For an existing task the YAML seed must not clobber state that's owned at
// runtime, so re-seeding preserves:
//   - the active flag (YAML doesn't model activation; a fresh create is active);
//   - the model routing — model + fallback chain. Routing is operational config
//     tuned live via the API, so the YAML value seeds it only at first creation
//     and never overwrites a saved chain. It then persists across restarts until
//     someone changes it through the API.
//
// Prompt/schema edits in YAML do still re-apply on restart (the onboarding
// contract: edit the prompt, restart, version bumps).
func (s *Store) Upsert(t *Task) error {
	s.editMu.Lock()
	defer s.editMu.Unlock()
	existing, err := s.getRaw(t.ID)
	if errors.Is(err, ErrNotFound) {
		return s.createLocked(t)
	}
	if err != nil {
		return err
	}
	t.Active = existing.Active
	t.Model = existing.Model
	t.FallbackModels = existing.FallbackModels
	return s.updateLocked(t)
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

// timeLayouts are the formats parseTime accepts. We write the space-separated
// form via fmtTime, but the modernc.org/sqlite driver recognizes DATETIME
// columns and round-trips them as RFC3339 ("2006-01-02T15:04:05Z") when scanned
// into a string — so a value we stored as "2006-01-02 15:04:05" comes back in
// RFC3339. parseTime must accept both or every version/run timestamp reads as
// the zero time (which surfaced in the UI as "0001-01-01").
var timeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
}

func parseTime(s string) time.Time {
	for _, layout := range timeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
