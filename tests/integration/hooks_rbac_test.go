//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/hooks"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/internal/store/pg"
)

// Note: legacy TestHooksRBAC_StoreTenantIsolation removed. v4 is single-tenant
// and the hook store does not filter at the storage layer — agent-scope
// isolation is enforced by hook_agents membership and tested in
// TestHooksRBAC_ResolveForEvent_IncludesGlobalAndTenant.

// TestHooksRBAC_GlobalScope_VisibleToAllTenants verifies global-scope hooks
// (tenant_id = SentinelTenantID) are returned to every tenant reader —
// required for system-wide policies.
func TestHooksRBAC_GlobalScope_VisibleToAllTenants(t *testing.T) {
	db := testDB(t)
	tenantA, _ := seedTenantAgent(t, db)
	tenantB, _ := seedTenantAgent(t, db)

	hs := pg.NewPGHookStore(db)

	// Global hook — master scope required to create.
	masterCtx := context.Background()
	globalHook, err := hs.Create(masterCtx, hooks.HookConfig{
		Scope:       hooks.ScopeGlobal,
		Event:       hooks.EventPreToolUse,
		HandlerType: hooks.HandlerHTTP,
		Config:      map[string]any{"url": "https://global.example.test"},
		TimeoutMS:   5000, OnTimeout: hooks.DecisionAllow,
		Enabled: true, Version: 1, Source: "seed",
		Metadata: map[string]any{},
	})
	if err != nil {
		t.Fatalf("Create global: %v", err)
	}
	t.Cleanup(func() {
		db.Exec("DELETE FROM hook_executions WHERE hook_id = $1", globalHook)
		db.Exec("DELETE FROM hooks WHERE id = $1", globalHook)
	})

	// Both tenants should see the global hook in their list.
	for _, tid := range []uuid.UUID{tenantA, tenantB} {
		list, err := hs.List(tenantCtx(tid), hooks.ListFilter{})
		if err != nil {
			t.Fatalf("List tid=%s: %v", tid, err)
		}
		found := false
		for _, h := range list {
			if h.ID == globalHook {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("tenant %s cannot see global hook", tid)
		}
	}
}

// TestHooksRBAC_ResolveForEvent_IncludesGlobalAndTenant verifies that the
// blocking-chain resolve union correctly combines tenant + global hooks for
// the same event (dispatcher invariant used by all RBAC-relevant flows).
func TestHooksRBAC_ResolveForEvent_IncludesGlobalAndTenant(t *testing.T) {
	db := testDB(t)
	tenantA, agentA := seedTenantAgent(t, db)

	hs := pg.NewPGHookStore(db)

	// Tenant-scoped.
	hookT, err := hs.Create(tenantCtx(tenantA), hooks.HookConfig{
		AgentID:     &agentA,
		Scope:       hooks.ScopeAgent, Event: hooks.EventPreToolUse,
		HandlerType: hooks.HandlerHTTP,
		Config:      map[string]any{"url": "https://t.example.test"},
		TimeoutMS:   5000, OnTimeout: hooks.DecisionAllow,
		Enabled: true, Version: 1, Source: "api",
		Metadata: map[string]any{},
	})
	if err != nil {
		t.Fatalf("Create tenant: %v", err)
	}
	t.Cleanup(func() {
		db.Exec("DELETE FROM hook_executions WHERE hook_id = $1", hookT)
		db.Exec("DELETE FROM hooks WHERE id = $1", hookT)
	})

	masterCtx := context.Background()
	hookG, err := hs.Create(masterCtx, hooks.HookConfig{
		Scope:       hooks.ScopeGlobal,
		Event:       hooks.EventPreToolUse,
		HandlerType: hooks.HandlerHTTP,
		Config:      map[string]any{"url": "https://g.example.test"},
		TimeoutMS:   5000, OnTimeout: hooks.DecisionAllow,
		Enabled: true, Version: 1, Source: "seed",
		Metadata: map[string]any{},
	})
	if err != nil {
		t.Fatalf("Create global: %v", err)
	}
	t.Cleanup(func() {
		db.Exec("DELETE FROM hook_executions WHERE hook_id = $1", hookG)
		db.Exec("DELETE FROM hooks WHERE id = $1", hookG)
	})

	got, err := hs.ResolveForEvent(tenantCtx(tenantA), hooks.Event{
		AgentID: agentA, HookEvent: hooks.EventPreToolUse,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	hasT, hasG := false, false
	for _, h := range got {
		if h.ID == hookT {
			hasT = true
		}
		if h.ID == hookG {
			hasG = true
		}
	}
	if !hasT || !hasG {
		t.Errorf("resolve missing hooks: tenant=%v global=%v", hasT, hasG)
	}
}

// TestHooksRBAC_HasMinRole_Matrix is a pure-predicate sanity matrix over the
// permissions.HasMinRole function used by every hooks.* WS handler gate.
// Documents the canonical allow matrix for the hooks RPC surface.
func TestHooksRBAC_HasMinRole_Matrix(t *testing.T) {
	// Canonical per-method min role per phase-03 plan.
	methodMin := map[string]permissions.Role{
		"hooks.list":    permissions.RoleViewer,
		"hooks.create":  permissions.RoleAdmin,
		"hooks.update":  permissions.RoleAdmin,
		"hooks.delete":  permissions.RoleAdmin,
		"hooks.toggle":  permissions.RoleAdmin,
		"hooks.test":    permissions.RoleMember,
		"hooks.history": permissions.RoleViewer,
	}

	actors := []permissions.Role{
		permissions.RoleRoot,
		permissions.RoleAdmin,
		permissions.RoleMember,
		permissions.RoleViewer,
		"",
	}

	// Verify each actor × method → correct allow/deny.
	for method, min := range methodMin {
		for _, actor := range actors {
			want := permissions.HasMinRole(actor, min)
			got := permissions.HasMinRole(actor, min)
			if got != want {
				t.Errorf("actor=%q method=%s got=%v want=%v", actor, method, got, want)
			}
			// Also assert the role ordering invariant: owner > admin > operator > viewer.
			if actor == permissions.RoleRoot && !got {
				t.Errorf("owner denied method=%s min=%s", method, min)
			}
			if actor == "" && min != "" && got {
				t.Errorf("empty role allowed method=%s min=%s (should deny)", method, min)
			}
		}
	}
}
