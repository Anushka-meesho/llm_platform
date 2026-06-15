package tasks

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrVersionNotFound is returned when a prompt version doesn't exist.
var ErrVersionNotFound = errors.New("prompt version not found")

// PromptVersion is one historical (or draft) prompt for a task. The task's
// active prompt is whichever version `tasks.prompt_version` points at.
type PromptVersion struct {
	TaskID         string    `json:"task_id"`
	Version        int       `json:"version"`
	PromptTemplate string    `json:"prompt_template"`
	SystemPrompt   string    `json:"system_prompt"`
	Note           string    `json:"note,omitempty"`
	CreatedBy      string    `json:"created_by,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	Active         bool      `json:"active"` // == task's current prompt_version
}

// maxVersion returns the highest version number recorded for a task (0 if none).
// Drafts count — new versions always go past every existing row.
func (s *Store) maxVersion(taskID string) (int, error) {
	var v int
	err := s.db.QueryRow(
		`SELECT COALESCE(MAX(version), 0) FROM prompt_versions WHERE task_id = ?`, taskID,
	).Scan(&v)
	return v, err
}

// appendVersion records a prompt_versions row.
func (s *Store) appendVersion(taskID string, version int, tmpl, sys, note, by string) error {
	_, err := s.db.Exec(`
		INSERT INTO prompt_versions
			(task_id, version, prompt_template, system_prompt, note, created_by, created_at)
		VALUES (?,?,?,?,?,?,?)`,
		taskID, version, tmpl, sys, note, by, fmtTime(time.Now()),
	)
	return err
}

// ListVersions returns all versions for a task, newest first, with the active
// one flagged.
func (s *Store) ListVersions(taskID string) ([]PromptVersion, error) {
	task, err := s.Get(taskID)
	if err != nil {
		return nil, err
	}

	rows, err := s.db.Query(`
		SELECT task_id, version, prompt_template, system_prompt, note, created_by, created_at
		FROM prompt_versions WHERE task_id = ? ORDER BY version DESC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []PromptVersion{}
	for rows.Next() {
		var v PromptVersion
		var createdAt string
		if err := rows.Scan(&v.TaskID, &v.Version, &v.PromptTemplate, &v.SystemPrompt,
			&v.Note, &v.CreatedBy, &createdAt); err != nil {
			return nil, err
		}
		v.CreatedAt = parseTime(createdAt)
		v.Active = v.Version == task.PromptVersion
		out = append(out, v)
	}
	return out, rows.Err()
}

// GetVersion returns one specific version.
func (s *Store) GetVersion(taskID string, version int) (*PromptVersion, error) {
	row := s.db.QueryRow(`
		SELECT task_id, version, prompt_template, system_prompt, note, created_by, created_at
		FROM prompt_versions WHERE task_id = ? AND version = ?`, taskID, version)

	var v PromptVersion
	var createdAt string
	err := row.Scan(&v.TaskID, &v.Version, &v.PromptTemplate, &v.SystemPrompt,
		&v.Note, &v.CreatedBy, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrVersionNotFound
	}
	if err != nil {
		return nil, err
	}
	v.CreatedAt = parseTime(createdAt)
	return &v, nil
}

// SaveDraft records a new prompt version WITHOUT activating it. Returns the
// assigned version number (max existing + 1, so drafts and deploys never collide).
func (s *Store) SaveDraft(taskID, tmpl, sys, note, by string) (int, error) {
	if _, err := s.Get(taskID); err != nil {
		return 0, err
	}
	if tmpl == "" {
		return 0, errors.New("prompt_template is required")
	}
	if _, err := parseTemplate(tmpl); err != nil {
		return 0, fmt.Errorf("prompt_template: %w", err)
	}

	maxV, err := s.maxVersion(taskID)
	if err != nil {
		return 0, err
	}
	next := maxV + 1
	if err := s.appendVersion(taskID, next, tmpl, sys, note, by); err != nil {
		return 0, err
	}
	return next, nil
}

// Deploy activates an existing version: the task's live prompt becomes that
// version's template/system prompt and runs stamp its number.
//
// NOTE (Phase 2): the eval quality gate slots in here — refuse to deploy a
// version whose eval run is missing or below the task's thresholds.
func (s *Store) Deploy(taskID string, version int) error {
	v, err := s.GetVersion(taskID, version)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(`
		UPDATE tasks SET prompt_template = ?, system_prompt = ?, prompt_version = ?, updated_at = ?
		WHERE id = ?`,
		v.PromptTemplate, v.SystemPrompt, v.Version, fmtTime(time.Now()), taskID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	s.invalidate(taskID)
	return nil
}
