package config

import (
	"fmt"
	"os"
)

type Config struct {
	OpenAIKey   string
	GroqKey     string
	GeminiKey   string
	DBPath      string
	Port        string
	PricingPath string
}

func Load() (*Config, error) {
	cfg := &Config{
		OpenAIKey:   os.Getenv("OPENAI_API_KEY"),
		GroqKey:     os.Getenv("GROQ_API_KEY"),
		GeminiKey:   os.Getenv("GEMINI_API_KEY"),
		DBPath:      getEnvOrDefault("DB_PATH", "./llm_platform.db"),
		Port:        getEnvOrDefault("PORT", "8000"),
		PricingPath: getEnvOrDefault("PRICING_PATH", "./pricing.json"),
	}

	var missing []string
	if cfg.OpenAIKey == "" {
		missing = append(missing, "OPENAI_API_KEY")
	}
	if cfg.GroqKey == "" {
		missing = append(missing, "GROQ_API_KEY")
	}
	if cfg.GeminiKey == "" {
		missing = append(missing, "GEMINI_API_KEY")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required environment variables: %v", missing)
	}

	return cfg, nil
}

func getEnvOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
