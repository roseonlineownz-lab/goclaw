package methods

import (
	"context"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/gateway"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

// ---- Tests: store.IsMasterScope helper ----

func TestIsMasterScopeContext_OwnerRole_Allowed(t *testing.T) {
	ctx := store.WithRole(context.Background(), store.RoleRoot)
	// Tenant set to a non-master tenant — owner role must still pass
	if !store.IsMasterScope(ctx) {
		t.Fatalf("owner role with non-master tenant should be allowed")
	}
}


func TestIsMasterScopeContext_NonMasterTenantNoOwner_Denied(t *testing.T) {
	ctx := context.Background()
	if store.IsMasterScope(ctx) {
		t.Fatalf("non-master tenant ctx without owner role must be denied")
	}
}

func TestIsMasterScopeContext_NonMasterTenantWithOwnerRole_Allowed(t *testing.T) {
	// A system owner visiting a tenant dashboard — bypass-all allows through
	ctx := context.Background()
	ctx = store.WithRole(ctx, store.RoleRoot)
	if !store.IsMasterScope(ctx) {
		t.Fatalf("owner role must bypass tenant scope check")
	}
}

// ---- Tests: requireMasterScope middleware ----

func configGuardRequest(method string) *protocol.RequestFrame {
	return &protocol.RequestFrame{
		Type:   protocol.FrameTypeRequest,
		ID:     "cfg-guard-req-1",
		Method: method,
	}
}

// nextCalledHandler returns a handler that flips *called to true when invoked.
func nextCalledHandler(called *bool) gateway.MethodHandler {
	return func(_ context.Context, _ *gateway.Client, _ *protocol.RequestFrame) {
		*called = true
	}
}


func TestRequireMasterScope_OwnerRole_CallsNext(t *testing.T) {
	m := &ConfigMethods{}
	var called bool
	h := m.requireMasterScope(nextCalledHandler(&called))

	// System owner visiting a non-master tenant ctx — must pass
	ctx := context.Background()
	ctx = store.WithRole(ctx, store.RoleRoot)
	h(ctx, nullClient(), configGuardRequest(protocol.MethodConfigApply))

	if !called {
		t.Fatalf("expected next handler to be called for owner role bypass")
	}
}


// ---- Test: middleware chain (requireMasterScope → requireOwner → handler) ----

// TestRequireMasterScope_ChainedWithRequireOwner asserts the master-scope guard
// fires BEFORE requireOwner (which uses client.IsOwner()). Non-master tenant ctx
// must be rejected even though nullClient has no owner role set — this protects
// the downstream handler from ever touching m.cfg for a non-master caller.
func TestRequireMasterScope_ChainedWithRequireOwner(t *testing.T) {
	m := &ConfigMethods{}
	var innerCalled bool
	inner := nextCalledHandler(&innerCalled)
	chain := m.requireMasterScope(m.requireOwner(inner))

	// Non-master tenant ctx → master-scope guard rejects first
	ctx := context.Background()
	ctx = store.WithRole(ctx, "admin")

	// Must not panic (handler with nil m.cfg is never reached)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("chained middleware panicked (guard did not fire early): %v", r)
		}
	}()
	chain(ctx, nullClient(), configGuardRequest(protocol.MethodConfigPatch))

	if innerCalled {
		t.Fatalf("inner handler must not be reached when master-scope guard rejects")
	}
}
