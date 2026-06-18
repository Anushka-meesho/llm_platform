package main

import (
	"log"
	"net/http"
	"os"
	"strings"

	"llm_platform_go/internal/api"
	"llm_platform_go/internal/cache"
	"llm_platform_go/internal/config"
	"llm_platform_go/internal/db"
	"llm_platform_go/internal/health"
	"llm_platform_go/internal/llm"
	"llm_platform_go/internal/tasks"
	"llm_platform_go/internal/users"

	"github.com/joho/godotenv"
)

func main() {
	// Load .env — ignore error so the binary works with real env vars too.
	_ = godotenv.Load()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config error: %v", err)
	}
	if missing := cfg.MissingProviderKeys(); len(missing) > 0 {
		log.Printf("warning: provider keys not set %v — those models will fail at call time", missing)
	}

	if err := llm.LoadPricing(cfg.PricingPath); err != nil {
		log.Fatalf("pricing error: %v", err)
	}

	database, err := db.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("database error: %v", err)
	}
	defer database.Close()

	if err := db.Migrate(database); err != nil {
		log.Fatalf("migration error: %v", err)
	}

	clients := llm.BuildClients(cfg)

	// Task registry — the DB is the single source of truth for tasks. Only the
	// built-in playground task is seeded; all product tasks are authored at
	// runtime through the Studio (POST /v1/tasks) and persist in the DB.
	taskStore := tasks.NewStore(database)
	if err := tasks.SeedPlayground(taskStore); err != nil {
		log.Fatalf("seed playground task: %v", err)
	}

	// User store — the swap seam. Replace NewDemoStore with a real Store impl
	// (Postgres, internal SSO/IdP, …) to point the platform at production
	// identity; nothing else changes. The demo store persists nothing.
	userStore := users.NewDemoStore()

	var allowedOrigins []string
	if v := os.Getenv("ALLOWED_ORIGINS"); v != "" {
		allowedOrigins = strings.Split(v, ",")
	}

	// Async observability writer — trace inserts off the request hot path.
	runWriter := db.NewRunWriter(database, 0)
	defer runWriter.Close()

	// Per-(task, model) circuit breaker — skips a model in a task's fallback
	// chain after repeated failures (provider errors OR schema-invalid output),
	// with exponential backoff and admin reset. Transitions persist to
	// model_health_events via an async writer for observation.
	healthWriter := db.NewHealthEventWriter(database, 0)
	defer healthWriter.Close()
	healthTracker := health.NewTracker(health.Config{
		Enabled:      cfg.HealthBreakerEnabled,
		Threshold:    cfg.HealthThreshold,
		BaseCooldown: cfg.HealthBaseCooldown,
		MaxCooldown:  cfg.HealthMaxCooldown,
		Factor:       2,
	}, healthWriter.Write)
	if cfg.HealthBreakerEnabled {
		log.Printf("model health breaker: on (threshold=%d, cooldown %s→%s, ×2 backoff)",
			cfg.HealthThreshold, cfg.HealthBaseCooldown, cfg.HealthMaxCooldown)
	} else {
		log.Printf("model health breaker: off")
	}

	// Prediction cache — Redis in production, in-process memory for dev boxes
	// without Redis, off otherwise. Tasks opt in via cache_enabled.
	var predictionCache cache.Cache
	switch cfg.CacheBackend {
	case "redis":
		c, err := cache.NewRedis(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
		if err != nil {
			log.Fatalf("cache error: %v", err)
		}
		predictionCache = c
		log.Printf("prediction cache: redis at %s", cfg.RedisAddr)
	case "memory":
		predictionCache = cache.NewMemory()
		log.Printf("prediction cache: in-process memory (dev only — use REDIS_ADDR in production)")
	default:
		log.Printf("prediction cache: off (set REDIS_ADDR or CACHE_BACKEND=memory to enable)")
	}

	router := api.NewRouter(api.RouterDeps{
		DB:      database,
		Clients: clients,
		Users:   userStore,
		Tasks:   taskStore,
		Runs:    runWriter,
		Cache:   predictionCache,
		Health:  healthTracker,
		Auth: api.AuthConfig{
			Secret:      []byte(cfg.JWTSecret),
			CookieName:  cfg.AuthCookieName,
			Issuer:      cfg.AuthIssuer,
			Domain:      cfg.CookieDomain,
			Secure:      cfg.CookieSecure,
			TokenExpiry: cfg.TokenExpiry,
		},
		AllowedOrigins: allowedOrigins,
	})

	addr := ":" + cfg.Port
	log.Printf("LLM Platform Go server listening on %s", addr)
	if err := http.ListenAndServe(addr, router); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
