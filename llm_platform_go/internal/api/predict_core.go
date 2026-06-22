package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	chimw "github.com/go-chi/chi/v5/middleware"

	"llm_platform_go/internal/auth"
	"llm_platform_go/internal/cache"
	"llm_platform_go/internal/db"
	"llm_platform_go/internal/health"
	"llm_platform_go/internal/llm"
	"llm_platform_go/internal/ratelimit"
	"llm_platform_go/internal/tasks"
	"llm_platform_go/internal/types"

	"github.com/google/uuid"
)

// taskHealthGate adapts the per-(task, model) circuit breaker to llm.HealthGate,
// binding every call to one task id and resolving the provider name for events.
type taskHealthGate struct {
	tracker *health.Tracker
	taskID  string
}

func (g taskHealthGate) Allow(model string) bool {
	ok, _ := g.tracker.Allow(g.taskID, model)
	return ok
}
func (g taskHealthGate) RecordSuccess(model string) {
	g.tracker.RecordSuccess(g.taskID, model, llm.ProviderName(model))
}
func (g taskHealthGate) RecordFailure(model, reason string) {
	g.tracker.RecordFailure(g.taskID, model, llm.ProviderName(model), reason)
}

// predictOptions tweak one prediction execution.
type predictOptions struct {
	isTest          bool                 // Studio test panel — flagged on the run row
	overrideVersion *tasks.PromptVersion // test a specific prompt version instead of the active one
	overrideModel   string               // test a specific model instead of the task's chain
	useCache        bool                 // production predicts only — test/shadow always run fresh
	enforceLimits   bool                 // apply the per-task request/token rate limiter (production predicts)
	// enforceSizeLimits applies the per-task input size guardrails (text length,
	// image size/count). Separate from enforceLimits so the Studio test panel
	// enforces the same size ceilings as production without also burning the
	// task's request/token rate-limit windows on author iteration.
	enforceSizeLimits bool
}

// predictOutcome is everything a prediction produced, shared by the Predict,
// Test, and Shadow endpoints.
type predictOutcome struct {
	RunID         string
	PromptVersion int
	Result        llm.ModelResult
	Output        json.RawMessage // parsed JSON when output schema validates
	OutputValid   *bool           // nil when the task has no output schema
	CacheHit      bool            // served from the prediction cache, no provider call
	// GatewayLatencyMs is the end-to-end wall-clock the platform spent on this
	// prediction: input validation, prompt render, the whole fallback walk
	// (including any failed/skipped models and retries), output validation, and
	// cache work. Result.LatencyMs, by contrast, is only the winning model's
	// call. Gateway ≥ model; the gap is the platform's own overhead + losers.
	GatewayLatencyMs int
}

// limiterError maps a rate-limiter rejection to the right HTTP error: an
// oversized input is a deterministic 413 (retrying won't help — shrink it),
// while a request-rate or token-budget breach is a transient 429 carrying a
// Retry-After hint for when the task's window refills.
func limiterError(d ratelimit.Decision) *AppError {
	switch d.Code {
	case ratelimit.InputTooLarge:
		return PayloadTooLarge(CodeInputTooLarge, "%s", d.Message)
	case ratelimit.RequestRate:
		return TooMany(CodeRateLimited, "%s", d.Message).
			WithRetryAfter(retryAfterSeconds(d.RetryAfter))
	case ratelimit.TokenBudget:
		return TooMany(CodeTokenBudget, "%s", d.Message).
			WithRetryAfter(retryAfterSeconds(d.RetryAfter))
	default:
		return TooMany(CodeRateLimited, "%s", d.Message)
	}
}

// enforceInputLimits applies a task's per-task input size guardrails (set in the
// task UI; 0 = no limit), independent of the global rate limiter. text is the
// full text sent to the model (system + user prompt). Every breach is a
// deterministic 413: retrying won't help, the input has to shrink. Remote image
// URLs are skipped — their size isn't known without fetching them.
func enforceInputLimits(task *tasks.Task, text string, images []string) *AppError {
	if task.MaxPromptChars > 0 {
		if n := utf8.RuneCountInString(text); n > task.MaxPromptChars {
			return PayloadTooLarge(CodeInputTooLarge,
				"text is %d characters, over this task's limit of %d", n, task.MaxPromptChars)
		}
	}
	if task.MaxImages > 0 && len(images) > task.MaxImages {
		return PayloadTooLarge(CodeInputTooLarge,
			"%d images submitted, over this task's limit of %d", len(images), task.MaxImages)
	}
	if task.MaxImageKB > 0 {
		limit := task.MaxImageKB * 1024
		for i, img := range images {
			if n, ok := imageByteSize(img); ok && n > limit {
				return PayloadTooLarge(CodeInputTooLarge,
					"image %d is ~%d KB, over this task's per-image limit of %d KB",
					i+1, (n+1023)/1024, task.MaxImageKB)
			}
		}
	}
	return nil
}

// imageByteSize returns the decoded size in bytes of an inline data-URL image
// and whether it was measurable. The base64 payload size is derived from its
// length (4 encoded chars → 3 bytes, less padding) without allocating to decode
// it. Non-data URLs (e.g. https links) return ok=false: their size is unknown
// without a fetch, so size limits don't apply to them.
func imageByteSize(s string) (int, bool) {
	if !strings.HasPrefix(s, "data:") {
		return 0, false
	}
	comma := strings.IndexByte(s, ',')
	if comma < 0 {
		return 0, false
	}
	meta, payload := s[:comma], s[comma+1:]
	if !strings.Contains(meta, ";base64") {
		// Non-base64 data URL: the payload is the (percent-encoded) bytes as-is.
		return len(payload), true
	}
	n := len(payload) / 4 * 3
	switch {
	case strings.HasSuffix(payload, "=="):
		n -= 2
	case strings.HasSuffix(payload, "="):
		n--
	}
	if n < 0 {
		n = 0
	}
	return n, true
}

// retryAfterSeconds rounds a sub-second window remainder up to at least 1, so a
// Retry-After is never 0 ("retry immediately") when the window hasn't refilled.
func retryAfterSeconds(d time.Duration) int {
	s := int(d / time.Second)
	if d%time.Second > 0 || s == 0 {
		s++
	}
	return s
}

// servedValid reports whether the prediction produced an answer that is safe to
// hand back as the result: the model chain succeeded AND, when the task has an
// output schema, the served output passed it. A schema-invalid output is still
// fully stored (the run row keeps the raw response, the gateway trace keeps
// every model's output), but it is NOT a valid answer — the API surfaces it as
// an error rather than returning invalid data as if it were the result.
func (o *predictOutcome) servedValid() bool {
	return o.Result.Success && (o.OutputValid == nil || *o.OutputValid)
}

// failureReason returns the stable error code and client-facing message for an
// outcome that produced no valid answer, or ("","") when the outcome is fine.
// It distinguishes "no model produced a usable response at all" from "models
// answered but none passed the task's output schema".
func (o *predictOutcome) failureReason() (code, msg string) {
	if o.servedValid() {
		return "", ""
	}
	if !o.Result.Success {
		detail := "no usable model for this task"
		if o.Result.Error != nil {
			detail = *o.Result.Error
		}
		return CodeNoModelAvailable, detail
	}
	return CodeNoValidOutput,
		"no valid response: the model output did not match the task's output schema"
}

// logUpstreamFailure records a 502 (the model chain produced no usable result —
// either no model responded, or none produced schema-valid output) with the
// request id, task, attributed model, and the reason, and echoes the request id
// in the response header — so an upstream failure is as traceable as any other
// error even though the body is the predict response.
func logUpstreamFailure(w http.ResponseWriter, r *http.Request, taskID string, outcome *predictOutcome) {
	reqID := chimw.GetReqID(r.Context())
	if reqID != "" {
		w.Header().Set("X-Request-ID", reqID)
	}
	code, detail := CodeNoModelAvailable, "unknown upstream error"
	model := ""
	if outcome != nil {
		code, detail = outcome.failureReason()
		model = outcome.Result.Model
	}
	slog.Error("upstream prediction failed",
		"request_id", reqID,
		"path", r.URL.Path,
		"task", taskID,
		"model", model,
		"code", code,
		"status", http.StatusBadGateway,
		"error", detail,
	)
}

// executePrediction is the single prediction pipeline:
// validate input → render prompt → call model chain → validate output → log run.
// It never writes to the ResponseWriter — callers shape their own responses. On a
// client-side problem it returns an *AppError (input/prompt validation); a usable
// but failed model chain is reported via the returned outcome, not an error.
func (h *Handler) executePrediction(ctx context.Context, task *tasks.Task, inputs json.RawMessage, user *auth.User, opts predictOptions) (*predictOutcome, *AppError) {
	gatewayStart := time.Now()
	if len(inputs) == 0 {
		return nil, Unprocessable(CodeInputValidation, "inputs is required")
	}
	if err := tasks.ValidateInput(task, inputs); err != nil {
		return nil, Unprocessable(CodeInputValidation, "input validation failed: %s", err.Error())
	}

	var inputMap map[string]any
	if err := json.Unmarshal(inputs, &inputMap); err != nil {
		return nil, Unprocessable(CodeInputValidation, "inputs must be a JSON object: %s", err.Error())
	}

	// Resolve the prompt: active task config, or an override version under test.
	renderTask := task
	promptVersion := task.PromptVersion
	if opts.overrideVersion != nil {
		cp := *task
		cp.PromptTemplate = opts.overrideVersion.PromptTemplate
		cp.SystemPrompt = opts.overrideVersion.SystemPrompt
		renderTask = &cp
		promptVersion = opts.overrideVersion.Version
	}

	prompt, err := tasks.RenderPrompt(renderTask, inputMap)
	if err != nil {
		return nil, Unprocessable(CodeValidationFailed, "prompt render failed: %s", err.Error())
	}

	// Multimodal input: image fields (base64 data URLs or https URLs) are not
	// rendered into the text prompt — the template only gates on them with
	// {{if .image}} / {{if .images}} — but are attached to the user message as
	// image_url blocks for vision models. They must also key the cache (same text
	// + different photos is a different prediction). Both a single "image" string
	// and an "images" array are accepted, in submission order.
	images := collectImages(inputMap, task.InputSchema)

	// Per-task input size limits. Enforced on production predicts and Studio test
	// runs alike (opts.enforceSizeLimits); text and image ceilings are configured
	// independently per task.
	if opts.enforceSizeLimits {
		if herr := enforceInputLimits(task, renderTask.SystemPrompt+"\n"+prompt, images); herr != nil {
			return nil, herr
		}
	}

	messages := []llm.ChatMessage{}
	if renderTask.SystemPrompt != "" {
		messages = append(messages, llm.ChatMessage{Role: "system", Content: renderTask.SystemPrompt})
	}
	userMsg := llm.ChatMessage{Role: "user", Content: prompt}
	if len(images) > 0 {
		userMsg.Images = images
	}
	messages = append(messages, userMsg)

	// Model chain: explicit override (Studio), or primary + fallbacks.
	models := append([]string{task.Model}, task.FallbackModels...)
	if opts.overrideModel != "" {
		models = []string{opts.overrideModel}
	}

	// Prediction cache (single level, keyed per model). The key pins the
	// deployed prompt version, fully rendered prompt, system prompt, sampling
	// params, output schema, and the routing key that produced the answer — but
	// NOT the fallback chain. Routing is live configuration (tasks.Store), read
	// fresh on every prediction, so a chain edit changes which model runs, not
	// which cache entry is consulted.
	//
	// The per-model cache is consulted *during* the fallback walk, as the router
	// reaches each model (modelLookup below). A model's cached answer is used
	// only when this request actually reached it (every higher-priority model
	// was tried live and failed), so a recovered primary is always called rather
	// than shadowed by a stale lower-priority entry. Studio overrides bypass
	// caching entirely.
	cacheable := opts.useCache && h.Cache != nil && task.CacheEnabled &&
		opts.overrideModel == "" && opts.overrideVersion == nil
	base := cache.KeyInputs{
		TaskID:         task.ID,
		PromptVersion:  promptVersion,
		SystemPrompt:   renderTask.SystemPrompt,
		RenderedPrompt: prompt,
		Temperature:    task.Temperature,
		MaxTokens:      task.MaxTokens,
		OutputSchema:   string(task.OutputSchema),
		Images:         images,
	}
	modelKey := func(model string) string { ki := base; ki.Model = model; return cache.Key(ki) }

	// modelLookup is the per-model cache hook handed to the fallback walk. On a
	// hit it captures the entry (for serveCached below, which needs the parsed
	// output/validity) and returns a stop signal so the walk halts at this model.
	var hitEntry *cache.Entry
	var modelLookup llm.ModelCacheLookup
	if cacheable {
		modelLookup = func(model string) (llm.ModelResult, bool) {
			raw, ok := h.Cache.Get(ctx, modelKey(model))
			if !ok {
				return llm.ModelResult{}, false
			}
			var entry cache.Entry
			if err := json.Unmarshal(raw, &entry); err != nil {
				return llm.ModelResult{}, false
			}
			hitEntry = &entry
			resp := entry.RawResponse
			return llm.ModelResult{
				Model: entry.Model, Provider: entry.Provider,
				Response: &resp, Success: true,
			}, true
		}
	}

	// Circuit-breaker health gating + schema-aware fallback apply to production
	// predicts only (the real fallback chain). Studio test panels and shadow
	// runs (useCache=false) or single-model overrides run the model as asked,
	// without skipping it or feeding production health.
	fbOpts := llm.FallbackOptions{Lookup: modelLookup}
	if opts.useCache && opts.overrideModel == "" {
		if h.Health.Enabled() {
			fbOpts.Gate = taskHealthGate{tracker: h.Health, taskID: task.ID}
		}
		if len(task.OutputSchema) > 0 {
			vt := task // capture for the validator closure
			fbOpts.Validate = func(text string) bool {
				_, err := tasks.ValidateOutput(vt, text)
				return err == nil
			}
		}
	}

	// Per-task rate limiter (production predicts only). Estimate the input cost
	// from the rendered system+user prompt and any images, then reserve it: an
	// oversized single input is rejected (413), and the task's per-window request
	// and token budgets are enforced up front (429). The reservation is settled
	// to the tokens actually consumed after the walk — see Reconcile below.
	var reservation ratelimit.Reservation
	if opts.enforceLimits && h.Limiter.Enabled() {
		est := h.Limiter.Estimate(renderTask.SystemPrompt+"\n"+prompt, len(images))
		res, dec := h.Limiter.Reserve(task.ID, est)
		if !dec.Allowed {
			return nil, limiterError(dec)
		}
		reservation = res
	}

	result := llm.CallWithFallbackOpts(ctx, h.Clients, models, messages,
		float32(task.Temperature), task.MaxTokens, fbOpts)

	// Settle the rate-limit reservation against the tokens actually consumed:
	// the sum across every attempt (winner + failed/fallback + schema-invalid),
	// which is the true input+output spend even when the request ultimately
	// failed. A cache hit consumed nothing upstream, so its attempts total ~0.
	if reservation.Active() {
		actualTokens := 0
		for _, a := range result.Attempts {
			actualTokens += a.TotalTokens
		}
		h.Limiter.Reconcile(reservation, actualTokens)
	}

	// One run id ties the answer (runs row) to its full gateway trace (one
	// gateway_attempts row per model the walk touched). Generated before the
	// cache branch so a cache-hit run and its trace share the same id.
	runID := uuid.New().String()
	h.recordAttempts(runID, &task.ID, result.Attempts, opts.isTest)

	// A per-model cache hit during the walk short-circuits the rest of the
	// pipeline (validation/fill/spend) — serveCached replays the stored outcome.
	if hitEntry != nil {
		cached := h.serveCached(runID, task, prompt, renderTask.SystemPrompt, images, promptVersion, user, hitEntry)
		cached.GatewayLatencyMs = int(time.Since(gatewayStart).Milliseconds())
		return cached, nil
	}

	// Output schema validation (flag only; correction retry lands in Phase 2).
	var output json.RawMessage
	var outputValid *bool
	if result.Success && len(task.OutputSchema) > 0 {
		parsed, verr := tasks.ValidateOutput(task, *result.Response)
		valid := verr == nil
		outputValid = &valid
		if valid {
			output = parsed
		}
	}

	// Cache fill — any clean success (schema valid or no schema). Failures and
	// schema-invalid outputs are never cached. One entry, keyed on the serving
	// model, at the task's TTL.
	if cacheable && result.Success && result.Response != nil &&
		(outputValid == nil || *outputValid) {
		entry := cache.Entry{
			Model:        result.Model,
			Provider:     result.Provider,
			RawResponse:  *result.Response,
			Output:       output,
			OutputValid:  outputValid,
			InputTokens:  result.InputTokens,
			OutputTokens: result.OutputTokens,
			TotalTokens:  result.TotalTokens,
			CachedAt:     time.Now().UTC(),
		}
		if b, err := json.Marshal(entry); err == nil {
			modelTTL := time.Duration(task.CacheTTLSeconds) * time.Second
			if modelTTL <= 0 {
				modelTTL = cache.DefaultTTL
			}
			h.Cache.Set(ctx, modelKey(result.Model), b, modelTTL)
		}
	}

	userID, userEmail := user.Subject, user.Email
	var sysPrompt *string
	if renderTask.SystemPrompt != "" {
		sysPrompt = &renderTask.SystemPrompt
	}
	providerName := result.Provider

	h.insertRun(&types.RunRow{
		RunID:         runID,
		Prompt:        prompt,
		SystemPrompt:  sysPrompt,
		Images:        images,
		Model:         result.Model,
		Response:      result.Response,
		LatencyMs:     result.LatencyMs,
		InputTokens:   result.InputTokens,
		OutputTokens:  result.OutputTokens,
		TotalTokens:   result.TotalTokens,
		CostUSD:       result.CostUSD,
		Success:       result.Success,
		Error:         result.Error,
		UserID:        &userID,
		UserEmail:     &userEmail,
		TaskID:        &task.ID,
		PromptVersion: promptVersion,
		Provider:      &providerName,
		FallbackUsed:  result.FallbackUsed,
		IsTest:        opts.isTest,
		CreatedAt:     time.Now().UTC(),
	})

	// Keep the budget gate's spend view current without waiting for the async
	// run write to land (test/shadow spend counts too — real spend is real).
	h.addSpend(task.ID, result.CostUSD)

	return &predictOutcome{
		RunID:            runID,
		PromptVersion:    promptVersion,
		Result:           result,
		Output:           output,
		OutputValid:      outputValid,
		GatewayLatencyMs: int(time.Since(gatewayStart).Milliseconds()),
	}, nil
}

// serveCached shapes a cache hit into a normal prediction outcome. The hit
// still gets a run row (cache_hit=1) so attribution and hit-rate stay
// observable, but with zero cost/tokens — nothing was consumed upstream, and
// the budget gate must not count replayed answers as spend.
func (h *Handler) serveCached(runID string, task *tasks.Task, prompt, systemPrompt string, images []string,
	promptVersion int, user *auth.User, entry *cache.Entry) *predictOutcome {

	response := entry.RawResponse
	// A cached answer from a non-primary model is still a degraded/fallback
	// outcome — keep the fallback_used + X-Platform-Degraded contract honest.
	fellBack := entry.Model != task.Model
	result := llm.ModelResult{
		Model:        entry.Model,
		Provider:     entry.Provider,
		Response:     &response,
		Success:      true,
		FallbackUsed: fellBack,
		Degraded:     fellBack,
	}

	userID, userEmail := user.Subject, user.Email
	var sysPrompt *string
	if systemPrompt != "" {
		sysPrompt = &systemPrompt
	}
	providerName := entry.Provider

	h.insertRun(&types.RunRow{
		RunID:         runID,
		Prompt:        prompt,
		SystemPrompt:  sysPrompt,
		Images:        images,
		Model:         entry.Model,
		Response:      &response,
		Success:       true,
		UserID:        &userID,
		UserEmail:     &userEmail,
		TaskID:        &task.ID,
		PromptVersion: promptVersion,
		Provider:      &providerName,
		CacheHit:      true,
		CreatedAt:     time.Now().UTC(),
	})

	return &predictOutcome{
		RunID:         runID,
		PromptVersion: promptVersion,
		Result:        result,
		Output:        entry.Output,
		OutputValid:   entry.OutputValid,
		CacheHit:      true,
	}
}

// insertRun routes observability writes through the async writer when one is
// configured, falling back to a synchronous insert (tests, simple setups).
// Either way, a failed trace write never fails the prediction.
func (h *Handler) insertRun(row *types.RunRow) {
	if h.Runs != nil {
		h.Runs.Write(row)
		return
	}
	_ = db.InsertRun(h.DB, row)
}

// recordAttempts persists the gateway trace for one run: one gateway_attempts
// row per model the fallback walk touched, in walk order. Like insertRun it
// prefers the async writer and never fails the prediction on a trace error. A
// run with no attempts (e.g. no models configured) writes nothing.
func (h *Handler) recordAttempts(runID string, taskID *string, attempts []llm.Attempt, isTest bool) {
	if len(attempts) == 0 {
		return
	}
	now := time.Now().UTC()
	for _, a := range attempts {
		row := &types.GatewayAttempt{
			RunID:          runID,
			TaskID:         taskID,
			Seq:            a.Seq,
			Model:          a.Model,
			Provider:       a.Provider,
			Outcome:        a.Outcome,
			FallbackUsed:   a.FallbackUsed,
			FallbackReason: a.FallbackReason,
			Response:       a.Response,
			Error:          a.Error,
			HTTPStatus:     a.HTTPStatus,
			InfraFailure:   a.InfraFailure,
			RetryCount:     a.RetryCount,
			LatencyMs:      a.LatencyMs,
			InputTokens:    a.InputTokens,
			OutputTokens:   a.OutputTokens,
			TotalTokens:    a.TotalTokens,
			CostUSD:        a.CostUSD,
			IsTest:         isTest,
			CreatedAt:      now,
		}
		if h.Attempts != nil {
			h.Attempts.Write(row)
		} else {
			_ = db.InsertGatewayAttempt(h.DB, row)
		}
	}
}

// collectImages gathers the multimodal inputs from a request. Image inputs are
// the values of properties the task's input schema types as images (a string or
// an array-of-strings carrying format:"image"), under whatever name the author
// chose. The implicit "image"/"images" names are always honoured too, so tasks
// authored before image fields were typed — and untyped callers — keep working.
// A property may hold a single string or an array; blank/non-string entries are
// dropped rather than failing the call.
func collectImages(inputMap map[string]any, inputSchema json.RawMessage) []string {
	names := imageSchemaFieldNames(inputSchema)
	seen := make(map[string]bool, len(names))
	for _, n := range names {
		seen[n] = true
	}
	for _, legacy := range []string{"image", "images"} {
		if !seen[legacy] {
			names = append(names, legacy)
			seen[legacy] = true
		}
	}

	var out []string
	for _, name := range names {
		switch v := inputMap[name].(type) {
		case string:
			if v != "" {
				out = append(out, v)
			}
		case []any:
			for _, e := range v {
				if s, ok := e.(string); ok && s != "" {
					out = append(out, s)
				}
			}
		}
	}
	return out
}

// imageSchemaFieldNames returns the names of input-schema properties typed as
// images: a string with format:"image", or an array whose items carry it. Names
// are sorted for deterministic ordering (the order images are attached in is
// cosmetic). Returns nil for an absent or unparseable schema.
func imageSchemaFieldNames(inputSchema json.RawMessage) []string {
	if len(inputSchema) == 0 {
		return nil
	}
	var s struct {
		Properties map[string]struct {
			Type   string `json:"type"`
			Format string `json:"format"`
			Items  struct {
				Format string `json:"format"`
			} `json:"items"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(inputSchema, &s); err != nil {
		return nil
	}
	var names []string
	for name, p := range s.Properties {
		if (p.Type == "string" && p.Format == "image") || (p.Type == "array" && p.Items.Format == "image") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// shapePredictResponse converts an outcome into the public predict JSON shape.
// Only a valid answer is returned as the result: if no model produced one
// (every model failed, was unhealthy, or returned schema-invalid output) the
// response carries a stable error_code and message and `output` stays null, so
// callers never receive an invalid response dressed up as the answer. The raw
// response is still echoed for debugging, and everything is persisted regardless.
func shapePredictResponse(task *tasks.Task, o *predictOutcome) predictResponse {
	errorCode, errMsg := o.failureReason()
	errPtr := o.Result.Error
	if errPtr == nil && errMsg != "" {
		errPtr = &errMsg
	}
	return predictResponse{
		TaskRunID:     o.RunID,
		TaskID:        task.ID,
		PromptVersion: o.PromptVersion,
		Model:         o.Result.Model,
		Provider:      o.Result.Provider,
		Output:        o.Output,
		OutputValid:   o.OutputValid,
		RawResponse:   o.Result.Response,
		Error:         errPtr,
		ErrorCode:     errorCode,
		FallbackUsed:  o.Result.FallbackUsed,
		Cached:        o.CacheHit,
		Usage: predictUsage{
			InputTokens:  o.Result.InputTokens,
			OutputTokens: o.Result.OutputTokens,
			TotalTokens:  o.Result.TotalTokens,
			CostUSD:      o.Result.CostUSD,
		},
		LatencyMs:        o.Result.LatencyMs,
		GatewayLatencyMs: o.GatewayLatencyMs,
	}
}
