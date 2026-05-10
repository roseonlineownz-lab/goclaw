package permissions

import (
	"context"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// globCacheInstance is the process-wide singleton glob cache shared across all tools.
// Bounded LRU (256 entries), 60s TTL — same as permCacheEntry lifecycle.
var globCacheInstance = NewGlobCache(0)

// matchGlob matches a single glob pattern against relPath using doublestar semantics.
// Returns false on malformed patterns (fail-safe: don't block on broken config).
func matchGlob(pattern, relPath string) bool {
	matched, _, _ := matchPatterns([]string{pattern}, relPath)
	return matched
}

// CheckDenyGlobs returns an error if relPath matches any of the agent's deny glob
// patterns. Call this BEFORE interceptors and disk writes — deny-glob is a
// security baseline that overrides any granted permission regardless of context.
//
// relPath must be workspace-relative with forward slashes and no leading '/'.
// Patterns are loaded from permStore.GetDenyGlobs and cached per (agentID, scope, userID)
// with a 60s TTL. The cache is invalidated when an admin writes a new grant row via the
// existing permStore.Invalidate hook (see HookGlobCacheInvalidate).
//
// When permStore is nil or no agent is set in context, falls back to matching
// against DefaultDenyGlobs to ensure baseline protection is always active.
//
// Returns nil when:
//   - relPath is empty
//   - relPath does not match any deny pattern (agent-configured or default)
//
// Returns *ErrDenyGlobMatch when relPath matches a pattern.
func CheckDenyGlobs(ctx context.Context, permStore store.ConfigPermissionStore, relPath string) error {
	if relPath == "" {
		return nil
	}

	agentID := store.AgentIDFromContext(ctx)

	// When permStore is available and an agent is identified, use the full
	// cache-backed lookup which merges agent-configured globs with defaults.
	if permStore != nil && agentID != uuid.Nil {
		scope := store.UserIDFromContext(ctx)
		// Extract numeric sender ID (strips the "|platform" suffix some channels append).
		senderID := store.SenderIDFromContext(ctx)
		numericID := strings.SplitN(senderID, "|", 2)[0]

		matched, pat, err := globCacheInstance.Match(ctx, permStore, agentID, scope, numericID, relPath)
		if err != nil {
			// Cache/store error: fall through to default globs below rather than
			// fail-open entirely — baseline protection must still apply.
			slog.WarnContext(ctx, "security.deny_glob_cache_err",
				"agent_id", agentID,
				"err", err,
			)
		} else {
			if matched {
				slog.WarnContext(ctx, "security.deny_glob_block",
					"path", relPath,
					"pattern", pat,
					"agent_id", agentID,
					"scope", scope,
				)
				return &ErrDenyGlobMatch{Pattern: pat, Path: relPath}
			}
			return nil
		}
	}

	// Fallback: no permStore or cache error — apply the baseline deny globs.
	// This ensures DM, web, and desktop contexts always block sensitive paths even
	// without a full permission store wired in.
	for _, pat := range store.DefaultDenyGlobs {
		if matchGlob(pat, relPath) {
			slog.WarnContext(ctx, "security.deny_glob_block_default",
				"path", relPath,
				"pattern", pat,
				"agent_id", agentID,
			)
			return &ErrDenyGlobMatch{Pattern: pat, Path: relPath}
		}
	}
	return nil
}

// HookGlobCacheInvalidate returns an invalidation function that drops all glob cache
// entries for agentID. Wire this into permStore's post-grant hook so admin extensions
// to deny_globs take effect within the current RPC round-trip rather than waiting for
// the 60s TTL to expire.
//
// Usage:
//
//	permStore.OnGrant(permissions.HookGlobCacheInvalidate())
func HookGlobCacheInvalidate() func(agentID uuid.UUID) {
	return globCacheInstance.Invalidate
}
