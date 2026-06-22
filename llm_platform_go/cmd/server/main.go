package main

import (
	"context"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"llm_platform_go/internal/api"
	"llm_platform_go/internal/cache"
	"llm_platform_go/internal/config"
	"llm_platform_go/internal/db"
	"llm_platform_go/internal/health"
	"llm_platform_go/internal/llm"
	"llm_platform_go/internal/ratelimit"
	"llm_platform_go/internal/tasks"
	"llm_platform_go/internal/users"

	"github.com/joho/godotenv"
)

func main() {
	// Load .env — ignore error so the binary works with real env vars too.
	_ = godotenv.Load()

	// Structured logging: every handled error is logged as JSON keyed by the
	// request id, so a failure surfaced to a client (which carries the same id)
	// is findable in the logs. LOG_LEVEL=debug widens it.
	logLevel := slog.LevelInfo
	if strings.EqualFold(os.Getenv("LOG_LEVEL"), "debug") {
		logLevel = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel})))

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config error: %v", err)
	}
	// In prod this rejects every insecure default (dev JWT secret, missing
	// ALLOWED_ORIGINS, non-secure cookie, relative paths, AUTH_MODE=demo) so a
	// misconfigured server refuses to boot rather than running unsafely.
	if err := cfg.Validate(); err != nil {
		log.Fatalf("config error: %v", err)
	}
	log.Printf("starting in %s mode (auth=%s, db=%s)", cfg.AppEnv, cfg.AuthMode, cfg.DBDriver)
	if missing := cfg.MissingProviderKeys(); len(missing) > 0 {
		log.Printf("warning: provider keys not set %v — those models will fail at call time", missing)
	}

	if err := llm.LoadPricing(cfg.PricingPath); err != nil {
		log.Fatalf("pricing error: %v", err)
	}

	database, err := db.Open(cfg.DBDriver, cfg.DBPath, cfg.DBDSN)
	if err != nil {
		log.Fatalf("database error: %v", err)
	}
	defer database.Close()

	// Migrations auto-run in dev for a zero-friction local boot. In prod they are
	// applied out-of-band via `cmd/migrate` (or cmd/bootstrap) before the new
	// build serves traffic, so a rolling deploy never blocks on — or races — a
	// schema change.
	if cfg.IsProd() {
		log.Printf("prod: skipping inline migrations (run `cmd/migrate` before deploy)")
	} else if err := db.Migrate(database); err != nil {
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
	if err := tasks.SeedAttributeExtraction(taskStore); err != nil {
		log.Fatalf("seed attribute-extraction task: %v", err)
	}

	// User store — the swap seam. Replace NewDemoStore with a real Store impl
	// (Postgres, internal SSO/IdP, …) to point the platform at production
	// identity; nothing else changes. The demo store persists nothing.
	userStore := users.NewDemoStore()

	// Async observability writer — trace inserts off the request hot path.
	runWriter := db.NewRunWriter(database, 0)
	defer runWriter.Close()

	// Async gateway-trace writer — one row per model the fallback walk touches,
	// off the hot path like runWriter. Captures every fallback, its reason, the
	// error and its classification, retries, and per-call latency for each run.
	attemptWriter := db.NewGatewayAttemptWriter(database, 0)
	defer attemptWriter.Close()

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

	// Per-task request/token rate limiter — rejects oversized inputs (413) and
	// throttles requests and token throughput per task per window (429), counting
	// the tokens actually consumed (incl. failed/fallback attempts).
	limiter := ratelimit.New(ratelimit.Config{
		Enabled:        cfg.RateLimitEnabled,
		Window:         cfg.RateWindow,
		MaxRequests:    cfg.RateMaxRequests,
		MaxTokens:      cfg.RateMaxTokens,
		MaxInputTokens: cfg.RateMaxInputTokens,
		CharsPerToken:  cfg.RateCharsPerToken,
		TokensPerImage: cfg.RateTokensPerImage,
	})
	if cfg.RateLimitEnabled {
		log.Printf("rate limiter: on (per task, window %s — max %d req, %d tok; max %d input tok/request)",
			cfg.RateWindow, cfg.RateMaxRequests, cfg.RateMaxTokens, cfg.RateMaxInputTokens)
	} else {
		log.Printf("rate limiter: off")
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
		DB:       database,
		Clients:  clients,
		Users:    userStore,
		Tasks:    taskStore,
		Runs:     runWriter,
		Attempts: attemptWriter,
		Cache:    predictionCache,
		Health:   healthTracker,
		Limiter:  limiter,
		Auth: api.AuthConfig{
			Secret:      []byte(cfg.JWTSecret),
			CookieName:  cfg.AuthCookieName,
			Issuer:      cfg.AuthIssuer,
			Domain:      cfg.CookieDomain,
			Secure:      cfg.CookieSecure,
			TokenExpiry: cfg.TokenExpiry,
		},
		AllowedOrigins: cfg.AllowedOrigins,
		AuthMode:       cfg.AuthMode,
	})

	addr := ":" + cfg.Port
	server := &http.Server{
		Addr:    addr,
		Handler: router,
		// Bound how long a single connection may hold resources, so slow or idle
		// clients can't exhaust the server (Slowloris). WriteTimeout is generous
		// because predictions can legitimately take many seconds.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Graceful shutdown: on SIGINT/SIGTERM, stop accepting new connections, let
	// in-flight requests drain, then close the async writers so their buffered
	// rows flush instead of being dropped on exit.
	serverErr := make(chan error, 1)
	go func() {
		log.Printf("LLM Platform Go server listening on %s", addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-serverErr:
		log.Fatalf("server error: %v", err)
	case sig := <-stop:
		log.Printf("received %s — shutting down gracefully", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			log.Printf("graceful shutdown timed out: %v", err)
		}
		// Drain the async writers before the deferred database.Close() runs, so
		// queued trace/run/health rows are persisted rather than lost.
		runWriter.Close()
		attemptWriter.Close()
		healthWriter.Close()
		log.Printf("shutdown complete")
	}
}
