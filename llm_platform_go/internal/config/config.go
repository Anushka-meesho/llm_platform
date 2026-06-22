package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// insecureJWTDefault is the dev-only fallback secret. Validate() rejects it when
// APP_ENV=prod so a production server can never sign tokens with a known key.
const insecureJWTDefault = "dev-insecure-secret-change-me"

type Config struct {
	// AppEnv is "dev" (default) or "prod". In prod, Validate() hard-fails on any
	// insecure default (known JWT secret, missing origins, non-secure cookie, …)
	// so a misconfigured server refuses to boot rather than running unsafely.
	AppEnv string

	// AuthMode selects how humans authenticate: "demo" (passwordless pick-a-user,
	// dev only) or "sso" (real IdP). Prod must be "sso"; the demo login routes are
	// not even registered otherwise.
	AuthMode string

	GroqKey     string
	Port        string
	PricingPath string

	// Database backend. DBDriver is "sqlite" (default, location = DBPath) or
	// "postgres" (location = DBDSN connection string). Only one location field is
	// used per driver.
	DBDriver string
	DBPath   string
	DBDSN    string

	// AllowedOrigins is the CORS allowlist (the frontend origin(s)). Required in
	// prod; in dev an empty list falls back to the Vite dev origin.
	AllowedOrigins []string

	// Provider base URLs — override to route through a proxy/gateway or a self-hosted endpoint.
	GroqBaseURL string

	// Meesho internal LLM gateway (OpenAI-compatible wire, but authenticated
	// with an x-bf-vk virtual-key header instead of Bearer). MeeshoGatewayVK is
	// the virtual key — insert it via the MEESHO_GATEWAY_VK env var (or .env).
	// Empty key → the gateway's models fail at call time with an auth error,
	// never a boot failure, same as any other unconfigured provider.
	MeeshoGatewayBaseURL string
	MeeshoGatewayVK      string

	// Auth — the demo SSO seam. JWTSecret signs the session token issued after
	// a (demo) login; AuthCookieName controls where the browser stores it.
	// In production the real SSO would mint equivalent tokens; only the user
	// store + login handler change, not these fields.
	JWTSecret      string
	AuthCookieName string
	AuthIssuer     string
	CookieDomain   string
	CookieSecure   bool
	TokenExpiry    time.Duration

	// Prediction cache backend. RedisAddr set → Redis (the production
	// backend); otherwise CACHE_BACKEND=memory gives an in-process cache for
	// dev boxes without Redis; otherwise caching is off. Per-task opt-in
	// (tasks.cache_enabled) still applies either way.
	RedisAddr     string
	RedisPassword string
	RedisDB       int
	CacheBackend  string // "redis" | "memory" | "off" (derived when empty)

	// Per-(task, model) circuit breaker. After HealthThreshold consecutive
	// failures (provider errors OR schema-invalid output) a model is skipped for
	// that task for HealthBaseCooldown, doubling on each re-trip up to
	// HealthMaxCooldown. Set HEALTH_BREAKER_ENABLED=false to route every model
	// every time regardless of recent failures.
	HealthBreakerEnabled bool
	HealthThreshold      int
	HealthBaseCooldown   time.Duration
	HealthMaxCooldown    time.Duration

	// Per-task input/token rate limiter. Each task gets its own rolling window
	// of RateWindow. A request is rejected up front if its estimated input
	// exceeds RateMaxInputTokens (413), if the task already hit RateMaxRequests
	// in the window, or if reserving its estimate would exceed RateMaxTokens
	// (both 429). Accepted requests reserve their estimate, then reconcile to the
	// tokens actually consumed (input+output across every attempt, incl. failed
	// ones). 0 for a Max disables that gate. Tokens are estimated as
	// chars/RateCharsPerToken plus RateTokensPerImage per attached image.
	RateLimitEnabled   bool
	RateWindow         time.Duration
	RateMaxRequests    int
	RateMaxTokens      int
	RateMaxInputTokens int
	RateCharsPerToken  int
	RateTokensPerImage int
}

func Load() (*Config, error) {
	cfg := &Config{
		AppEnv:   strings.ToLower(getEnvOrDefault("APP_ENV", "dev")),
		AuthMode: strings.ToLower(getEnvOrDefault("AUTH_MODE", "demo")),

		GroqKey:     os.Getenv("GROQ_API_KEY"),
		Port:        getEnvOrDefault("PORT", "8000"),
		PricingPath: getEnvOrDefault("PRICING_PATH", "./pricing.json"),

		DBDriver: strings.ToLower(getEnvOrDefault("DB_DRIVER", "sqlite")),
		DBPath:   getEnvOrDefault("DB_PATH", "./llm_platform.db"),
		DBDSN:    os.Getenv("DB_DSN"),

		AllowedOrigins: splitAndTrim(os.Getenv("ALLOWED_ORIGINS")),

		GroqBaseURL: getEnvOrDefault("GROQ_BASE_URL", "https://api.groq.com/openai/v1"),

		MeeshoGatewayBaseURL: getEnvOrDefault("MEESHO_GATEWAY_BASE_URL", "http://llm-gateway.prd.meesho.int/v1"),
		MeeshoGatewayVK:      os.Getenv("MEESHO_GATEWAY_VK"),

		JWTSecret:      getEnvOrDefault("JWT_SECRET", insecureJWTDefault),
		AuthCookieName: getEnvOrDefault("AUTH_COOKIE_NAME", "llm_platform_token"),
		AuthIssuer:     getEnvOrDefault("AUTH_ISSUER", "llm-platform-demo"),
		CookieDomain:   os.Getenv("COOKIE_DOMAIN"),
		CookieSecure:   getEnvBool("COOKIE_SECURE", false),
		TokenExpiry:    getEnvDuration("TOKEN_EXPIRY", 12*time.Hour),

		RedisAddr:     os.Getenv("REDIS_ADDR"),
		RedisPassword: os.Getenv("REDIS_PASSWORD"),
		RedisDB:       getEnvInt("REDIS_DB", 0),
		CacheBackend:  os.Getenv("CACHE_BACKEND"),

		HealthBreakerEnabled: getEnvBool("HEALTH_BREAKER_ENABLED", true),
		HealthThreshold:      getEnvInt("HEALTH_FAILURE_THRESHOLD", 3),
		HealthBaseCooldown:   getEnvDuration("HEALTH_BASE_COOLDOWN", 30*time.Second),
		HealthMaxCooldown:    getEnvDuration("HEALTH_MAX_COOLDOWN", 30*time.Minute),

		RateLimitEnabled:   getEnvBool("RATE_LIMIT_ENABLED", true),
		RateWindow:         getEnvDuration("RATE_WINDOW", time.Minute),
		RateMaxRequests:    getEnvInt("RATE_MAX_REQUESTS", 600),
		RateMaxTokens:      getEnvInt("RATE_MAX_TOKENS", 200000),
		RateMaxInputTokens: getEnvInt("RATE_MAX_INPUT_TOKENS", 16000),
		RateCharsPerToken:  getEnvInt("RATE_CHARS_PER_TOKEN", 4),
		RateTokensPerImage: getEnvInt("RATE_TOKENS_PER_IMAGE", 1000),
	}
	if cfg.CacheBackend == "" {
		if cfg.RedisAddr != "" {
			cfg.CacheBackend = "redis"
		} else {
			cfg.CacheBackend = "off"
		}
	}

	// Require at least one active provider key so a totally unconfigured server
	// fails loudly at startup rather than returning auth errors on every call.
	if cfg.GroqKey == "" && cfg.MeeshoGatewayVK == "" {
		return nil, fmt.Errorf("no provider API keys set: configure at least one of GROQ_API_KEY, MEESHO_GATEWAY_VK")
	}

	return cfg, nil
}

// IsProd reports whether the server is configured for production.
func (c *Config) IsProd() bool { return c.AppEnv == "prod" }

// IsInsecureJWTSecret reports whether s is unset or the known dev default — i.e.
// unsafe to sign production tokens with. Shared by Validate and cmd/bootstrap.
func IsInsecureJWTSecret(s string) bool { return s == "" || s == insecureJWTDefault }

// Validate enforces production safety invariants. In dev it is a no-op (returning
// only fast-fail driver checks); in prod every dangerous default becomes a boot
// error so a misconfigured server refuses to start. Call it right after Load().
func (c *Config) Validate() error {
	// Driver sanity applies in every environment.
	switch c.DBDriver {
	case "sqlite":
		if c.DBPath == "" {
			return fmt.Errorf("DB_DRIVER=sqlite requires DB_PATH")
		}
	case "postgres":
		if c.DBDSN == "" {
			return fmt.Errorf("DB_DRIVER=postgres requires DB_DSN")
		}
	default:
		return fmt.Errorf("DB_DRIVER must be 'sqlite' or 'postgres', got %q", c.DBDriver)
	}

	switch c.AuthMode {
	case "demo", "sso":
	default:
		return fmt.Errorf("AUTH_MODE must be 'demo' or 'sso', got %q", c.AuthMode)
	}

	if !c.IsProd() {
		return nil
	}

	// --- Production-only hard requirements. ---
	var problems []string
	if IsInsecureJWTSecret(c.JWTSecret) {
		problems = append(problems, "JWT_SECRET must be set to a strong non-default value")
	}
	if c.AuthMode == "demo" {
		problems = append(problems, "AUTH_MODE=demo is not allowed in prod (use sso); the passwordless login is dev-only")
	}
	if len(c.AllowedOrigins) == 0 {
		problems = append(problems, "ALLOWED_ORIGINS must list the frontend origin(s)")
	}
	if !c.CookieSecure {
		problems = append(problems, "COOKIE_SECURE must be true (HTTPS-only session cookie)")
	}
	if c.DBDriver == "sqlite" && !filepath.IsAbs(c.DBPath) {
		problems = append(problems, fmt.Sprintf("DB_PATH must be an absolute path in prod, got %q", c.DBPath))
	}
	if !filepath.IsAbs(c.PricingPath) {
		problems = append(problems, fmt.Sprintf("PRICING_PATH must be an absolute path in prod, got %q", c.PricingPath))
	}
	if len(problems) > 0 {
		return fmt.Errorf("insecure production config:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

// MissingProviderKeys lists provider keys that are not configured, for an
// informational startup warning.
func (c *Config) MissingProviderKeys() []string {
	var missing []string
	if c.GroqKey == "" {
		missing = append(missing, "GROQ_API_KEY")
	}
	if c.MeeshoGatewayVK == "" {
		missing = append(missing, "MEESHO_GATEWAY_VK")
	}
	return missing
}

// splitAndTrim parses a comma-separated env value into a trimmed, non-empty list.
func splitAndTrim(v string) []string {
	if v == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func getEnvOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func getEnvBool(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return fallback
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}
