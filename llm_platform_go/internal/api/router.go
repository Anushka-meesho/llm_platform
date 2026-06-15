package api

import (
	"database/sql"
	"net/http"

	"llm_platform_go/internal/cache"
	"llm_platform_go/internal/db"
	"llm_platform_go/internal/llm"
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
	Runs           *db.RunWriter // optional async observability writer
	Cache          cache.Cache   // optional prediction cache; nil → caching off
	Auth           AuthConfig
	AllowedOrigins []string // CORS — the frontend origin(s)
}

func NewRouter(deps RouterDeps) http.Handler {
	h := &Handler{
		DB:      deps.DB,
		Clients: deps.Clients,
		Users:   deps.Users,
		Tasks:   deps.Tasks,
		Runs:    deps.Runs,
		Cache:   deps.Cache,
		Auth:    deps.Auth,
	}

	origins := deps.AllowedOrigins
	if len(origins) == 0 {
		origins = []string{"http://localhost:5173"}
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   origins,
		AllowedMethods:   []string{"GET", "POST", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Content-Type", "Authorization"},
		AllowCredentials: true, // session cookie must be sent cross-origin in dev
	}))

	r.Get("/health", h.HealthCheck)

	// Auth — public (demo SSO stand-in).
	r.Get("/auth/demo-users", h.DemoUsers)
	r.Post("/auth/login", h.Login)
	r.Post("/auth/logout", h.Logout)

	// Everything else requires a valid session.
	r.Group(func(pr chi.Router) {
		pr.Use(RequireAuth(deps.Auth.Secret, deps.Auth.CookieName))

		pr.Get("/auth/me", h.Me)
		pr.Get("/pricing", h.Pricing)
		pr.Post("/run", h.RunEndpoint)
		pr.Get("/sessions", h.ListSessions)
		pr.Get("/sessions/{session_id}", h.GetSession)
		pr.Delete("/sessions", h.DeleteSessions)
		pr.Post("/feedback", h.Feedback)
		pr.Get("/dashboard", h.Dashboard)

		// Task-keyed product API (design doc §4). Note: /v1/tasks/runs/{run_id}
		// is registered before /v1/tasks/{task_id} routes so "runs" doesn't
		// match as a task id.
		pr.Get("/v1/tasks/runs/{run_id}", h.GetTaskRun)
		pr.Post("/v1/tasks", h.CreateTask)
		pr.Get("/v1/tasks", h.ListTasks)
		pr.Get("/v1/tasks/{task_id}", h.GetTask)
		pr.Put("/v1/tasks/{task_id}", h.UpdateTask)
		pr.Post("/v1/tasks/{task_id}/predict", h.Predict)

		// Prompt registry + Studio (Phase 1).
		pr.Get("/v1/tasks/{task_id}/versions", h.ListPromptVersions)
		pr.Post("/v1/tasks/{task_id}/versions", h.SaveDraftVersion)
		pr.Post("/v1/tasks/{task_id}/deploy", h.DeployVersion)
		pr.Post("/v1/tasks/{task_id}/test", h.TestTask)
		pr.Get("/v1/tasks/{task_id}/stats", h.TaskStats)

		// Shadow comparison harness (Phase 1 success metric).
		pr.Post("/v1/shadow/compare", h.ShadowCompare)
		pr.Get("/v1/shadow/reports", h.ListShadowReports)
	})

	return r
}
