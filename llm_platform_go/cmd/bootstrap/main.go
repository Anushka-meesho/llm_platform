// bootstrap prepares a fresh deployment securely. Run it once per environment
// before starting the server:
//
//	APP_ENV=prod DB_DRIVER=sqlite DB_PATH=/data/llm_platform.db \
//	  go run ./cmd/bootstrap -write-env -issue-admin -admin-email you@meesho.com
//
// It will, in order:
//  1. Generate a strong JWT_SECRET if one isn't set (crypto/rand), printing it
//     and — with -write-env — appending it to the env file.
//  2. Validate the config for prod and print a pass/fail checklist.
//  3. Run database migrations against the configured backend.
//  4. Lock down a SQLite DB file (0600, parent dir, sane location).
//  5. Optionally mint a break-glass admin token so the platform is reachable
//     before SSO is fully wired.
//
// Exits non-zero on any hard failure so it can gate a deploy pipeline.
package main

import (
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"llm_platform_go/internal/auth"
	"llm_platform_go/internal/config"
	"llm_platform_go/internal/db"

	"github.com/joho/godotenv"
)

func main() {
	writeEnv := flag.Bool("write-env", false, "append a generated JWT_SECRET to the env file")
	envFile := flag.String("env-file", ".env", "env file to read and (with -write-env) append to")
	issueAdmin := flag.Bool("issue-admin", false, "mint a break-glass admin token and print it")
	adminSub := flag.String("admin-sub", "svc:break-glass-admin", "subject for the break-glass admin token")
	adminEmail := flag.String("admin-email", "admin@meesho.local", "email for the break-glass admin token")
	adminTTL := flag.Duration("admin-ttl", 720*time.Hour, "break-glass admin token lifetime")
	flag.Parse()

	_ = godotenv.Load(*envFile)
	failures := 0
	fail := func(format string, args ...any) {
		failures++
		fmt.Printf("  ✗ "+format+"\n", args...)
	}
	ok := func(format string, args ...any) { fmt.Printf("  ✓ "+format+"\n", args...) }

	fmt.Println("== 1. JWT secret ==")
	if config.IsInsecureJWTSecret(os.Getenv("JWT_SECRET")) {
		secret, err := generateSecret()
		if err != nil {
			log.Fatalf("generate secret: %v", err)
		}
		_ = os.Setenv("JWT_SECRET", secret) // so config.Load below sees it
		if *writeEnv {
			if err := appendEnv(*envFile, "JWT_SECRET", secret); err != nil {
				fail("could not write JWT_SECRET to %s: %v", *envFile, err)
			} else {
				ok("generated JWT_SECRET and appended it to %s", *envFile)
			}
		} else {
			ok("generated JWT_SECRET (store this in your secrets manager):")
			fmt.Printf("\n    JWT_SECRET=%s\n\n", secret)
		}
	} else {
		ok("JWT_SECRET already set")
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	fmt.Println("== 2. Config validation ==")
	if err := cfg.Validate(); err != nil {
		// Validate returns a multi-line message; surface each problem as a failure.
		for _, line := range strings.Split(err.Error(), "\n") {
			line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
			if line == "" || strings.HasSuffix(line, ":") {
				continue
			}
			fail("%s", line)
		}
	} else {
		ok("config valid for %s mode", cfg.AppEnv)
	}

	fmt.Println("== 3. Database migration ==")
	database, err := db.Open(cfg.DBDriver, cfg.DBPath, cfg.DBDSN)
	if err != nil {
		fail("open %s database: %v", cfg.DBDriver, err)
	} else {
		defer database.Close()
		if err := db.Migrate(database); err != nil {
			fail("migrate: %v", err)
		} else {
			ok("schema migrated (%s)", cfg.DBDriver)
		}

		fmt.Println("== 4. Storage hardening ==")
		if cfg.DBDriver == "sqlite" {
			hardenSQLite(cfg.DBPath, ok, fail)
		} else {
			ok("postgres: file permissions managed by the server (skipped)")
		}
	}

	if *issueAdmin {
		fmt.Println("== 5. Break-glass admin token ==")
		secret := os.Getenv("JWT_SECRET")
		token, err := auth.IssueToken(
			&auth.User{Subject: *adminSub, Email: *adminEmail, Name: "Break-glass Admin", Role: auth.RoleAdmin},
			[]byte(secret), cfg.AuthIssuer, *adminTTL,
		)
		if err != nil {
			fail("issue admin token: %v", err)
		} else {
			ok("admin token (Authorization: Bearer …), expires %s:",
				time.Now().UTC().Add(*adminTTL).Format(time.RFC3339))
			fmt.Printf("\n    %s\n\n", token)
		}
	}

	fmt.Println(strings.Repeat("─", 60))
	if failures > 0 {
		fmt.Printf("bootstrap finished with %d problem(s) — resolve them before serving traffic.\n", failures)
		os.Exit(1)
	}
	fmt.Println("bootstrap OK — ready to start the server.")
}

// generateSecret returns 32 random bytes, base64-encoded.
func generateSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// appendEnv appends KEY=value to the env file (creating it if absent).
func appendEnv(path, key, value string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "\n# added by cmd/bootstrap\n%s=%s\n", key, value)
	return err
}

// hardenSQLite ensures the DB file's parent exists, sets 0600 on the file, and
// warns when the path sits somewhere a web server might serve it from.
func hardenSQLite(path string, ok, fail func(string, ...any)) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fail("create DB dir %s: %v", dir, err)
		return
	}
	if _, err := os.Stat(path); err == nil {
		if err := os.Chmod(path, 0o600); err != nil {
			fail("chmod 0600 %s: %v", path, err)
		} else {
			ok("DB file locked down (0600): %s", path)
		}
	} else {
		ok("DB dir ready (file will be created on first run): %s", dir)
	}
	if cwd, err := os.Getwd(); err == nil {
		if abs, err := filepath.Abs(path); err == nil && strings.HasPrefix(abs, cwd) {
			fail("DB path %s is inside the working dir — move it outside the repo/web root", abs)
		}
	}
}
