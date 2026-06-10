package api

import (
	"database/sql"
	"net/http"

	"llm_platform_go/internal/llm"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
)

func NewRouter(db *sql.DB, clients *llm.Clients) http.Handler {
	h := &Handler{DB: db, Clients: clients}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins: []string{"*"},
		AllowedMethods: []string{"GET", "POST", "DELETE", "OPTIONS"},
		AllowedHeaders: []string{"Accept", "Content-Type"},
	}))

	r.Get("/health", h.HealthCheck)
	r.Post("/run", h.RunEndpoint)
	r.Get("/sessions", h.ListSessions)
	r.Get("/sessions/{session_id}", h.GetSession)
	r.Delete("/sessions", h.DeleteSessions)

	return r
}
