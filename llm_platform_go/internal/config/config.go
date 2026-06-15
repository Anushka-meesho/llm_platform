package config

import (
	"fmt"
	"os"
	"strings"
)

type Config struct {
	BifrostURL        string
	BifrostVirtualKey string
	DBPath            string
	Port              string
	PricingPath       string
}

func Load() (*Config, error) {
	cfg := &Config{
		BifrostURL:        strings.TrimRight(getEnvOrDefault("BIFROST_URL", "http://llm-gateway.prd.meesho.int"), "/"),
		BifrostVirtualKey: os.Getenv("BIFROST_VIRTUAL_KEY"),
		DBPath:            getEnvOrDefault("DB_PATH", "./llm_platform.db"),
		Port:              getEnvOrDefault("PORT", "8000"),
		PricingPath:       getEnvOrDefault("PRICING_PATH", "./pricing.json"),
	}

	if cfg.BifrostVirtualKey == "" {
		return nil, fmt.Errorf("missing required environment variable: BIFROST_VIRTUAL_KEY")
	}

	return cfg, nil
}

func getEnvOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
