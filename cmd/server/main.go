package main

import (
	"log"
	"net/http"

	"llm_platform_go/internal/api"
	"llm_platform_go/internal/config"
	"llm_platform_go/internal/db"
	"llm_platform_go/internal/llm"

	"github.com/joho/godotenv"
)

func main() {
	// Load .env — ignore error so the binary works with real env vars too.
	_ = godotenv.Load()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config error: %v", err)
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
	router := api.NewRouter(database, clients)

	addr := ":" + cfg.Port
	log.Printf("LLM Platform Go server listening on %s", addr)
	if err := http.ListenAndServe(addr, router); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
