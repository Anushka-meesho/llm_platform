package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"

	"llm_platform_go/internal/auth"
	"llm_platform_go/internal/cache"
	"llm_platform_go/internal/db"
	"llm_platform_go/internal/health"
	"llm_platform_go/internal/llm"
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

// logUpstreamFailure records a 502 (the model chain produced no usable result)
// with the request id, task, attributed model, and the provider error, and
// echoes the request id in the response header — so an upstream failure is as
// traceable as any other error even though the body is the predict response.
func logUpstreamFailure(w http.ResponseWriter, r *http.Request, taskID string, outcome *predictOutcome) {
	reqID := chimw.GetReqID(r.Context())
	if reqID != "" {
		w.Header().Set("X-Request-ID", reqID)
	}
	detail := "unknown upstream error"
	model := ""
	if outcome != nil {
		if outcome.Result.Error != nil {
			detail = *outcome.Result.Error
		}
		model = outcome.Result.Model
	}
	slog.Error("upstream prediction failed",
		"request_id", reqID,
		"path", r.URL.Path,
		"task", taskID,
		"model", model,
		"code", CodeUpstreamFailed,
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
	images := collectImages(inputMap)

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

	result := llm.CallWithFallbackOpts(ctx, h.Clients, models, messages,
		float32(task.Temperature), task.MaxTokens, fbOpts)

	// A per-model cache hit during the walk short-circuits the rest of the
	// pipeline (validation/fill/spend) — serveCached replays the stored outcome.
	if hitEntry != nil {
		cached := h.serveCached(task, prompt, renderTask.SystemPrompt, images, promptVersion, user, hitEntry)
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

	runID := uuid.New().String()
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
func (h *Handler) serveCached(task *tasks.Task, prompt, systemPrompt string, images []string,
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

	runID := uuid.New().String()
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

// collectImages gathers the multimodal inputs from a request, in submission
// order: a single "image" string (legacy single-image contract) first, then any
// entries of an "images" array. Blank entries are dropped. Both keys are
// accepted so existing single-image callers keep working alongside multi-image
// ones, and a non-string entry is simply ignored rather than failing the call.
func collectImages(inputMap map[string]any) []string {
	var out []string
	if s, ok := inputMap["image"].(string); ok && s != "" {
		out = append(out, s)
	}
	if arr, ok := inputMap["images"].([]any); ok {
		for _, v := range arr {
			if s, ok := v.(string); ok && s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// shapePredictResponse converts an outcome into the public predict JSON shape.
func shapePredictResponse(task *tasks.Task, o *predictOutcome) predictResponse {
	return predictResponse{
		TaskRunID:     o.RunID,
		TaskID:        task.ID,
		PromptVersion: o.PromptVersion,
		Model:         o.Result.Model,
		Provider:      o.Result.Provider,
		Output:        o.Output,
		OutputValid:   o.OutputValid,
		RawResponse:   o.Result.Response,
		Error:         o.Result.Error,
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
