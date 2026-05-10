package tools

import (
	"context"
	"testing"
	"time"
)

// TestResolveChain_CacheHit verifies back-to-back calls return the cached chain.
func TestResolveChain_CacheHit(t *testing.T) {
	fake := newFakeSecretsStore()
	tool := &WebSearchTool{
		secrets:    fake,
		cache:      newWebCache(defaultCacheMaxEntries, defaultCacheTTL),
		chainCache: newWebSearchChainCache(),
	}

	ctx := context.Background()
	fake.Set(ctx, "tools.web.brave.api_key", "test-key-brave")

	chain1 := tool.resolveChain(ctx)
	chain2 := tool.resolveChain(ctx)

	if len(chain1) == 0 || len(chain2) == 0 {
		t.Fatalf("expected non-empty chains, got %d and %d", len(chain1), len(chain2))
	}
	if len(chain1) != len(chain2) {
		t.Errorf("chain length mismatch: %d vs %d", len(chain1), len(chain2))
	}
	for i := range chain1 {
		if chain1[i].Name() != chain2[i].Name() {
			t.Errorf("provider %d: %s vs %s", i, chain1[i].Name(), chain2[i].Name())
		}
	}
}

// TestResolveChain_CacheInvalidation verifies invalidation refreshes the chain.
func TestResolveChain_CacheInvalidation(t *testing.T) {
	fake := newFakeSecretsStore()
	tool := &WebSearchTool{
		secrets:    fake,
		cache:      newWebCache(defaultCacheMaxEntries, defaultCacheTTL),
		chainCache: newWebSearchChainCache(),
	}

	ctx := context.Background()

	// Initial state: only DDG (no API keys configured).
	chain1 := tool.resolveChain(ctx)
	if len(chain1) != 1 || chain1[0].Name() != "duckduckgo" {
		t.Fatalf("initial chain should be DDG only, got %v", chainNames(chain1))
	}

	// Add a provider key and invalidate the cached chain.
	fake.Set(ctx, "tools.web.brave.api_key", "test-key-brave-new")
	tool.chainCache.Invalidate()

	// Next resolve should pick up the new key.
	chain2 := tool.resolveChain(ctx)
	if len(chain2) != 2 {
		t.Errorf("after invalidation, expected 2 providers, got %d", len(chain2))
	}
	foundBrave := false
	for _, p := range chain2 {
		if p.Name() == "brave" {
			foundBrave = true
			break
		}
	}
	if !foundBrave {
		t.Error("after invalidation, Brave should be in chain")
	}
}

// TestResolveChain_TTLCacheExpiry verifies expired entries trigger refresh.
func TestResolveChain_TTLCacheExpiry(t *testing.T) {
	fake := newFakeSecretsStore()
	tool := &WebSearchTool{
		secrets:    fake,
		cache:      newWebCache(defaultCacheMaxEntries, defaultCacheTTL),
		chainCache: newWebSearchChainCache(),
	}
	ctx := context.Background()

	// Initial state: DDG only.
	chain1 := tool.resolveChain(ctx)
	if len(chain1) != 1 {
		t.Fatalf("initial chain should be 1 provider, got %d", len(chain1))
	}

	// Force-expire the cached entry by rewinding the stored expiry.
	tool.chainCache.mu.Lock()
	tool.chainCache.expires = time.Now().Add(-1 * time.Second)
	tool.chainCache.mu.Unlock()

	// Add provider and resolve again — should bypass expired cache.
	fake.Set(ctx, "tools.web.brave.api_key", "test-key-brave-after-ttl")
	chain2 := tool.resolveChain(ctx)
	if len(chain2) != 2 {
		t.Errorf("after TTL expiry, expected 2 providers, got %d", len(chain2))
	}
}
