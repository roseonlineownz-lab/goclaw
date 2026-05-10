//go:build integration

package integration

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/edition"
	"github.com/nextlevelbuilder/goclaw/internal/hooks"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// ─── Hook integration shared test helpers ───────────────────────────────────
//
// Helpers scoped to the hook integration tests. Do NOT extend with generic
// store / channel helpers — keep additions hook-specific so the file stays
// focused.

// withEdition flips edition.Current() for the duration of the test, restoring
// the prior value on cleanup. Used by tests that exercise the edition-gated
// migration + validation paths.
func withEdition(t *testing.T, e edition.Edition) {
	t.Helper()
	prev := edition.Current()
	edition.SetCurrent(e)
	t.Cleanup(func() { edition.SetCurrent(prev) })
}


// purgeAgentHookByID removes one hook row + its audit. Standard cleanup
// pattern for tests that create a single hook explicitly.
func purgeAgentHookByID(t *testing.T, db *sql.DB, id uuid.UUID) {
	t.Helper()
	db.Exec(`DELETE FROM hook_executions WHERE hook_id = $1`, id)
	db.Exec(`DELETE FROM hooks WHERE id = $1`, id)
}

// pollAuditDecision polls hook_executions for one row matching (hookID,
// decision) — returns true once observed or false on timeout. Stricter
// equivalent of pollAuditCount(want=1) when callers want a boolean check.
func pollAuditDecision(t *testing.T, db *sql.DB, hookID uuid.UUID, decision string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var n int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM hook_executions WHERE hook_id = $1 AND decision = $2`,
			hookID, decision,
		).Scan(&n); err == nil && n > 0 {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// crossTenantOwnerCtx returns a context bypassing tenant scope AND carrying
// the owner role — required for tests that reach into rows across tenants
// (e.g. seed reconciliation, command-autodisable migration which lists
// every tenant's enabled rows).
func crossTenantOwnerCtx() context.Context {
	ctx := context.Background()
	ctx = store.WithRole(ctx, store.RoleRoot)
	return ctx
}

// allowAllScript is a no-op handle() that always allows. Used in tests
// that need a script hook present but care only about its lifecycle, not
// its decision behavior.
const allowAllScript = `function handle(e) { return {decision: "allow"}; }`

// hookCfgScript returns a baseline script hook config. Tests override fields
// (scope, source, event, etc.) before passing to Create. Centralizes the
// boilerplate so each test's intent stays one-line-clear.
func hookCfgScript(_ uuid.UUID, ev hooks.HookEvent, source string, src string) hooks.HookConfig {
	return hooks.HookConfig{
		Scope: hooks.ScopeUser,
		Event:       ev,
		HandlerType: hooks.HandlerScript,
		Config:      map[string]any{"source": src},
		TimeoutMS:   2000,
		OnTimeout:   hooks.DecisionAllow,
		Priority:    100,
		Enabled:     true,
		Version:     1,
		Source:      source,
		Metadata:    map[string]any{},
	}
}
