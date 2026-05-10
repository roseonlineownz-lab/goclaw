package permissions

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// mockLoader returns a fixed pattern list. errOnLoad makes it error.
type mockLoader struct {
	patterns  []string
	callCount int
}

func (m *mockLoader) GetDenyGlobs(_ context.Context, _ uuid.UUID, _, _ string) ([]string, error) {
	m.callCount++
	return m.patterns, nil
}

func TestGlobCache_CacheHit(t *testing.T) {
	loader := &mockLoader{patterns: []string{".env*"}}
	c := NewGlobCache(0)

	agentID := uuid.New()

	// First call — cache miss → loader invoked
	matched, pat, err := c.Match(context.Background(), loader, agentID, "group:telegram:-100", "42", ".env.local")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !matched || pat != ".env*" {
		t.Fatalf("expected match .env*, got matched=%v pat=%q", matched, pat)
	}
	if loader.callCount != 1 {
		t.Fatalf("loader should be called once, got %d", loader.callCount)
	}

	// Second call — cache hit → loader NOT invoked again
	matched2, _, err2 := c.Match(context.Background(), loader, agentID, "group:telegram:-100", "42", ".env.local")
	if err2 != nil {
		t.Fatalf("unexpected error: %v", err2)
	}
	if !matched2 {
		t.Fatal("expected match on cache hit")
	}
	if loader.callCount != 1 {
		t.Fatalf("loader should still be 1 on cache hit, got %d", loader.callCount)
	}
}

func TestGlobCache_TTLExpiry(t *testing.T) {
	loader := &mockLoader{patterns: []string{".env*"}}
	c := NewGlobCache(0)
	agentID := uuid.New()

	// Plant an already-expired entry directly.
	k := cacheKey{agentID: agentID, scope: "group:tg:-1", userID: "99"}
	c.mu.Lock()
	c.entries[k] = compiledEntry{
		patterns:  []string{".env*"},
		expiresAt: time.Now().Add(-1 * time.Second), // already expired
	}
	c.lruKeys = append(c.lruKeys, k)
	c.mu.Unlock()

	// Match should bypass expired cache and call loader
	_, _, err := c.Match(context.Background(), loader, agentID, "group:tg:-1", "99", ".env.local")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if loader.callCount != 1 {
		t.Fatalf("expected loader called once after TTL expiry, got %d", loader.callCount)
	}
}

func TestGlobCache_LRUEvict(t *testing.T) {
	loader := &mockLoader{patterns: []string{}}
	c := NewGlobCache(2) // tiny max

	// Fill to capacity
	a1, a2 := uuid.New(), uuid.New()
	c.Match(context.Background(), loader, a1, "scope", "u1", "foo.txt") //nolint:errcheck
	c.Match(context.Background(), loader, a2, "scope", "u1", "foo.txt") //nolint:errcheck

	if len(c.entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(c.entries))
	}

	// Third unique agent → evicts oldest (a1)
	a3 := uuid.New()
	c.Match(context.Background(), loader, a3, "scope", "u1", "foo.txt") //nolint:errcheck

	if len(c.entries) != 2 {
		t.Fatalf("expected 2 after evict, got %d", len(c.entries))
	}
	k1 := cacheKey{agentID: a1, scope: "scope", userID: "u1"}
	if _, ok := c.entries[k1]; ok {
		t.Fatal("oldest entry (a1) should have been evicted")
	}
}

func TestGlobCache_Invalidate(t *testing.T) {
	loader := &mockLoader{patterns: []string{".env*"}}
	c := NewGlobCache(0)

	agentID := uuid.New()
	other := uuid.New()

	// Populate two agents
	c.Match(context.Background(), loader, agentID, "s", "u", "x") //nolint:errcheck
	c.Match(context.Background(), loader, other, "s", "u", "x")   //nolint:errcheck
	if len(c.entries) != 2 {
		t.Fatalf("expected 2 entries before invalidate, got %d", len(c.entries))
	}

	c.Invalidate(agentID)

	if _, ok := c.entries[cacheKey{agentID: agentID, scope: "s", userID: "u"}]; ok {
		t.Fatal("agentID entry should be removed after Invalidate")
	}
	if _, ok := c.entries[cacheKey{agentID: other, scope: "s", userID: "u"}]; !ok {
		t.Fatal("other agent entry should survive Invalidate")
	}
}

func TestGlobCache_NoMatch(t *testing.T) {
	loader := &mockLoader{patterns: []string{".env*", "secrets/**"}}
	c := NewGlobCache(0)

	matched, pat, err := c.Match(context.Background(), loader, uuid.New(), "s", "u", "src/main.go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if matched {
		t.Fatalf("expected no match for src/main.go, got pattern=%q", pat)
	}
}

func TestMatchPatterns_Baseline(t *testing.T) {
	baseline := []string{".env*", "secrets/**", ".git/**", "*.key", "*.pem"}
	cases := []struct {
		path    string
		wantHit bool
	}{
		{".env", true},
		{".env.local", true},
		{".env.production", true},
		{"secrets/api.txt", true},
		{"secrets/nested/key.json", true},
		{"secrets/.hidden", true},
		{".git/config", true},
		{".git/objects/pack/file", true},
		{"id_rsa.key", true},
		{"cert.pem", true},
		{"src/main.go", false},
		{"README.md", false},
		{"config/app.json", false},
	}

	for _, tc := range cases {
		matched, pat, err := matchPatterns(baseline, tc.path)
		if err != nil {
			t.Errorf("%s: unexpected error %v", tc.path, err)
			continue
		}
		if matched != tc.wantHit {
			t.Errorf("%s: got matched=%v (pat=%q), want %v", tc.path, matched, pat, tc.wantHit)
		}
	}
}
