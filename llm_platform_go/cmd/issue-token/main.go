// issue-token mints long-lived service tokens for machine callers (CIS, batch
// jobs). The platform's RequireAuth already accepts Bearer tokens; this is how
// a service principal gets one.
//
// Usage:
//
//	go run ./cmd/issue-token -sub svc:cis -email cis@svc.local -name "CIS" -ttl 8760h
//
// Convention: service subjects are prefixed "svc:" so per-caller usage shows
// up distinctly in run attribution and dashboards.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"llm_platform_go/internal/auth"

	"github.com/joho/godotenv"
)

func main() {
	sub := flag.String("sub", "", "principal subject, e.g. svc:cis (required)")
	email := flag.String("email", "", "principal email, e.g. cis@svc.local (required)")
	name := flag.String("name", "", "display name")
	role := flag.String("role", auth.RoleClient, "RBAC role: admin or client")
	ttl := flag.Duration("ttl", 8760*time.Hour, "token lifetime (default 1 year)")
	issuer := flag.String("issuer", "", "JWT issuer (default: AUTH_ISSUER env or llm-platform-demo)")
	flag.Parse()

	if *sub == "" || *email == "" {
		flag.Usage()
		os.Exit(2)
	}
	if !auth.KnownRole(*role) {
		log.Fatalf("unknown role %q (want: admin or client)", *role)
	}

	_ = godotenv.Load() // pick up JWT_SECRET from .env when run from the repo

	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		log.Fatal("JWT_SECRET is not set (env or .env)")
	}
	iss := *issuer
	if iss == "" {
		iss = os.Getenv("AUTH_ISSUER")
	}
	if iss == "" {
		iss = "llm-platform-demo"
	}

	token, err := auth.IssueToken(
		&auth.User{Subject: *sub, Email: *email, Name: *name, Role: *role},
		[]byte(secret), iss, *ttl,
	)
	if err != nil {
		log.Fatalf("issue token: %v", err)
	}

	fmt.Fprintf(os.Stderr, "subject=%s role=%s issuer=%s expires=%s\n",
		*sub, *role, iss, time.Now().UTC().Add(*ttl).Format(time.RFC3339))
	fmt.Println(token) // token on stdout for easy capture
}
