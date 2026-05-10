package tools

import (
	"sync"
	"testing"
	"time"
)

// TestWebSearchChainCache_TTLExpiry verifies the entry expires after TTL.
// Uses an injected clock to avoid time.Sleep.
func TestWebSearchChainCache_TTLExpiry(t *testing.T) {
	c := newWebSearchChainCache()

	fakeNow := time.Unix(1000, 0)
	c.now = func() time.Time { return fakeNow }

	chain := []SearchProvider{&fakeSearchProvider{"brave"}}

	c.Set(chain)

	if got, ok := c.Get(); !ok || len(got) != 1 {
		t.Error("expected cache hit at t=0")
	}

	// Within TTL.
	fakeNow = time.Unix(1030, 0)
	if got, ok := c.Get(); !ok || len(got) != 1 {
		t.Error("expected cache hit at t+30s")
	}

	// Past TTL.
	fakeNow = time.Unix(1061, 0)
	if _, ok := c.Get(); ok {
		t.Error("expected cache miss after TTL expiry")
	}
}

// TestWebSearchChainCache_Invalidate verifies the entry is dropped.
func TestWebSearchChainCache_Invalidate(t *testing.T) {
	c := newWebSearchChainCache()
	c.Set([]SearchProvider{&fakeSearchProvider{"brave"}})

	if _, ok := c.Get(); !ok {
		t.Error("expected cache hit before invalidate")
	}

	c.Invalidate()

	if _, ok := c.Get(); ok {
		t.Error("expected cache miss after invalidate")
	}
}

// TestWebSearchChainCache_OverwriteRefreshesEntry verifies Set replaces the chain.
func TestWebSearchChainCache_OverwriteRefreshesEntry(t *testing.T) {
	c := newWebSearchChainCache()
	c.Set([]SearchProvider{&fakeSearchProvider{"brave"}})
	c.Set([]SearchProvider{&fakeSearchProvider{"exa"}})

	got, ok := c.Get()
	if !ok || len(got) != 1 || got[0].Name() != "exa" {
		t.Errorf("expected overwrite to replace chain, got %+v ok=%v", got, ok)
	}
}

// TestWebSearchChainCache_ConcurrentReaders verifies race-safe reads.
func TestWebSearchChainCache_ConcurrentReaders(t *testing.T) {
	c := newWebSearchChainCache()
	c.Set([]SearchProvider{&fakeSearchProvider{"brave"}, &fakeSearchProvider{"exa"}})

	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			for range 100 {
				got, ok := c.Get()
				if !ok || len(got) != 2 {
					t.Errorf("concurrent read failed")
				}
			}
		})
	}
	wg.Wait()
}

// TestWebSearchChainCache_ConcurrentMutations verifies race-safe writes.
func TestWebSearchChainCache_ConcurrentMutations(t *testing.T) {
	c := newWebSearchChainCache()
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			for j := range 10 {
				c.Set([]SearchProvider{&fakeSearchProvider{"brave"}})
				c.Get()
				if j%3 == 0 {
					c.Invalidate()
				}
			}
		})
	}
	wg.Wait()
}
