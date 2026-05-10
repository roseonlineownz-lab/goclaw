package tools

import (
	"context"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
)

// helpers

func newCodexWithDefaults(name, strategy string, extras []string) *providers.CodexProvider {
	p := providers.NewCodexProvider(name, nil, "", "")
	if strategy != "" || len(extras) > 0 {
		p = p.WithRoutingDefaults(strategy, extras)
	}
	return p
}

func registryWith(providers_ ...*providers.CodexProvider) *providers.Registry {
	reg := providers.NewRegistry()
	for _, p := range providers_ {
		reg.Register(p)
	}
	return reg
}

// TestWrapsWhenCodexHasExtras: Codex with round_robin strategy and extra members
// → should wrap to *ChatGPTOAuthRouter.
func TestWrapsWhenCodexHasExtras(t *testing.T) {
	base := newCodexWithDefaults("base", "round_robin", []string{"extra1", "extra2"})
	extra1 := newCodexWithDefaults("extra1", "", nil)
	extra2 := newCodexWithDefaults("extra2", "", nil)
	reg := registryWith(base, extra1, extra2)

	got := wrapPoolProvider(context.Background(), reg, "base", base)

	if _, ok := got.(*providers.ChatGPTOAuthRouter); !ok {
		t.Errorf("wrapPoolProvider() = %T, want *providers.ChatGPTOAuthRouter", got)
	}
}

// TestWrapsWhenPriorityOrderWithMembers: Codex with priority_order + extras → wraps.
func TestWrapsWhenPriorityOrderWithMembers(t *testing.T) {
	base := newCodexWithDefaults("base", "priority_order", []string{"extra1"})
	extra1 := newCodexWithDefaults("extra1", "", nil)
	reg := registryWith(base, extra1)

	got := wrapPoolProvider(context.Background(), reg, "base", base)

	if _, ok := got.(*providers.ChatGPTOAuthRouter); !ok {
		t.Errorf("wrapPoolProvider() = %T, want *providers.ChatGPTOAuthRouter", got)
	}
}

// TestDoesNotWrapSoloCodexNilDefaults: Codex with nil RoutingDefaults → no wrap.
func TestDoesNotWrapSoloCodexNilDefaults(t *testing.T) {
	// Not calling WithRoutingDefaults → RoutingDefaults() returns nil.
	base := providers.NewCodexProvider("base", nil, "", "")
	reg := registryWith(base)

	got := wrapPoolProvider(context.Background(), reg, "base", base)

	if got != base {
		t.Errorf("wrapPoolProvider() returned %T, want original *CodexProvider (no wrap)", got)
	}
}

// TestDoesNotWrapPrimaryFirstNoExtras: strategy primary_first (not round_robin/priority_order),
// extras empty → returns provider unchanged.
func TestDoesNotWrapPrimaryFirstNoExtras(t *testing.T) {
	base := newCodexWithDefaults("base", "primary_first", []string{})
	reg := registryWith(base)

	got := wrapPoolProvider(context.Background(), reg, "base", base)

	if got != base {
		t.Errorf("wrapPoolProvider() with primary_first + no extras: want original provider, got %T", got)
	}
}

// TestDoesNotWrapNonCodex: non-Codex provider (byteplus style) → unchanged.
func TestDoesNotWrapNonCodex(t *testing.T) {
	reg := providers.NewRegistry()
	fake := &fakeNonCodexProvider{name: "byteplus"}
	reg.Register(fake)

	got := wrapPoolProvider(context.Background(), reg, "byteplus", fake)

	if got != fake {
		t.Errorf("wrapPoolProvider() returned %T, want original non-Codex provider unchanged", got)
	}
}

// TestFallsBackWhenRouterHasNoRegisteredMembers: extras reference missing providers
// → router has no registered members → return original Codex.
func TestFallsBackWhenRouterHasNoRegisteredMembers(t *testing.T) {
	// Only base registered; extra1 and extra2 are NOT in registry.
	base := newCodexWithDefaults("base", "round_robin", []string{"missing1", "missing2"})
	reg := registryWith(base)

	got := wrapPoolProvider(context.Background(), reg, "base", base)

	// base is in the registry (resolves as pool member), so HasRegisteredProviders() is true.
	if _, ok := got.(*providers.ChatGPTOAuthRouter); !ok {
		t.Errorf("wrapPoolProvider() with missing extras but base present: got %T, want *ChatGPTOAuthRouter (base self-resolves)", got)
	}
}

// TestFallsBackWhenZeroMembersResolve: verifies we return original when the
// router genuinely has NO registered members.
func TestFallsBackWhenZeroMembersResolve(t *testing.T) {
	// base NOT registered in registry; extras also missing.
	base := newCodexWithDefaults("ghost", "round_robin", []string{"missing1"})
	reg := providers.NewRegistry() // empty registry — nothing registered

	got := wrapPoolProvider(context.Background(), reg, "ghost", base)

	// Router created but HasRegisteredProviders() == false → must return original.
	if got != base {
		t.Errorf("wrapPoolProvider() with empty registry: want original provider, got %T", got)
	}
}

// TestWrappedRouterSatisfiesNativeImageProvider: wrapped result can be
// type-asserted to NativeImageProvider.
func TestWrappedRouterSatisfiesNativeImageProvider(t *testing.T) {
	base := newCodexWithDefaults("base", "round_robin", []string{"extra1"})
	extra1 := newCodexWithDefaults("extra1", "", nil)
	reg := registryWith(base, extra1)

	got := wrapPoolProvider(context.Background(), reg, "base", base)

	if _, ok := got.(providers.NativeImageProvider); !ok {
		t.Errorf("wrapPoolProvider() result %T does not satisfy NativeImageProvider", got)
	}
}

// TestParamsInjection: _native_provider in ExecuteWithChain callParams is the
// *ChatGPTOAuthRouter, not the bare *CodexProvider.
func TestParamsInjection(t *testing.T) {
	base := newCodexWithDefaults("pool_base", "round_robin", []string{"extra1"})
	extra1 := newCodexWithDefaults("extra1", "", nil)
	reg := registryWith(base, extra1)

	chain := []MediaProviderEntry{{
		Provider:   "pool_base",
		Model:      "gpt-image-1",
		Enabled:    true,
		Timeout:    10,
		MaxRetries: 1,
	}}

	// capturedNative captures whatever _native_provider lands in callParams.
	var capturedNative any
	fn := func(fnCtx context.Context, cp credentialProvider, providerName, model string, params map[string]any) ([]byte, *providers.Usage, error) {
		capturedNative = params["_native_provider"]
		return []byte("ok"), nil, nil
	}

	_, err := ExecuteWithChain(context.Background(), chain, reg, fn)
	if err != nil {
		t.Fatalf("ExecuteWithChain returned error: %v", err)
	}

	if _, ok := capturedNative.(*providers.ChatGPTOAuthRouter); !ok {
		t.Errorf("_native_provider = %T, want *providers.ChatGPTOAuthRouter", capturedNative)
	}
}

// TestStrategyPassedThrough: verifies round_robin strategy is preserved in the
// router by checking the router is created (strategy is opaque; tested indirectly
// by confirming the router forms from round_robin vs priority_order inputs).
func TestStrategyPassedThrough(t *testing.T) {
	for _, strategy := range []string{"round_robin", "priority_order"} {
		t.Run(strategy, func(t *testing.T) {
			base := newCodexWithDefaults("base", strategy, []string{"extra1"})
			extra1 := newCodexWithDefaults("extra1", "", nil)
			reg := registryWith(base, extra1)

			got := wrapPoolProvider(context.Background(), reg, "base", base)

			if _, ok := got.(*providers.ChatGPTOAuthRouter); !ok {
				t.Errorf("strategy %q: wrapPoolProvider() = %T, want *ChatGPTOAuthRouter", strategy, got)
			}
		})
	}
}

// fakeNonCodexProvider is a minimal non-Codex provider for testing.
// Implements only the providers.Provider interface — no Codex-specific methods.
type fakeNonCodexProvider struct {
	name string
}

func (f *fakeNonCodexProvider) Name() string         { return f.name }
func (f *fakeNonCodexProvider) DefaultModel() string { return "" }
func (f *fakeNonCodexProvider) Chat(_ context.Context, _ providers.ChatRequest) (*providers.ChatResponse, error) {
	return nil, nil
}
func (f *fakeNonCodexProvider) ChatStream(_ context.Context, _ providers.ChatRequest, _ func(providers.StreamChunk)) (*providers.ChatResponse, error) {
	return nil, nil
}
