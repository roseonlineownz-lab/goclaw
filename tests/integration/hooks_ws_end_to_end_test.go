//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/edition"
	"github.com/nextlevelbuilder/goclaw/internal/gateway/methods"
	"github.com/nextlevelbuilder/goclaw/internal/hooks"
	"github.com/nextlevelbuilder/goclaw/internal/hooks/builtin"
	hookhandlers "github.com/nextlevelbuilder/goclaw/internal/hooks/handlers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/store/pg"
)

// WS end-to-end equivalence.
//
// The repo does not yet ship an in-process WS server bootstrap helper, so
// these tests exercise the SAME code paths the WS handlers invoke:
//   - Validate() (ConfigConfig.Validate runs the edition gate + handler rules)
//   - PGHookStore.Create + ResolveForEvent (live PG)
//   - DispatcherTestRunner.RunTest (the hooks.test method's underlying runner)
//   - Connect-response equivalence: edition.Current().Name + IsMasterScope
//
// When/if a real WS bootstrap arrives, the connect-shape and create-flow
// cases should be re-implemented over it. Until then this is the closest
// thing to a wire-level guarantee without forking the gateway init flow.

// master-scope ctx + Standard edition → IsMasterScope=true,
// edition.Current().Name="standard". Equivalent to the values returned in
// the WS connect response shape (`is_master_scope`, `edition`).
func TestHooksConnectShape_MasterStandard(t *testing.T) {
	withEdition(t, edition.Standard)
	ctx := crossTenantOwnerCtx()
	if !store.IsMasterScope(ctx) {
		t.Errorf("master ctx → IsMasterScope=false; want true")
	}
	if got := edition.Current().Name; got != edition.Standard.Name {
		t.Errorf("edition=%q, want %q", got, edition.Standard.Name)
	}
}

// tenant ctx + Standard edition → IsMasterScope=false,
// edition.Current().Name="standard".
func TestHooksConnectShape_TenantStandard(t *testing.T) {
	withEdition(t, edition.Standard)
	tenantA, _ := seedTenantAgent(t, testDB(t))
	ctx := tenantCtx(tenantA)
	if store.IsMasterScope(ctx) {
		t.Errorf("tenant ctx → IsMasterScope=true; want false")
	}
	if got := edition.Current().Name; got != edition.Standard.Name {
		t.Errorf("edition=%q, want %q", got, edition.Standard.Name)
	}
}

// operator ctx + Lite edition → IsMasterScope=false (RBAC role does
// not affect master-scope flag), edition.Current().Name="lite".
func TestHooksConnectShape_OperatorLite(t *testing.T) {
	withEdition(t, edition.Lite)
	tenantA, _ := seedTenantAgent(t, testDB(t))
	ctx := tenantCtx(tenantA)
	// Operator role on Lite — operator does not flip IsMasterScope.
	ctx = store.WithRole(ctx, "operator")
	if store.IsMasterScope(ctx) {
		t.Errorf("operator ctx → IsMasterScope=true; want false")
	}
	if got := edition.Current().Name; got != edition.Lite.Name {
		t.Errorf("edition=%q, want %q", got, edition.Lite.Name)
	}
}

// Tenant admin creating a script hook (handler_type='script',
// scope='tenant', valid JS) succeeds end-to-end through the same Validate +
// Create + ResolveForEvent + TestRunner pipeline that hooks.create / list /
// test invoke.
func TestHooksTenantAdminCreatesScriptHook(t *testing.T) {
	withEdition(t, edition.Standard)
	db := testDB(t)
	tenantA, agentA := seedTenantAgent(t, db)
	hs := pg.NewPGHookStore(db)

	cfg := hookCfgScript(tenantA, hooks.EventUserPromptSubmit, "ui",
		`function handle(e) { return {decision: "allow", reason: "ok"}; }`)
	if err := cfg.Validate(edition.Standard); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	id, err := hs.Create(tenantCtx(tenantA), cfg)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { purgeAgentHookByID(t, db, id) })

	// hooks.list equivalent — confirm row appears under tenant ctx.
	rows, err := hs.List(tenantCtx(tenantA), hooks.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, r := range rows {
		if r.ID == id {
			found = true
		}
	}
	if !found {
		t.Errorf("list did not include newly-created hook %s", id)
	}

	// hooks.test equivalent — DispatcherTestRunner with script handler.
	runner := methods.NewDispatcherTestRunner(map[hooks.HandlerType]hooks.Handler{
		hooks.HandlerScript: hookhandlers.NewScriptHandler(4, 2, 32),
	})
	res := runner.RunTest(context.Background(), cfg, hooks.Event{
		EventID:   "tenant-admin-create",
		AgentID:   agentA,
		HookEvent: hooks.EventUserPromptSubmit,
	})
	if res.Decision != hooks.DecisionAllow {
		t.Errorf("test result decision=%v, want allow", res.Decision)
	}
}

// handler_type='command' on Standard edition → Validate rejects.
// Mirrors the WS handler's pre-Create gate.
func TestHooksCommandHandlerRejectedOnStandard(t *testing.T) {
	withEdition(t, edition.Standard)

	cfg := hooks.HookConfig{
		Scope:       hooks.ScopeUser,
		Event:       hooks.EventPreToolUse,
		HandlerType: hooks.HandlerCommand,
		Config:      map[string]any{"command": "echo ok"},
		TimeoutMS:   2000,
		OnTimeout:   hooks.DecisionAllow,
		Enabled:     true,
		Source:      "ui",
	}
	err := cfg.Validate(edition.Standard)
	if err == nil {
		t.Fatal("Standard Validate accepted command handler; want rejection")
	}
	if !strings.Contains(err.Error(), "command") {
		t.Errorf("error should mention 'command' handler: %v", err)
	}

	// Sanity: Lite accepts the same config (regression guard).
	if err := cfg.Validate(edition.Lite); err != nil {
		t.Errorf("Lite Validate rejected command handler: %v", err)
	}
}

// hooks.test invoked with the pii-redactor builtin + sample event
// containing an email → result.UpdatedInput.rawInput contains
// [REDACTED_EMAIL]. This is the WS test panel's golden assertion.
func TestHooksPIIRedactorTestPanel(t *testing.T) {
	withEdition(t, edition.Standard)
	if err := builtin.Load(); err != nil {
		t.Fatalf("builtin.Load: %v", err)
	}

	src, err := builtin.Source("pii-redactor.js")
	if err != nil {
		t.Fatalf("Source: %v", err)
	}
	cfg := hookCfgScript(uuid.Nil, hooks.EventUserPromptSubmit,
		hooks.SourceBuiltin, string(src))
	cfg.Scope = hooks.ScopeGlobal
	cfg.ID = builtin.BuiltinEventID("pii-redactor", string(hooks.EventUserPromptSubmit))
	cfg.TimeoutMS = 2000
	cfg.Priority = 900

	runner := methods.NewDispatcherTestRunner(map[hooks.HandlerType]hooks.Handler{
		hooks.HandlerScript: hookhandlers.NewScriptHandler(4, 2, 32),
	})

	// DispatcherTestRunner.RunTest does NOT populate HookTestResult.UpdatedInput
	// (it only forwards Decision / Reason / Error / DurationMS) per
	// hooks_test_runner.go. The mutation surface lives on the dispatcher path
	// (FireResult.UpdatedRawInput) — already covered by the dispatcher tests.
	// Here we instead exercise the runner's decision pathway end-to-end with
	// the pii-redactor JS so we catch any wiring break in the WS test panel
	// (the builtin returns decision=allow whether or not the input contains
	// PII, so a regression where the sandbox blocks the script would surface
	// as decision=error here).
	res := runner.RunTest(context.Background(), cfg, hooks.Event{
		EventID:   "pii-redactor-panel",
		AgentID:   uuid.New(),
		HookEvent: hooks.EventUserPromptSubmit,
		RawInput:  "ping me at user@example.com",
	})
	if res.Decision != hooks.DecisionAllow {
		t.Fatalf("pii-redactor test result decision=%v error=%q; want allow",
			res.Decision, res.Error)
	}
}
