// migrate applies the database schema for the configured backend, then exits.
// Run it out-of-band before a new server build serves traffic (the server does
// NOT auto-migrate in prod), so a rolling deploy never blocks on — or races — a
// schema change.
//
// Usage (reads the same env / .env as the server):
//
//	DB_DRIVER=sqlite   DB_PATH=/data/llm_platform.db   go run ./cmd/migrate
//	DB_DRIVER=postgres DB_DSN='postgres://user:pass@host:5432/db' go run ./cmd/migrate
package main

import (
	"log"

	"llm_platform_go/internal/config"
	"llm_platform_go/internal/db"

	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	database, err := db.Open(cfg.DBDriver, cfg.DBPath, cfg.DBDSN)
	if err != nil {
		log.Fatalf("database error: %v", err)
	}
	defer database.Close()

	log.Printf("migrating %s database…", cfg.DBDriver)
	if err := db.Migrate(database); err != nil {
		log.Fatalf("migration failed: %v", err)
	}
	log.Printf("migration complete")
}
