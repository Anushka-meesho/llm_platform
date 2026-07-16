package api

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"llm_platform_go/internal/auth"
	"llm_platform_go/internal/tasks"

	"github.com/go-chi/chi/v5"
)

const (
	evalMaxUploadRows  = 5000
	evalDefaultRunRows = 50
	evalMaxRunRows     = 200
	evalMaxMismatches  = 20
	evalMaxOutputItems = 20
)

type evalValidationError struct {
	Row     int    `json:"row"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

type evalRunRequest struct {
	DatasetID int64  `json:"dataset_id"`
	MaxItems  int    `json:"max_items,omitempty"`
	Model     string `json:"model,omitempty"`
}

type evalRowResult struct {
	Item         int
	RowNo        int
	Inputs       any
	Expected     any
	Actual       any
	Error        string
	Matched      bool
	MismatchKeys []string
	Model        string
	LatencyMs    int
	CostUSD      float64
}

// GET /v1/tasks/{task_id}/eval-datasets
func (h *Handler) ListEvalDatasets(w http.ResponseWriter, r *http.Request) {
	task, ok := h.resolveTask(w, r)
	if !ok {
		return
	}
	datasets, err := h.Tasks.ListEvalDatasets(task.ID)
	if err != nil {
		writeErr(w, r, Internal(CodeDBError, "list eval datasets").WithCause(err))
		return
	}
	runs, err := h.Tasks.ListEvalRuns(task.ID)
	if err != nil {
		writeErr(w, r, Internal(CodeDBError, "list eval runs").WithCause(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"task_id":  task.ID,
		"datasets": datasets,
		"runs":     runs,
	})
}

// POST /v1/tasks/{task_id}/eval-datasets/upload
func (h *Handler) UploadEvalDataset(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	task, ok := h.resolveTask(w, r)
	if !ok {
		return
	}

	if err := r.ParseMultipartForm(16 << 20); err != nil {
		writeErr(w, r, Unprocessable(CodeInvalidBody, "invalid multipart form: %s", err.Error()))
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		writeErr(w, r, Unprocessable(CodeValidationFailed, "dataset name is required"))
		return
	}

	inputMapping, err := parseStringMap(r.FormValue("input_mapping"))
	if err != nil {
		writeErr(w, r, Unprocessable(CodeValidationFailed, "input_mapping must be a JSON object of schema field to CSV column"))
		return
	}
	outputMapping, err := parseStringMap(r.FormValue("output_mapping"))
	if err != nil {
		writeErr(w, r, Unprocessable(CodeValidationFailed, "output_mapping must be a JSON object of schema field to CSV column"))
		return
	}
	if len(inputMapping) == 0 {
		writeErr(w, r, Unprocessable(CodeValidationFailed, "input_mapping is required"))
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, r, Unprocessable(CodeInvalidBody, "file is required"))
		return
	}
	defer file.Close()
	examples, rowErrors, err := parseEvalUpload(task, file, header, inputMapping, outputMapping)
	if err != nil {
		writeErr(w, r, Unprocessable(CodeDatasetValidation, "%s", err.Error()))
		return
	}
	if len(rowErrors) > 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"code":   CodeDatasetValidation,
			"detail": "dataset validation failed",
			"errors": rowErrors,
		})
		return
	}
	version, err := h.Tasks.NextEvalDatasetVersion(task.ID, name)
	if err != nil {
		writeErr(w, r, Internal(CodeDBError, "assign dataset version").WithCause(err))
		return
	}
	ds := &tasks.EvalDataset{
		TaskID:        task.ID,
		Name:          name,
		Version:       version,
		SourceType:    evalSourceType(header.Filename),
		SourceRef:     header.Filename,
		Status:        "ready",
		InputMapping:  inputMapping,
		OutputMapping: outputMapping,
		SchemaHash:    taskSchemaHash(task),
		CreatedBy:     user.Subject,
	}
	if err := h.Tasks.CreateEvalDataset(ds, examples); err != nil {
		writeErr(w, r, Internal(CodeDBError, "create eval dataset").WithCause(err))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"dataset": ds,
		"errors":  []evalValidationError{},
	})
}

// POST /v1/tasks/{task_id}/eval-datasets/prism
func (h *Handler) CreatePrismEvalDataset(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	task, ok := h.resolveTask(w, r)
	if !ok {
		return
	}
	var req struct {
		Name          string            `json:"name"`
		SQL           string            `json:"sql"`
		InputMapping  map[string]string `json:"input_mapping"`
		OutputMapping map[string]string `json:"output_mapping"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, r, Unprocessable(CodeInvalidBody, "invalid request body: %s", err.Error()))
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.SQL = strings.TrimSpace(req.SQL)
	if req.Name == "" || req.SQL == "" {
		writeErr(w, r, Unprocessable(CodeValidationFailed, "name and sql are required"))
		return
	}
	if len(req.InputMapping) == 0 {
		writeErr(w, r, Unprocessable(CodeValidationFailed, "input_mapping is required"))
		return
	}

	version, err := h.Tasks.NextEvalDatasetVersion(task.ID, req.Name)
	if err != nil {
		writeErr(w, r, Internal(CodeDBError, "assign dataset version").WithCause(err))
		return
	}
	ds := &tasks.EvalDataset{
		TaskID:        task.ID,
		Name:          req.Name,
		Version:       version,
		SourceType:    "prism_sql",
		SourceRef:     req.SQL,
		Status:        "pending_import",
		InputMapping:  req.InputMapping,
		OutputMapping: req.OutputMapping,
		SchemaHash:    taskSchemaHash(task),
		CreatedBy:     user.Subject,
	}
	if err := h.Tasks.CreateEvalDataset(ds, nil); err != nil {
		writeErr(w, r, Internal(CodeDBError, "create Prism eval dataset").WithCause(err))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"dataset": ds,
		"detail":  "Prism SQL source registered. Import execution is pending Prism connector integration.",
	})
}

// POST /v1/tasks/{task_id}/versions/{version}/eval
func (h *Handler) RunEval(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	task, ok := h.resolveTask(w, r)
	if !ok {
		return
	}
	version, err := strconv.Atoi(chi.URLParam(r, "version"))
	if err != nil || version <= 0 {
		writeErr(w, r, Unprocessable(CodeValidationFailed, "version must be a positive integer"))
		return
	}
	var req evalRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, r, Unprocessable(CodeInvalidBody, "invalid request body: %s", err.Error()))
		return
	}
	if req.DatasetID <= 0 {
		writeErr(w, r, Unprocessable(CodeValidationFailed, "dataset_id is required"))
		return
	}
	if req.MaxItems <= 0 {
		req.MaxItems = evalDefaultRunRows
	}
	if req.MaxItems > evalMaxRunRows {
		writeErr(w, r, Unprocessable(CodeValidationFailed, "max_items cannot exceed %d", evalMaxRunRows))
		return
	}

	ds, err := h.Tasks.GetEvalDataset(task.ID, req.DatasetID)
	if errors.Is(err, tasks.ErrDatasetNotFound) {
		writeErr(w, r, NotFound(CodeDatasetNotFound, "eval dataset not found"))
		return
	}
	if err != nil {
		writeErr(w, r, Internal(CodeDBError, "load eval dataset").WithCause(err))
		return
	}
	if ds.Status != "ready" {
		writeErr(w, r, Conflict(CodeValidationFailed, "dataset is %s and has no imported examples yet", ds.Status))
		return
	}
	examples, err := h.Tasks.ListEvalExamples(task.ID, ds.ID, req.MaxItems)
	if err != nil {
		writeErr(w, r, Internal(CodeDBError, "load eval examples").WithCause(err))
		return
	}
	if len(examples) == 0 {
		writeErr(w, r, Unprocessable(CodeValidationFailed, "dataset has no examples to evaluate"))
		return
	}

	opts := predictOptions{isTest: true, overrideModel: req.Model}
	if version != task.PromptVersion {
		v, err := h.Tasks.GetVersion(task.ID, version)
		if errors.Is(err, tasks.ErrVersionNotFound) {
			writeErr(w, r, NotFound(CodeVersionNotFound, "version not found"))
			return
		}
		if err != nil {
			writeErr(w, r, Internal(CodeDBError, "load version").WithCause(err))
			return
		}
		opts.overrideVersion = v
	}

	run := h.executeEvalRun(r, task, user, ds, examples, version, opts)
	if err := h.Tasks.RecordEvalRun(run); err != nil {
		writeErr(w, r, Internal(CodeDBError, "record eval run").WithCause(err))
		return
	}
	writeJSON(w, http.StatusOK, run)
}

// POST /v1/tasks/{task_id}/versions/{version}/check
func (h *Handler) CheckEvalDataset(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	task, ok := h.resolveTask(w, r)
	if !ok {
		return
	}
	version, ds, examples, opts, ok := h.prepareEvalRequest(w, r, task)
	if !ok {
		return
	}
	run, _ := h.executeEvalRows(r, task, user, ds, examples, version, opts)
	writeJSON(w, http.StatusOK, run)
}

// POST /v1/tasks/{task_id}/versions/{version}/check.csv
func (h *Handler) DownloadEvalCSV(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	task, ok := h.resolveTask(w, r)
	if !ok {
		return
	}
	version, ds, examples, opts, ok := h.prepareEvalRequest(w, r, task)
	if !ok {
		return
	}
	run, rows := h.executeEvalRows(r, task, user, ds, examples, version, opts)
	filename := fmt.Sprintf("%s-v%d-eval-check.csv", ds.Name, ds.Version)
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, sanitizeFilename(filename)))
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{
		"row_no", "matched", "input_json", "expected_output_json", "actual_output_json", "error",
		"mismatch_fields", "model", "prompt_version", "dataset_name", "dataset_version", "latency_ms", "cost_usd",
	})
	for _, row := range rows {
		_ = cw.Write([]string{
			strconv.Itoa(row.RowNo),
			strconv.FormatBool(row.Matched),
			jsonString(row.Inputs),
			jsonString(row.Expected),
			jsonString(row.Actual),
			row.Error,
			strings.Join(row.MismatchKeys, "|"),
			row.Model,
			strconv.Itoa(run.PromptVersion),
			run.DatasetName,
			strconv.Itoa(run.DatasetVersion),
			strconv.Itoa(row.LatencyMs),
			strconv.FormatFloat(row.CostUSD, 'f', 8, 64),
		})
	}
	cw.Flush()
}

func (h *Handler) prepareEvalRequest(w http.ResponseWriter, r *http.Request, task *tasks.Task) (int, *tasks.EvalDataset, []tasks.EvalExample, predictOptions, bool) {
	version, err := strconv.Atoi(chi.URLParam(r, "version"))
	if err != nil || version <= 0 {
		writeErr(w, r, Unprocessable(CodeValidationFailed, "version must be a positive integer"))
		return 0, nil, nil, predictOptions{}, false
	}
	var req evalRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, r, Unprocessable(CodeInvalidBody, "invalid request body: %s", err.Error()))
		return 0, nil, nil, predictOptions{}, false
	}
	if req.DatasetID <= 0 {
		writeErr(w, r, Unprocessable(CodeValidationFailed, "dataset_id is required"))
		return 0, nil, nil, predictOptions{}, false
	}
	if req.MaxItems <= 0 {
		req.MaxItems = evalDefaultRunRows
	}
	if req.MaxItems > evalMaxRunRows {
		writeErr(w, r, Unprocessable(CodeValidationFailed, "max_items cannot exceed %d", evalMaxRunRows))
		return 0, nil, nil, predictOptions{}, false
	}
	ds, err := h.Tasks.GetEvalDataset(task.ID, req.DatasetID)
	if errors.Is(err, tasks.ErrDatasetNotFound) {
		writeErr(w, r, NotFound(CodeDatasetNotFound, "eval dataset not found"))
		return 0, nil, nil, predictOptions{}, false
	}
	if err != nil {
		writeErr(w, r, Internal(CodeDBError, "load eval dataset").WithCause(err))
		return 0, nil, nil, predictOptions{}, false
	}
	if ds.Status != "ready" {
		writeErr(w, r, Conflict(CodeValidationFailed, "dataset is %s and has no imported examples yet", ds.Status))
		return 0, nil, nil, predictOptions{}, false
	}
	examples, err := h.Tasks.ListEvalExamples(task.ID, ds.ID, req.MaxItems)
	if err != nil {
		writeErr(w, r, Internal(CodeDBError, "load eval examples").WithCause(err))
		return 0, nil, nil, predictOptions{}, false
	}
	if len(examples) == 0 {
		writeErr(w, r, Unprocessable(CodeValidationFailed, "dataset has no examples to evaluate"))
		return 0, nil, nil, predictOptions{}, false
	}
	opts := predictOptions{isTest: true, overrideModel: req.Model}
	if version != task.PromptVersion {
		v, err := h.Tasks.GetVersion(task.ID, version)
		if errors.Is(err, tasks.ErrVersionNotFound) {
			writeErr(w, r, NotFound(CodeVersionNotFound, "version not found"))
			return 0, nil, nil, predictOptions{}, false
		}
		if err != nil {
			writeErr(w, r, Internal(CodeDBError, "load version").WithCause(err))
			return 0, nil, nil, predictOptions{}, false
		}
		opts.overrideVersion = v
	}
	return version, ds, examples, opts, true
}

func parseStringMap(raw string) (map[string]string, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string]string{}, nil
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for k, v := range parsed {
		key := strings.TrimSpace(k)
		value := strings.TrimSpace(v)
		if key == "" || value == "" {
			return nil, fmt.Errorf("mapping keys and values must be non-empty")
		}
		out[key] = value
	}
	return out, nil
}

func parseEvalUpload(task *tasks.Task, file multipart.File, header *multipart.FileHeader, inputMapping, outputMapping map[string]string) ([]tasks.EvalExample, []evalValidationError, error) {
	ext := strings.ToLower(filepath.Ext(header.Filename))
	switch ext {
	case ".csv":
		return parseEvalCSV(task, file, inputMapping, outputMapping)
	case ".xlsx":
		records, err := readXLSXRecords(file)
		if err != nil {
			return nil, nil, err
		}
		return parseEvalRecords(task, records, inputMapping, outputMapping, "XLSX")
	default:
		return nil, nil, fmt.Errorf("only CSV and XLSX uploads are supported")
	}
}

func evalSourceType(filename string) string {
	if strings.ToLower(filepath.Ext(filename)) == ".xlsx" {
		return "xlsx"
	}
	return "csv"
}

func parseEvalCSV(task *tasks.Task, file multipart.File, inputMapping, outputMapping map[string]string) ([]tasks.EvalExample, []evalValidationError, error) {
	reader := csv.NewReader(file)
	reader.TrimLeadingSpace = true
	records, err := reader.ReadAll()
	if err != nil {
		return nil, nil, fmt.Errorf("read CSV: %w", err)
	}
	return parseEvalRecords(task, records, inputMapping, outputMapping, "CSV")
}

func parseEvalRecords(task *tasks.Task, records [][]string, inputMapping, outputMapping map[string]string, sourceLabel string) ([]tasks.EvalExample, []evalValidationError, error) {
	if len(records) == 0 {
		return nil, nil, fmt.Errorf("%s contains no header row", sourceLabel)
	}
	header := records[0]
	columns := map[string]int{}
	for i, h := range header {
		columns[strings.TrimSpace(h)] = i
	}
	if missing := missingColumns(columns, inputMapping, outputMapping); len(missing) > 0 {
		return nil, nil, fmt.Errorf("missing %s columns: %s", sourceLabel, strings.Join(missing, ", "))
	}

	inputTypes := schemaPropertyTypes(task.InputSchema)
	outputTypes := schemaPropertyTypes(task.OutputSchema)
	examples := []tasks.EvalExample{}
	rowErrors := []evalValidationError{}
	for i, record := range records[1:] {
		rowNo := i + 2
		if len(examples) >= evalMaxUploadRows {
			return nil, nil, fmt.Errorf("too many rows (max %d)", evalMaxUploadRows)
		}
		inputs, errs := buildMappedObject(record, columns, inputMapping, inputTypes)
		for _, e := range errs {
			e.Row = rowNo
			rowErrors = append(rowErrors, e)
		}
		expected, errs := buildMappedObject(record, columns, outputMapping, outputTypes)
		for _, e := range errs {
			e.Row = rowNo
			rowErrors = append(rowErrors, e)
		}
		inputRaw, _ := json.Marshal(inputs)
		expectedRaw, _ := json.Marshal(expected)
		if err := tasks.ValidateInput(task, inputRaw); err != nil {
			rowErrors = append(rowErrors, evalValidationError{Row: rowNo, Field: "inputs", Message: err.Error()})
		}
		if err := tasks.ValidateExpectedOutput(task, expectedRaw); err != nil {
			rowErrors = append(rowErrors, evalValidationError{Row: rowNo, Field: "expected_output", Message: err.Error()})
		}
		examples = append(examples, tasks.EvalExample{
			ExampleID:      cellByName(record, columns, "example_id"),
			RowNo:          rowNo,
			Inputs:         inputRaw,
			ExpectedOutput: expectedRaw,
			Metadata:       json.RawMessage(`{}`),
		})
	}
	if len(examples) == 0 && len(rowErrors) == 0 {
		return nil, nil, fmt.Errorf("%s contains no data rows", sourceLabel)
	}
	return examples, rowErrors, nil
}

func readXLSXRecords(file multipart.File) ([][]string, error) {
	size, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, fmt.Errorf("read XLSX size: %w", err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind XLSX: %w", err)
	}
	zr, err := zip.NewReader(file, size)
	if err != nil {
		return nil, fmt.Errorf("open XLSX: %w", err)
	}
	shared, err := readXLSXSharedStrings(zr)
	if err != nil {
		return nil, err
	}
	sheet, err := readZipFile(zr, "xl/worksheets/sheet1.xml")
	if err != nil {
		return nil, fmt.Errorf("read first XLSX sheet: %w", err)
	}
	records, err := parseXLSXSheet(sheet, shared)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("XLSX first sheet contains no rows")
	}
	return records, nil
}

func readXLSXSharedStrings(zr *zip.Reader) ([]string, error) {
	raw, err := readZipFile(zr, "xl/sharedStrings.xml")
	if err != nil {
		return []string{}, nil
	}
	var doc struct {
		Items []struct {
			Texts []string `xml:"t"`
		} `xml:"si"`
	}
	if err := xml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse XLSX shared strings: %w", err)
	}
	out := make([]string, 0, len(doc.Items))
	for _, item := range doc.Items {
		out = append(out, strings.Join(item.Texts, ""))
	}
	return out, nil
}

func readZipFile(zr *zip.Reader, name string) ([]byte, error) {
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(rc)
	}
	return nil, fmt.Errorf("%s not found", name)
}

func parseXLSXSheet(raw []byte, shared []string) ([][]string, error) {
	var sheet struct {
		Rows []struct {
			Cells []struct {
				Ref       string `xml:"r,attr"`
				Type      string `xml:"t,attr"`
				Value     string `xml:"v"`
				InlineStr struct {
					Text string `xml:"t"`
				} `xml:"is"`
			} `xml:"c"`
		} `xml:"sheetData>row"`
	}
	if err := xml.Unmarshal(raw, &sheet); err != nil {
		return nil, fmt.Errorf("parse XLSX sheet: %w", err)
	}
	records := [][]string{}
	for _, row := range sheet.Rows {
		record := []string{}
		for _, cell := range row.Cells {
			col := excelColumnIndex(cell.Ref)
			for len(record) <= col {
				record = append(record, "")
			}
			record[col] = xlsxCellValue(cell.Type, cell.Value, cell.InlineStr.Text, shared)
		}
		records = append(records, record)
	}
	return records, nil
}

var excelCellRef = regexp.MustCompile(`^[A-Z]+`)

func excelColumnIndex(ref string) int {
	letters := excelCellRef.FindString(strings.ToUpper(ref))
	if letters == "" {
		return 0
	}
	idx := 0
	for _, ch := range letters {
		idx = idx*26 + int(ch-'A'+1)
	}
	return idx - 1
}

func xlsxCellValue(cellType, value, inline string, shared []string) string {
	switch cellType {
	case "s":
		idx, err := strconv.Atoi(strings.TrimSpace(value))
		if err == nil && idx >= 0 && idx < len(shared) {
			return shared[idx]
		}
	case "inlineStr":
		return inline
	}
	return value
}

func missingColumns(columns map[string]int, mappings ...map[string]string) []string {
	missingSet := map[string]bool{}
	for _, mapping := range mappings {
		for _, col := range mapping {
			if _, ok := columns[col]; !ok {
				missingSet[col] = true
			}
		}
	}
	missing := make([]string, 0, len(missingSet))
	for col := range missingSet {
		missing = append(missing, col)
	}
	sort.Strings(missing)
	return missing
}

func buildMappedObject(record []string, columns map[string]int, mapping, typesByField map[string]string) (map[string]any, []evalValidationError) {
	out := map[string]any{}
	errs := []evalValidationError{}
	for field, col := range mapping {
		raw := cellByName(record, columns, col)
		value, err := coerceCSVCell(raw, typesByField[field])
		if err != nil {
			errs = append(errs, evalValidationError{Field: field, Message: err.Error()})
			continue
		}
		out[field] = value
	}
	return out, errs
}

func cellByName(record []string, columns map[string]int, name string) string {
	idx, ok := columns[name]
	if !ok || idx >= len(record) {
		return ""
	}
	return record[idx]
}

func coerceCSVCell(raw, typ string) (any, error) {
	trimmed := strings.TrimSpace(raw)
	switch typ {
	case "integer":
		v, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("expected integer, got %q", raw)
		}
		return v, nil
	case "number":
		v, err := strconv.ParseFloat(trimmed, 64)
		if err != nil {
			return nil, fmt.Errorf("expected number, got %q", raw)
		}
		return v, nil
	case "boolean":
		v, err := strconv.ParseBool(strings.ToLower(trimmed))
		if err != nil {
			return nil, fmt.Errorf("expected boolean, got %q", raw)
		}
		return v, nil
	case "object", "array":
		var v any
		if err := json.Unmarshal([]byte(trimmed), &v); err != nil {
			return nil, fmt.Errorf("expected JSON %s, got %q", typ, raw)
		}
		return v, nil
	default:
		return raw, nil
	}
}

func schemaPropertyTypes(raw json.RawMessage) map[string]string {
	out := map[string]string{}
	var doc struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return out
	}
	for field, prop := range doc.Properties {
		var meta struct {
			Type any `json:"type"`
		}
		if err := json.Unmarshal(prop, &meta); err != nil {
			continue
		}
		if typ, ok := meta.Type.(string); ok {
			out[field] = typ
		}
	}
	return out
}

func taskSchemaHash(task *tasks.Task) string {
	sum := sha256.Sum256(append(append([]byte{}, task.InputSchema...), task.OutputSchema...))
	return hex.EncodeToString(sum[:])
}

func (h *Handler) executeEvalRun(
	r *http.Request,
	task *tasks.Task,
	user *auth.User,
	ds *tasks.EvalDataset,
	examples []tasks.EvalExample,
	version int,
	opts predictOptions,
) *tasks.EvalRun {
	run, _ := h.executeEvalRows(r, task, user, ds, examples, version, opts)
	return run
}

func (h *Handler) executeEvalRows(
	r *http.Request,
	task *tasks.Task,
	user *auth.User,
	ds *tasks.EvalDataset,
	examples []tasks.EvalExample,
	version int,
	opts predictOptions,
) (*tasks.EvalRun, []evalRowResult) {
	run := &tasks.EvalRun{
		TaskID:         task.ID,
		PromptVersion:  version,
		DatasetID:      ds.ID,
		DatasetName:    ds.Name,
		DatasetVersion: ds.Version,
		Model:          opts.overrideModel,
		Total:          len(examples),
		CreatedBy:      user.Subject,
		CreatedAt:      time.Now().UTC(),
	}
	var totalFields, matchedFields, latencySum int
	rows := make([]evalRowResult, 0, len(examples))

	for i, ex := range examples {
		row := evalRowResult{
			Item:     i,
			RowNo:    ex.RowNo,
			Inputs:   decodeRawJSON(ex.Inputs),
			Expected: decodeRawJSON(ex.ExpectedOutput),
		}
		outcome, herr := h.executePrediction(r.Context(), task, ex.Inputs, user, opts)
		if herr != nil || outcome == nil || !outcome.Result.Success || outcome.Output == nil {
			run.Failed++
			reason := "prediction failed"
			if herr != nil {
				reason = herr.Message
			} else if outcome != nil && outcome.Result.Error != nil {
				reason = *outcome.Result.Error
			}
			if outcome != nil {
				latencySum += outcome.Result.LatencyMs
				run.TotalCostUSD += outcome.Result.CostUSD
				row.LatencyMs = outcome.Result.LatencyMs
				row.CostUSD = outcome.Result.CostUSD
				row.Model = outcome.Result.Model
				if run.Model == "" {
					run.Model = outcome.Result.Model
				}
			}
			row.Error = reason
			row.MismatchKeys = []string{"(prediction)"}
			rows = append(rows, row)
			continue
		}

		latencySum += outcome.Result.LatencyMs
		run.TotalCostUSD += outcome.Result.CostUSD
		row.LatencyMs = outcome.Result.LatencyMs
		row.CostUSD = outcome.Result.CostUSD
		row.Model = outcome.Result.Model
		row.Actual = decodeRawJSON(outcome.Output)
		if run.Model == "" {
			run.Model = outcome.Result.Model
		}
		expected := flattenJSON("", ex.ExpectedOutput)
		actual := flattenJSON("", outcome.Output)
		totalFields += len(expected)
		itemMatched := 0
		for field, want := range expected {
			got, present := actual[field]
			if present && reflect.DeepEqual(want, got) {
				itemMatched++
				continue
			}
			if !present {
				got = nil
			}
			row.MismatchKeys = append(row.MismatchKeys, field)
		}
		matchedFields += itemMatched
		matched := len(expected) > 0 && itemMatched == len(expected)
		row.Matched = matched
		if matched {
			run.Passed++
		} else {
			run.Failed++
		}
		sort.Strings(row.MismatchKeys)
		rows = append(rows, row)
	}
	if totalFields > 0 {
		run.MatchRate = float64(matchedFields) / float64(totalFields)
	}
	if len(examples) > 0 {
		run.AvgLatencyMs = float64(latencySum) / float64(len(examples))
	}
	run.Details = buildEvalDetails(rows, evalMaxOutputItems, map[string]any{
		"matched_fields": matchedFields,
		"total_fields":   totalFields,
	})
	return run, rows
}

func buildEvalDetails(rows []evalRowResult, sampleLimit int, extra map[string]any) json.RawMessage {
	mismatches := []shadowMismatch{}
	outputSamples := []map[string]any{}
	for _, row := range rows {
		if len(outputSamples) < sampleLimit {
			outputSamples = append(outputSamples, map[string]any{
				"item":            row.Item,
				"row_no":          row.RowNo,
				"inputs":          row.Inputs,
				"expected_output": row.Expected,
				"actual_output":   row.Actual,
				"error":           row.Error,
				"matched":         row.Matched,
				"mismatch_fields": row.MismatchKeys,
			})
		}
		for _, field := range row.MismatchKeys {
			if len(mismatches) >= evalMaxMismatches {
				break
			}
			mismatches = append(mismatches, shadowMismatch{
				Item: row.Item, Field: field, Expected: row.Expected, Got: row.Actual,
			})
		}
	}
	detailMap := map[string]any{
		"mismatches":     mismatches,
		"output_samples": outputSamples,
	}
	for k, v := range extra {
		detailMap[k] = v
	}
	details, _ := json.Marshal(map[string]any{
		"mismatches":     detailMap["mismatches"],
		"output_samples": detailMap["output_samples"],
		"matched_fields": detailMap["matched_fields"],
		"total_fields":   detailMap["total_fields"],
	})
	return details
}

func decodeRawJSON(raw json.RawMessage) any {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	return v
}

func jsonString(value any) string {
	if value == nil {
		return ""
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(raw)
}

func sanitizeFilename(name string) string {
	replacer := strings.NewReplacer("/", "-", "\\", "-", `"`, "", "\n", "-", "\r", "-")
	return replacer.Replace(name)
}
