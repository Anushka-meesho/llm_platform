package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"llm_platform_go/internal/api"
	"llm_platform_go/internal/cache"
	"llm_platform_go/internal/config"
	"llm_platform_go/internal/db"
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

	// Recovery prober: production requests never probe a failing provider —
	// they fail fast down the task's fallback chain. This background loop
	// health-checks unhealthy providers (1-token ping every 15s) and closes
	// their circuit on recovery, returning traffic to the highest-priority
	// healthy model automatically.
	proberCtx, stopProber := context.WithCancel(context.Background())
	defer stopProber()
	llm.StartRecoveryProber(proberCtx, clients, 15*time.Second)
	log.Printf("recovery prober: started (15s interval, probe-only breakers)")

	// Task registry — seed the built-in playground task and any tasks.d/*.yaml
	// configs (the plug-and-play onboarding contract).
	taskStore := tasks.NewStore(database)
	if err := tasks.SeedPlayground(taskStore); err != nil {
		log.Fatalf("seed playground task: %v", err)
	}
	tasksDir := os.Getenv("TASKS_DIR")
	if tasksDir == "" {
		tasksDir = "./tasks.d"
	}
	if n, err := tasks.LoadYAMLDir(taskStore, tasksDir); err != nil {
		log.Fatalf("load task configs from %s: %v", tasksDir, err)
	} else if n > 0 {
		log.Printf("loaded %d task config(s) from %s", n, tasksDir)
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
