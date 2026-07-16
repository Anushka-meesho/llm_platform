package tasks

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// EvalDataset is a versioned labelled-example set for one task.
type EvalDataset struct {
	ID            int64             `json:"id"`
	TaskID        string            `json:"task_id"`
	Name          string            `json:"name"`
	Version       int               `json:"version"`
	SourceType    string            `json:"source_type"`
	SourceRef     string            `json:"source_ref,omitempty"`
	Status        string            `json:"status"`
	InputMapping  map[string]string `json:"input_mapping,omitempty"`
	OutputMapping map[string]string `json:"output_mapping,omitempty"`
	RowCount      int               `json:"row_count"`
	SchemaHash    string            `json:"schema_hash,omitempty"`
	CreatedBy     string            `json:"created_by,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
}

// EvalExample is one labelled row in a dataset.
type EvalExample struct {
	ID             int64           `json:"id"`
	DatasetID      int64           `json:"dataset_id"`
	ExampleID      string          `json:"example_id,omitempty"`
	RowNo          int             `json:"row_no"`
	Inputs         json.RawMessage `json:"inputs"`
	ExpectedOutput json.RawMessage `json:"expected_output"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
}

// EvalRun records the result of running one prompt version over one dataset.
type EvalRun struct {
	ID             int64           `json:"id"`
	TaskID         string          `json:"task_id"`
	PromptVersion  int             `json:"prompt_version"`
	DatasetID      int64           `json:"dataset_id"`
	DatasetName    string          `json:"dataset_name"`
	DatasetVersion int             `json:"dataset_version"`
	Model          string          `json:"model"`
	Total          int             `json:"total"`
	Passed         int             `json:"passed"`
	Failed         int             `json:"failed"`
	MatchRate      float64         `json:"match_rate"`
	AvgLatencyMs   float64         `json:"avg_latency_ms"`
	TotalCostUSD   float64         `json:"total_cost_usd"`
	Details        json.RawMessage `json:"details"`
	CreatedBy      string          `json:"created_by,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
}

// ErrDatasetNotFound is returned when a dataset id doesn't belong to the task.
var ErrDatasetNotFound = errors.New("eval dataset not found")

// NextEvalDatasetVersion returns the next version for a task-scoped dataset name.
func (s *Store) NextEvalDatasetVersion(taskID, name string) (int, error) {
	var v int
	err := s.db.QueryRow(
		`SELECT COALESCE(MAX(version), 0) + 1 FROM eval_datasets WHERE task_id = ? AND name = ?`,
		taskID, name,
	).Scan(&v)
	return v, err
}

// CreateEvalDataset inserts one dataset and its examples atomically.
func (s *Store) CreateEvalDataset(ds *EvalDataset, examples []EvalExample) error {
	if ds.Name == "" {
		return errors.New("dataset name is required")
	}
	if ds.SourceType == "" {
		return errors.New("source_type is required")
	}
	if ds.Status == "" {
		ds.Status = "ready"
	}
	ds.RowCount = len(examples)
	ds.CreatedAt = time.Now().UTC()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	inputMapping, _ := json.Marshal(ds.InputMapping)
	outputMapping, _ := json.Marshal(ds.OutputMapping)
	res, err := tx.Exec(`
		INSERT INTO eval_datasets
			(task_id, name, version, source_type, source_ref, status,
			 input_mapping, output_mapping, row_count, schema_hash, created_by, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		ds.TaskID, ds.Name, ds.Version, ds.SourceType, ds.SourceRef, ds.Status,
		string(inputMapping), string(outputMapping), ds.RowCount, ds.SchemaHash,
		ds.CreatedBy, fmtTime(ds.CreatedAt),
	)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	ds.ID = id

	for _, ex := range examples {
		metadata := ex.Metadata
		if len(metadata) == 0 {
			metadata = json.RawMessage(`{}`)
		}
		if _, err := tx.Exec(`
			INSERT INTO eval_examples
				(dataset_id, example_id, row_no, inputs_json, expected_output_json, metadata_json)
			VALUES (?,?,?,?,?,?)`,
			id, ex.ExampleID, ex.RowNo, string(ex.Inputs), string(ex.ExpectedOutput), string(metadata),
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListEvalDatasets returns datasets for a task, newest versions first.
func (s *Store) ListEvalDatasets(taskID string) ([]EvalDataset, error) {
	rows, err := s.db.Query(`
		SELECT id, task_id, name, version, source_type, source_ref, status,
		       input_mapping, output_mapping, row_count, schema_hash, created_by, created_at
		FROM eval_datasets
		WHERE task_id = ?
		ORDER BY id DESC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []EvalDataset{}
	for rows.Next() {
		ds, err := scanEvalDataset(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *ds)
	}
	return out, rows.Err()
}

// GetEvalDataset returns one dataset by id, scoped to its task.
func (s *Store) GetEvalDataset(taskID string, datasetID int64) (*EvalDataset, error) {
	row := s.db.QueryRow(`
		SELECT id, task_id, name, version, source_type, source_ref, status,
		       input_mapping, output_mapping, row_count, schema_hash, created_by, created_at
		FROM eval_datasets
		WHERE task_id = ? AND id = ?`, taskID, datasetID)
	ds, err := scanEvalDataset(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDatasetNotFound
	}
	return ds, err
}

// ListEvalExamples loads examples for a dataset. limit <= 0 means all examples.
func (s *Store) ListEvalExamples(taskID string, datasetID int64, limit int) ([]EvalExample, error) {
	if _, err := s.GetEvalDataset(taskID, datasetID); err != nil {
		return nil, err
	}
	query := `
		SELECT id, dataset_id, example_id, row_no, inputs_json, expected_output_json, metadata_json
		FROM eval_examples
		WHERE dataset_id = ?
		ORDER BY row_no ASC`
	args := []any{datasetID}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []EvalExample{}
	for rows.Next() {
		var ex EvalExample
		var inputs, expected, metadata string
		if err := rows.Scan(&ex.ID, &ex.DatasetID, &ex.ExampleID, &ex.RowNo, &inputs, &expected, &metadata); err != nil {
			return nil, err
		}
		ex.Inputs = json.RawMessage(inputs)
		ex.ExpectedOutput = json.RawMessage(expected)
		ex.Metadata = json.RawMessage(metadata)
		out = append(out, ex)
	}
	return out, rows.Err()
}

// RecordEvalRun persists an eval run summary.
func (s *Store) RecordEvalRun(run *EvalRun) error {
	if run.CreatedAt.IsZero() {
		run.CreatedAt = time.Now().UTC()
	}
	details := run.Details
	if len(details) == 0 {
		details = json.RawMessage(`{}`)
	}
	res, err := s.db.Exec(`
		INSERT INTO eval_runs
			(task_id, prompt_version, dataset_id, dataset_name, dataset_version, model,
			 total, passed, failed, match_rate, avg_latency_ms, total_cost_usd,
			 details, created_by, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		run.TaskID, run.PromptVersion, run.DatasetID, run.DatasetName, run.DatasetVersion, run.Model,
		run.Total, run.Passed, run.Failed, run.MatchRate, run.AvgLatencyMs, run.TotalCostUSD,
		string(details), run.CreatedBy, fmtTime(run.CreatedAt),
	)
	if err != nil {
		return err
	}
	if id, err := res.LastInsertId(); err == nil {
		run.ID = id
	}
	run.Details = details
	return nil
}

// ListEvalRuns returns recent eval runs for a task.
func (s *Store) ListEvalRuns(taskID string) ([]EvalRun, error) {
	rows, err := s.db.Query(`
		SELECT id, task_id, prompt_version, dataset_id, dataset_name, dataset_version,
		       model, total, passed, failed, match_rate, avg_latency_ms, total_cost_usd,
		       details, created_by, created_at
		FROM eval_runs
		WHERE task_id = ?
		ORDER BY id DESC
		LIMIT 50`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []EvalRun{}
	for rows.Next() {
		var run EvalRun
		var details, createdAt string
		if err := rows.Scan(
			&run.ID, &run.TaskID, &run.PromptVersion, &run.DatasetID, &run.DatasetName,
			&run.DatasetVersion, &run.Model, &run.Total, &run.Passed, &run.Failed,
			&run.MatchRate, &run.AvgLatencyMs, &run.TotalCostUSD, &details,
			&run.CreatedBy, &createdAt,
		); err != nil {
			return nil, err
		}
		run.Details = json.RawMessage(details)
		run.CreatedAt = parseTime(createdAt)
		out = append(out, run)
	}
	return out, rows.Err()
}

func scanEvalDataset(r rowScanner) (*EvalDataset, error) {
	var ds EvalDataset
	var inputMapping, outputMapping, createdAt string
	err := r.Scan(
		&ds.ID, &ds.TaskID, &ds.Name, &ds.Version, &ds.SourceType, &ds.SourceRef,
		&ds.Status, &inputMapping, &outputMapping, &ds.RowCount, &ds.SchemaHash,
		&ds.CreatedBy, &createdAt,
	)
	if err != nil {
		return nil, err
	}
	ds.CreatedAt = parseTime(createdAt)
	ds.InputMapping = map[string]string{}
	ds.OutputMapping = map[string]string{}
	_ = json.Unmarshal([]byte(inputMapping), &ds.InputMapping)
	_ = json.Unmarshal([]byte(outputMapping), &ds.OutputMapping)
	return &ds, nil
}
