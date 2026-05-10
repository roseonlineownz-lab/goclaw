package cache

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/bus"
)

// agentAccessEntry caches agent access check results.
type agentAccessEntry struct {
	Allowed bool
	Role    string
}

// PermissionCache provides short-TTL caching for hot permission lookups.
// Uses InMemoryCache[V] caches with pubsub invalidation.
type PermissionCache struct {
	agentAccess *InMemoryCache[agentAccessEntry]
	teamAccess  *InMemoryCache[bool]
}

// permissionCacheSweepInterval and permissionCacheMaxSize bound background
// growth of per-user cache entries. Without these, long-running gateways with
// many distinct users would accumulate unbounded entries (agent access,
// team access) even with a 30s TTL — lazy eviction only fires on Get,
// so entries for disconnected users never get reclaimed.
const (
	permissionCacheSweepInterval = 60 * time.Second
	permissionCacheMaxSize       = 10_000
)

// NewPermissionCache creates a new permission cache with periodic sweep
// goroutines for the inner caches. Call Close() on gateway shutdown to
// stop the sweep goroutines.
func NewPermissionCache() *PermissionCache {
	return &PermissionCache{
		agentAccess: NewInMemoryCache[agentAccessEntry](
			WithSweepInterval[agentAccessEntry](permissionCacheSweepInterval),
			WithMaxSize[agentAccessEntry](permissionCacheMaxSize),
		),
		teamAccess: NewInMemoryCache[bool](
			WithSweepInterval[bool](permissionCacheSweepInterval),
			WithMaxSize[bool](permissionCacheMaxSize),
		),
	}
}

// Close stops all background sweep goroutines. Safe to call multiple times.
func (pc *PermissionCache) Close() {
	pc.agentAccess.Close()
	pc.teamAccess.Close()
}

const (
	agentAccessTTL = 30 * time.Second
	teamAccessTTL  = 30 * time.Second
)

// --- Agent Access ---

func (pc *PermissionCache) GetAgentAccess(ctx context.Context, agentID uuid.UUID, userID string) (bool, string, bool) {
	entry, ok := pc.agentAccess.Get(ctx, agentID.String()+":"+userID)
	if !ok {
		return false, "", false
	}
	return entry.Allowed, entry.Role, true
}

func (pc *PermissionCache) SetAgentAccess(ctx context.Context, agentID uuid.UUID, userID string, allowed bool, role string) {
	pc.agentAccess.Set(ctx, agentID.String()+":"+userID, agentAccessEntry{Allowed: allowed, Role: role}, agentAccessTTL)
}

// --- Team Access ---

func (pc *PermissionCache) GetTeamAccess(ctx context.Context, teamID uuid.UUID, userID string) (bool, bool) {
	return pc.teamAccess.Get(ctx, teamID.String()+":"+userID)
}

func (pc *PermissionCache) SetTeamAccess(ctx context.Context, teamID uuid.UUID, userID string, allowed bool) {
	pc.teamAccess.Set(ctx, teamID.String()+":"+userID, allowed, teamAccessTTL)
}

// --- Invalidation ---

// HandleInvalidation processes a cache invalidation event from the bus.
func (pc *PermissionCache) HandleInvalidation(p bus.CacheInvalidatePayload) {
	slog.Debug("perm_cache.invalidated", "kind", string(p.Kind), "key", p.Key)
	ctx := context.Background()
	switch p.Kind {
	case bus.CacheKindAgentAccess:
		// Key is agentID — delete all access entries for this agent.
		if p.Key != "" {
			pc.agentAccess.DeleteByPrefix(ctx, p.Key+":")
		} else {
			pc.agentAccess.Clear(ctx)
		}
	case bus.CacheKindTeamAccess:
		// Key is teamID — delete all access entries for this team.
		if p.Key != "" {
			pc.teamAccess.DeleteByPrefix(ctx, p.Key+":")
		} else {
			pc.teamAccess.Clear(ctx)
		}
	}
}
