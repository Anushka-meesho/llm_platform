package api

import (
	"database/sql"
	"net/http"

	"llm_platform_go/internal/auth"
	"llm_platform_go/internal/cache"
	"llm_platform_go/internal/db"
	"llm_platform_go/internal/health"
	"llm_platform_go/internal/llm"
	"llm_platform_go/internal/ratelimit"
	"llm_platform_go/internal/schema"
	"llm_platform_go/internal/tasks"
	"llm_platform_go/internal/users"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
)

// RouterDeps bundles everything NewRouter needs to wire the handlers.
type RouterDeps struct {
	DB             *sql.DB
	Clients        *llm.Clients
	Users          users.Store
	Tasks          *tasks.Store
	Runs           *db.RunWriter            // optional async observability writer
	Attempts       *db.GatewayAttemptWriter // optional async gateway-trace writer
	Cache          cache.Cache              // optional prediction cache; nil → caching off
	Health         *health.Tracker          // optional per-(task, model) circuit breaker
	Limiter        *ratelimit.Limiter       // optional per-task request/token rate limiter
	Auth           AuthConfig
	AllowedOrigins []string // CORS — the frontend origin(s)
}

func NewRouter(deps RouterDeps) http.Handler {
	h := &Handler{
		DB:       deps.DB,
		Clients:  deps.Clients,
		Users:    deps.Users,
		Tasks:    deps.Tasks,
		Runs:     deps.Runs,
		Attempts: deps.Attempts,
		Cache:    deps.Cache,
		Health:   deps.Health,
		Limiter:  deps.Limiter,
		Auth:     deps.Auth,
	}

	// Request-body schemas (embedded YAML). v(name) is the per-route validation
	// middleware; a bad body is rejected with 422 before the handler runs.
	reg := schema.MustLoadRequests()
	v := func(name string) func(http.Handler) http.Handler { return validateBody(reg, name) }

	origins := deps.AllowedOrigins
	if len(origins) == 0 {
		origins = []string{"http://localhost:5173"}
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Logger)
	r.Use(recoverer) // custom: logs panic + stack with request id, returns the standard envelope
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   origins,
		AllowedMethods:   []string{"GET", "POST", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Content-Type", "Authorization"},
		AllowCredentials: true, // session cookie must be sent cross-origin in dev
	}))

	r.Get("/health", h.HealthCheck)

	// Auth — public (demo SSO stand-in).
	r.Get("/auth/demo-users", h.DemoUsers)
	r.With(v("auth_login")).Post("/auth/login", h.Login)
	r.Post("/auth/logout", h.Logout)

	// Everything else requires a valid session.
	r.Group(func(pr chi.Router) {
		pr.Use(RequireAuth(deps.Auth.Secret, deps.Auth.CookieName))

		// Available to any authenticated principal: auth bootstrap and the
		// Studio playground sample tools (Compare/Estimate/Dashboard). RBAC
		// governs the product task API below, not these UI helpers.
		pr.Get("/auth/me", h.Me)
		pr.Get("/pricing", h.Pricing)
		pr.With(v("run")).Post("/run", h.RunEndpoint)
		pr.Get("/sessions", h.ListSessions)
		pr.Get("/sessions/{session_id}", h.GetSession)
		pr.Get("/sessions/{session_id}/leaderboard", h.GetLeaderboard)
		pr.With(v("delete_sessions")).Delete("/sessions", h.DeleteSessions)
		pr.With(v("feedback")).Post("/feedback", h.Feedback)
		pr.Get("/dashboard", h.Dashboard)

		// Admin prompt history — a cross-tenant view of every user's runs, so
		// it's held to the admin role (RequireAdmin), not a task capability.
		// Static "models" is registered before "{run_id}" so it isn't captured
		// as a run id.
		pr.With(RequireAdmin).Get("/v1/admin/runs", h.AdminListRuns)
		pr.With(RequireAdmin).Get("/v1/admin/runs/models", h.AdminRunModels)
		pr.With(RequireAdmin).Get("/v1/admin/runs/{run_id}", h.AdminGetRun)

		// Per-(task, model) circuit-breaker health: view live states, reset a
		// model to healthy, and read the persisted fallback/health events.
		pr.With(RequireAdmin).Get("/v1/admin/model-health", h.AdminModelHealth)
		pr.With(RequireAdmin).Get("/v1/admin/model-health/events", h.AdminModelHealthEvents)
		pr.With(RequireAdmin, v("model_health_reset")).Post("/v1/admin/model-health/reset", h.AdminResetModelHealth)

		// Product task API — each route gated on the RBAC capability it needs.
		// read: anyone with task access. predict: callers + authors. write:
		// authoring (creator/admin). deploy: the publish gate (approver/admin).
		read := RequirePermission(auth.PermTaskRead)
		predict := RequirePermission(auth.PermTaskPredict)
		write := RequirePermission(auth.PermTaskWrite)
		deploy := RequirePermission(auth.PermTaskDeploy)
		del := RequirePermission(auth.PermTaskDelete)

		// Task-keyed product API (design doc §4). Note: /v1/tasks/runs/{run_id}
		// is registered before /v1/tasks/{task_id} routes so "runs" doesn't
		// match as a task id.
		pr.With(read).Get("/v1/tasks/runs/{run_id}", h.GetTaskRun)
		pr.With(write, v("create_task")).Post("/v1/tasks", h.CreateTask)
		pr.With(read).Get("/v1/tasks", h.ListTasks)
		pr.With(read).Get("/v1/tasks/{task_id}", h.GetTask)
		pr.With(write, v("update_task")).Put("/v1/tasks/{task_id}", h.UpdateTask)
		pr.With(del).Delete("/v1/tasks/{task_id}", h.DeleteTask)
		pr.With(predict, v("predict")).Post("/v1/tasks/{task_id}/predict", h.Predict)

		// Prompt registry + Studio (Phase 1).
		pr.With(read).Get("/v1/tasks/{task_id}/versions", h.ListPromptVersions)
		pr.With(write, v("save_draft_version")).Post("/v1/tasks/{task_id}/versions", h.SaveDraftVersion)
		pr.With(del).Delete("/v1/tasks/{task_id}/versions/{version}", h.DeleteVersion)
		pr.With(deploy, v("deploy_version")).Post("/v1/tasks/{task_id}/deploy", h.DeployVersion)
		pr.With(write, v("test_task")).Post("/v1/tasks/{task_id}/test", h.TestTask)
		pr.With(read).Get("/v1/tasks/{task_id}/stats", h.TaskStats)

		// Eval datasets + prompt-version evaluation (Phase 2 foundation).
		pr.With(read).Get("/v1/tasks/{task_id}/eval-datasets", h.ListEvalDatasets)
		pr.With(write).Post("/v1/tasks/{task_id}/eval-datasets/upload", h.UploadEvalDataset)
		pr.With(write, v("create_prism_eval_dataset")).Post("/v1/tasks/{task_id}/eval-datasets/prism", h.CreatePrismEvalDataset)
		pr.With(write, v("run_eval")).Post("/v1/tasks/{task_id}/versions/{version}/check", h.CheckEvalDataset)
		pr.With(write, v("run_eval")).Post("/v1/tasks/{task_id}/versions/{version}/check.csv", h.DownloadEvalCSV)
		pr.With(write, v("run_eval")).Post("/v1/tasks/{task_id}/versions/{version}/eval", h.RunEval)

		// Shadow comparison harness (Phase 1 success metric).
		pr.With(write, v("shadow_compare")).Post("/v1/shadow/compare", h.ShadowCompare)
		pr.With(read).Get("/v1/shadow/reports", h.ListShadowReports)
	})

	return r
}
