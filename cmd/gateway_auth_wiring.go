package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/auth"
	"github.com/nextlevelbuilder/goclaw/internal/email"
	httpapi "github.com/nextlevelbuilder/goclaw/internal/http"
)

// wireAuthBootstrap initializes Phase-06 auth + bootstrap state at gateway startup:
//   - counts users → sets bootstrap-required flag
//   - generates a one-shot bootstrap token (printed to stderr) when required
//   - constructs JWTKeyset from env, registers SIGHUP reload (caller's responsibility)
//   - registers /v1/bootstrap/* and /v1/auth/* HTTP handlers
//
// Idempotent guards: if Users or UserSessions stores are nil, the caller skips
// this entirely. JWTKeyset construction failure is **fatal on a fresh install**:
// without a keyset, /v1/bootstrap/init is never registered, leaving the
// operator with an unbootstrappable gateway. Caller propagates the error.
func (d *gatewayDeps) wireAuthBootstrap(ctx context.Context) error {
	users := d.pgStores.Users
	sessions := d.pgStores.UserSessions

	// Count users; users count of 0 → bootstrap required.
	list, err := users.List(ctx, 1, 0)
	bootstrapNeeded := err == nil && len(list) == 0
	httpapi.SetBootstrapRequired(bootstrapNeeded)
	if bootstrapNeeded {
		tok, gerr := httpapi.GenerateBootstrapToken()
		if gerr != nil {
			slog.Error("bootstrap.token_generation_failed", "err", gerr)
			return fmt.Errorf("bootstrap token generation failed: %w", gerr)
		}
		slog.Info("bootstrap.token_generated",
			"token", tok,
			"msg", "set X-Bootstrap-Token header on POST /v1/bootstrap/init",
		)
	}

	// JWT keyset. Required on fresh install (operator must be able to bootstrap
	// even before any sessions exist) — fail-fast if misconfigured.
	ks, err := auth.NewJWTKeyset()
	if err != nil {
		slog.Error("auth.jwt_keyset_init_failed",
			"err", err,
			"hint", "set GOCLAW_JWT_SECRETS_JSON or GOCLAW_JWT_SECRET",
		)
		return fmt.Errorf("JWT keyset init failed (set GOCLAW_JWT_SECRETS_JSON or GOCLAW_JWT_SECRET): %w", err)
	}
	httpapi.InitJWTKeyset(ks)

	accessTTL := envDuration("GOCLAW_JWT_ACCESS_TTL", 15*time.Minute)
	refreshTTL := envDuration("GOCLAW_JWT_REFRESH_TTL", 30*24*time.Hour)

	// rc1: only the stderr-stub Dispatcher exists. Real SMTP wiring lands
	// post-rc1; the stub keeps every reset/invite path alive in dev/local.
	if d.emailer == nil {
		d.emailer = email.NewStderrDispatcher(slog.Default())
	}

	d.server.SetBootstrapHandler(httpapi.NewBootstrapHandler(
		users, sessions, d.pgStores.DB, ks, accessTTL, refreshTTL,
	))
	d.server.SetAuthHandler(httpapi.NewAuthHandler(
		users, sessions, d.pgStores.PasswordReset, d.emailer,
		ks, accessTTL, refreshTTL, d.cfg.WebURL,
	))
	d.server.SetUsersHandler(httpapi.NewUsersHandler(users))
	d.server.SetHooksBudgetHandler(httpapi.NewHooksBudgetHandler(d.pgStores.UserHookBudget))
	return nil
}

// envDuration parses a Go duration from env or returns the fallback.
func envDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	if secs, err := strconv.Atoi(v); err == nil {
		return time.Duration(secs) * time.Second
	}
	return fallback
}
