package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"llm_platform_go/internal/auth"
	"llm_platform_go/internal/cache"
	"llm_platform_go/internal/db"
	"llm_platform_go/internal/llm"
	"llm_platform_go/internal/tasks"
	"llm_platform_go/internal/types"

	"github.com/google/uuid"
)

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
}

// httpError carries an HTTP status + detail out of executePrediction.
type httpError struct {
	status int
	detail string
}

// executePrediction is the single prediction pipeline:
// validate input → render prompt → call model chain → validate output → log run.
// It never writes to the ResponseWriter — callers shape their own responses.
func (h *Handler) executePrediction(ctx context.Context, task *tasks.Task, inputs json.RawMessage, user *auth.User, opts predictOptions) (*predictOutcome, *httpError) {
	if len(inputs) == 0 {
		return nil, &httpError{http.StatusUnprocessableEntity, "inputs is required"}
	}
	if err := tasks.ValidateInput(task, inputs); err != nil {
		return nil, &httpError{http.StatusUnprocessableEntity, "input validation failed: " + err.Error()}
	}

	var inputMap map[string]any
	if err := json.Unmarshal(inputs, &inputMap); err != nil {
		return nil, &httpError{http.StatusUnprocessableEntity, "inputs must be a JSON object: " + err.Error()}
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
		return nil, &httpError{http.StatusUnprocessableEntity, err.Error()}
	}

	messages := []llm.ChatMessage{}
	if renderTask.SystemPrompt != "" {
		messages = append(messages, llm.ChatMessage{Role: "system", Content: renderTask.SystemPrompt})
	}
	messages = append(messages, llm.ChatMessage{Role: "user", Content: prompt})

	// Prediction cache lookup. The key pins everything that determined the
	// output at call time: deployed prompt version, the fully rendered prompt
	// (template + every input/context value), system prompt, primary model,
	// sampling params, and output schema — identical request state or no hit.
	// Studio overrides bypass the cache entirely.
	cacheable := opts.useCache && h.Cache != nil && task.CacheEnabled &&
		opts.overrideModel == "" && opts.overrideVersion == nil
	var cacheKey string
	if cacheable {
		cacheKey = cache.Key(cache.KeyInputs{
			TaskID:         task.ID,
			PromptVersion:  promptVersion,
			Model:          task.Model,
			SystemPrompt:   renderTask.SystemPrompt,
			RenderedPrompt: prompt,
			Temperature:    task.Temperature,
			MaxTokens:      task.MaxTokens,
			OutputSchema:   string(task.OutputSchema),
		})
		if raw, ok := h.Cache.Get(ctx, cacheKey); ok {
			var entry cache.Entry
			if err := json.Unmarshal(raw, &entry); err == nil {
				return h.serveCached(task, prompt, renderTask.SystemPrompt,
					promptVersion, user, &entry), nil
			}
		}
	}

	// Model chain: explicit override (Studio), or primary + fallbacks.
	models := append([]string{task.Model}, task.FallbackModels...)
	if opts.overrideModel != "" {
		models = []string{opts.overrideModel}
	}

	result := llm.CallWithFallback(ctx, h.Clients, models, messages,
		float32(task.Temperature), task.MaxTokens)

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

	// Cache fill — only clean outcomes: primary model served it (a fallback
	// answer must not be replayed once the primary recovers) and the output
	// passed schema validation (or the task has no schema).
	if cacheable && result.Success && !result.FallbackUsed && !result.Degraded &&
		result.Response != nil && (outputValid == nil || *outputValid) {
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
			ttl := time.Duration(task.CacheTTLSeconds) * time.Second
			if ttl <= 0 {
				ttl = cache.DefaultTTL
			}
			h.Cache.Set(ctx, cacheKey, b, ttl)
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
		RunID:         runID,
		PromptVersion: promptVersion,
		Result:        result,
		Output:        output,
		OutputValid:   outputValid,
	}, nil
}

// serveCached shapes a cache hit into a normal prediction outcome. The hit
// still gets a run row (cache_hit=1) so attribution and hit-rate stay
// observable, but with zero cost/tokens — nothing was consumed upstream, and
// the budget gate must not count replayed answers as spend.
func (h *Handler) serveCached(task *tasks.Task, prompt, systemPrompt string,
	promptVersion int, user *auth.User, entry *cache.Entry) *predictOutcome {

	response := entry.RawResponse
	result := llm.ModelResult{
		Model:    entry.Model,
		Provider: entry.Provider,
		Response: &response,
		Success:  true,
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
		LatencyMs: o.Result.LatencyMs,
	}
}
