package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	GroqKey     string
	DBPath      string
	Port        string
	PricingPath string

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
}

func Load() (*Config, error) {
	cfg := &Config{
		GroqKey:     os.Getenv("GROQ_API_KEY"),
		DBPath:      getEnvOrDefault("DB_PATH", "./llm_platform.db"),
		Port:        getEnvOrDefault("PORT", "8000"),
		PricingPath: getEnvOrDefault("PRICING_PATH", "./pricing.json"),

		GroqBaseURL: getEnvOrDefault("GROQ_BASE_URL", "https://api.groq.com/openai/v1"),

		MeeshoGatewayBaseURL: getEnvOrDefault("MEESHO_GATEWAY_BASE_URL", "http://llm-gateway.prd.meesho.int/v1"),
		MeeshoGatewayVK:      os.Getenv("MEESHO_GATEWAY_VK"),

		JWTSecret:      getEnvOrDefault("JWT_SECRET", "dev-insecure-secret-change-me"),
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
