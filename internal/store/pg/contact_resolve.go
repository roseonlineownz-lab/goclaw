package pg

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"
)

const contactResolveCacheTTL = 60 * time.Second

// contactResolveEntry holds a cached user resolution result.
type contactResolveEntry struct {
	tenantUserID string // empty = not merged
	fetched      time.Time
}

// contactResolveCache is a TTL cache for contact→user resolution.
type contactResolveCache struct {
	mu    sync.RWMutex
	items map[string]contactResolveEntry // key: "channelType:senderID"
}

func newContactResolveCache() *contactResolveCache {
	return &contactResolveCache{items: make(map[string]contactResolveEntry)}
}

func (c *contactResolveCache) get(key string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if entry, ok := c.items[key]; ok && time.Since(entry.fetched) < contactResolveCacheTTL {
		return entry.tenantUserID, true
	}
	return "", false
}

func (c *contactResolveCache) set(key, tenantUserID string) {
	c.mu.Lock()
	c.items[key] = contactResolveEntry{tenantUserID: tenantUserID, fetched: time.Now()}
	c.mu.Unlock()
}

// InvalidateContactResolveCache clears all cached contact→user resolutions.
// Call after merge/unmerge operations.
func (s *PGContactStore) InvalidateContactResolveCache() {
	if s.resolveCache == nil {
		return
	}
	s.resolveCache.mu.Lock()
	s.resolveCache.items = make(map[string]contactResolveEntry)
	s.resolveCache.mu.Unlock()
}

// ResolveTenantUserID returns the merged user UUID (as string) for a given
// (channelType, senderID) lookup, or "" if the contact is missing or unmerged.
// Result is cached in-memory for 60s (TTL) and invalidated on merge.
func (s *PGContactStore) ResolveTenantUserID(ctx context.Context, channelType, senderID string) (string, error) {
	if channelType == "" || senderID == "" {
		return "", nil
	}
	key := channelType + ":" + senderID
	if s.resolveCache != nil {
		if hit, ok := s.resolveCache.get(key); ok {
			return hit, nil
		}
	}
	var merged string
	err := s.db.QueryRowContext(ctx,
		`SELECT merged_id::text FROM channel_contacts
		  WHERE channel_type = $1 AND sender_id = $2 AND merged_id IS NOT NULL
		  LIMIT 1`,
		channelType, senderID,
	).Scan(&merged)
	if errors.Is(err, sql.ErrNoRows) {
		if s.resolveCache != nil {
			s.resolveCache.set(key, "")
		}
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if s.resolveCache != nil {
		s.resolveCache.set(key, merged)
	}
	return merged, nil
}
