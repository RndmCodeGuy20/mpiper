// Command mint-api-key inserts a new API key for a tenant and prints the
// plaintext key exactly once. There is no HTTP admin surface — keys are minted
// out-of-band with this tool.
//
// Usage:
//
//	go run ./cmd/mint-api-key --tenant demo-user [--env development]
//	                          [--expires 720h] [--scopes assets:write,webhooks:write]
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/rndmcodeguy20/mpiper/internal/config"
	"github.com/rndmcodeguy20/mpiper/internal/database"
	"github.com/rndmcodeguy20/mpiper/internal/repository"
	"github.com/rndmcodeguy20/mpiper/pkg/utils"
	tenantpkg "github.com/rndmcodeguy20/mpiper/pkg/utils/tenant"
	"go.uber.org/zap"
)

func main() {
	var (
		tenant  = flag.String("tenant", "", "tenant id the key authenticates as (required)")
		env     = flag.String("env", envOr("ENV", "development"), "config environment (development|staging|production)")
		expires = flag.Duration("expires", 0, "optional validity window, e.g. 720h; 0 means never expires")
		scopes  = flag.String("scopes", "", "optional comma-separated scopes")
	)
	flag.Parse()

	if *tenant == "" {
		fmt.Fprintln(os.Stderr, "error: --tenant is required")
		flag.Usage()
		os.Exit(2)
	}
	if !tenantpkg.IsValidSlug(*tenant) {
		fmt.Fprintf(os.Stderr, "error: --tenant %q is not a valid tenant identifier (allowed: a-z, 0-9, _, -; max 64 chars)\n", *tenant)
		flag.Usage()
		os.Exit(2)
	}

	cfg, err := config.InitializeConfig(config.ToEnvironment(*env))
	if err != nil {
		fatalf("load config: %v", err)
	}
	config.Init(cfg)

	db, err := database.NewPostgresDB(cfg.DB)
	if err != nil {
		fatalf("connect db: %v", err)
	}
	defer func() { _ = db.Close() }()

	mat, err := utils.GenerateAPIKey()
	if err != nil {
		fatalf("generate key: %v", err)
	}

	var scopeList []string
	if s := strings.TrimSpace(*scopes); s != "" {
		for _, sc := range strings.Split(s, ",") {
			if t := strings.TrimSpace(sc); t != "" {
				scopeList = append(scopeList, t)
			}
		}
	}

	var expiresAt *time.Time
	if *expires > 0 {
		t := time.Now().Add(*expires).UTC()
		expiresAt = &t
	}

	repo := repository.NewAPIKeyRepository(db, zap.NewNop())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	id, err := repo.Create(ctx, *tenant, mat.Hash, mat.Prefix, scopeList, expiresAt)
	if err != nil {
		fatalf("insert key: %v", err)
	}

	exp := "never"
	if expiresAt != nil {
		exp = expiresAt.Format(time.RFC3339)
	}

	// Human-readable summary to stderr; the bare key to stdout so it can be
	// captured cleanly: KEY=$(go run ./cmd/mint-api-key --tenant t)
	fmt.Fprintf(os.Stderr, "API key created\n")
	fmt.Fprintf(os.Stderr, "  id:      %s\n", id)
	fmt.Fprintf(os.Stderr, "  tenant:  %s\n", *tenant)
	fmt.Fprintf(os.Stderr, "  prefix:  %s\n", mat.Prefix)
	fmt.Fprintf(os.Stderr, "  scopes:  %v\n", scopeList)
	fmt.Fprintf(os.Stderr, "  expires: %s\n", exp)
	fmt.Fprintf(os.Stderr, "  (the key below is shown ONCE and is not recoverable)\n")
	fmt.Println(mat.Full)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "mint-api-key: "+format+"\n", args...)
	os.Exit(1)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
