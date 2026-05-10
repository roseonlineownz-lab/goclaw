package permissions

import (
	"context"
	"sync"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/google/uuid"
)

const globCacheTTL = 60 * time.Second
const globCacheMax = 256

// DenyGlobLoader is the store-side interface for fetching deny glob patterns.
// Satisfied by store.ConfigPermissionStore.GetDenyGlobs.
type DenyGlobLoader interface {
	GetDenyGlobs(ctx context.Context, agentID uuid.UUID, scope, userID string) ([]string, error)
}

// ErrDenyGlobMatch is returned when a path matches one of the deny glob patterns.
// The offending pattern and path are embedded for audit logging.
type ErrDenyGlobMatch struct {
	Pattern string
	Path    string
}

func (e *ErrDenyGlobMatch) Error() string {
	return "permission denied: path matches deny glob pattern " + e.Pattern + " (path: " + e.Path + ")"
}

type compiledEntry struct {
	patterns  []string
	expiresAt time.Time
}

type cacheKey struct {
	agentID uuid.UUID
	scope   string
	userID  string
}

// GlobCache compiles and caches deny glob patterns per (agentID, scope, userID).
// TTL = 60s (mirrors existing permCacheEntry). LRU-evicts when entries exceed max.
type GlobCache struct {
	mu      sync.RWMutex
	entries map[cacheKey]compiledEntry
	lruKeys []cacheKey // insertion order for eviction
	max     int
}

// NewGlobCache creates a cache with the given max entry count.
func NewGlobCache(max int) *GlobCache {
	if max <= 0 {
		max = globCacheMax
	}
	return &GlobCache{
		entries: make(map[cacheKey]compiledEntry, max),
		max:     max,
	}
}

// Match returns (matched, matchedPattern, error). A match means the relPath
// is covered by one of the deny glob patterns and should be DENIED.
func (c *GlobCache) Match(ctx context.Context, loader DenyGlobLoader, agentID uuid.UUID, scope, userID, relPath string) (bool, string, error) {
	k := cacheKey{agentID: agentID, scope: scope, userID: userID}

	c.mu.RLock()
	if entry, ok := c.entries[k]; ok && time.Now().Before(entry.expiresAt) {
		patterns := entry.patterns
		c.mu.RUnlock()
		return matchPatterns(patterns, relPath)
	}
	c.mu.RUnlock()

	// Load from store.
	patterns, err := loader.GetDenyGlobs(ctx, agentID, scope, userID)
	if err != nil {
		// Fail-safe: return baseline patterns so the deny layer still works.
		patterns = []string{".env*", "secrets/**", ".git/**", "*.key", "*.pem"}
	}

	c.mu.Lock()
	// Evict oldest when at capacity.
	if len(c.entries) >= c.max && len(c.lruKeys) > 0 {
		oldest := c.lruKeys[0]
		c.lruKeys = c.lruKeys[1:]
		delete(c.entries, oldest)
	}
	c.entries[k] = compiledEntry{
		patterns:  patterns,
		expiresAt: time.Now().Add(globCacheTTL),
	}
	c.lruKeys = append(c.lruKeys, k)
	c.mu.Unlock()

	return matchPatterns(patterns, relPath)
}

// Invalidate removes all cached entries for the given agent, forcing a reload
// on the next call. Called when admin extends deny_globs via grant RPC.
func (c *GlobCache) Invalidate(agentID uuid.UUID) {
	c.mu.Lock()
	defer c.mu.Unlock()

	newKeys := c.lruKeys[:0]
	for _, k := range c.lruKeys {
		if k.agentID == agentID {
			delete(c.entries, k)
		} else {
			newKeys = append(newKeys, k)
		}
	}
	c.lruKeys = newKeys
}

// matchPatterns checks relPath against each pattern using doublestar.PathMatch.
// Returns (true, matchedPattern, nil) on first match; (false, "", nil) if no match.
func matchPatterns(patterns []string, relPath string) (bool, string, error) {
	for _, pat := range patterns {
		matched, err := doublestar.PathMatch(pat, relPath)
		if err != nil {
			continue // malformed pattern — skip
		}
		if matched {
			return true, pat, nil
		}
	}
	return false, "", nil
}
