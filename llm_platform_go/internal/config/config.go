package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	OpenAIKey    string
	GroqKey      string
	GeminiKey    string
	AnthropicKey string
	DBPath       string
	Port         string
	PricingPath  string

	// Provider base URLs — override to route through a proxy/gateway or a
	// self-hosted endpoint. Defaults are the public APIs. AnthropicBaseURL
	// empty → the SDK default (https://api.anthropic.com).
	OpenAIBaseURL    string
	GroqBaseURL      string
	GeminiBaseURL    string
	AnthropicBaseURL string

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
}

func Load() (*Config, error) {
	cfg := &Config{
		OpenAIKey:    os.Getenv("OPENAI_API_KEY"),
		GroqKey:      os.Getenv("GROQ_API_KEY"),
		GeminiKey:    os.Getenv("GEMINI_API_KEY"),
		AnthropicKey: os.Getenv("ANTHROPIC_API_KEY"),
		DBPath:       getEnvOrDefault("DB_PATH", "./llm_platform.db"),
		Port:         getEnvOrDefault("PORT", "8000"),
		PricingPath:  getEnvOrDefault("PRICING_PATH", "./pricing.json"),

		OpenAIBaseURL:    getEnvOrDefault("OPENAI_BASE_URL", "https://api.openai.com/v1"),
		GroqBaseURL:      getEnvOrDefault("GROQ_BASE_URL", "https://api.groq.com/openai/v1"),
		GeminiBaseURL:    getEnvOrDefault("GEMINI_BASE_URL", "https://generativelanguage.googleapis.com/v1beta/openai"),
		AnthropicBaseURL: os.Getenv("ANTHROPIC_BASE_URL"),

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
	}
	if cfg.CacheBackend == "" {
		if cfg.RedisAddr != "" {
			cfg.CacheBackend = "redis"
		} else {
			cfg.CacheBackend = "off"
		}
	}

	// Each provider key is independent: the platform boots with whatever subset
	// is configured. Models whose key is missing simply fail at call time (the
	// runner reports a per-model auth error). Only require at least one, so a
	// totally unconfigured server still fails loudly.
	if cfg.OpenAIKey == "" && cfg.GroqKey == "" && cfg.GeminiKey == "" && cfg.AnthropicKey == "" {
		return nil, fmt.Errorf("no provider API keys set: configure at least one of OPENAI_API_KEY, GROQ_API_KEY, GEMINI_API_KEY, ANTHROPIC_API_KEY")
	}

	return cfg, nil
}

// MissingProviderKeys lists provider keys that are not configured, for an
// informational startup warning.
func (c *Config) MissingProviderKeys() []string {
	var missing []string
	if c.OpenAIKey == "" {
		missing = append(missing, "OPENAI_API_KEY")
	}
	if c.GroqKey == "" {
		missing = append(missing, "GROQ_API_KEY")
	}
	if c.GeminiKey == "" {
		missing = append(missing, "GEMINI_API_KEY")
	}
	if c.AnthropicKey == "" {
		missing = append(missing, "ANTHROPIC_API_KEY")
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
