package workspace

import (
	"errors"
	"log/slog"
	"strings"

	"github.com/google/uuid"
)

// ErrUserZoneViolation is returned when an agent attempts to access
// `users/{user_key}/...` under another user's zone. It is the LAST line of
// defence — even owners and editors must not cross user-zone boundaries.
var ErrUserZoneViolation = errors.New("workspace: forbidden cross-user-zone access")

// UserKeyResolver resolves a user_key slug to a user UUID. Returns ok=false
// when the key is unknown — callers reject in that case (deny-by-default).
type UserKeyResolver interface {
	LookupUserIDByKey(key string) (uuid.UUID, bool)
}

// EnforceUserZoneAccess inspects a resolved absolute path against the agent's
// workspace root. If the path falls under `users/<user_key>/...` and that
// `<user_key>` does not map back to senderUserID, it returns
// ErrUserZoneViolation.
//
// Paths outside any `users/.../` zone pass through (no opinion). Paths whose
// `<user_key>` is unknown to the resolver are rejected — defence-in-depth
// against typo + race conditions where a user is mid-deletion.
//
// resolvedPath MUST already be a clean absolute path (path traversal handled
// upstream by resolvePathWithAllowed).
func EnforceUserZoneAccess(
	resolvedPath, workspaceRoot string,
	senderUserID uuid.UUID,
	resolver UserKeyResolver,
) error {
	key, ok := extractUserKeyFromPath(resolvedPath, workspaceRoot)
	if !ok {
		return nil // not in any user zone
	}
	if resolver == nil {
		// Defensive: missing resolver means we can't verify — reject.
		slog.Warn("security.privacy_zone_violation",
			"reason", "no_resolver", "path", resolvedPath, "user_key", key)
		return ErrUserZoneViolation
	}
	targetID, found := resolver.LookupUserIDByKey(key)
	if !found {
		slog.Warn("security.privacy_zone_violation",
			"reason", "unknown_user_key", "path", resolvedPath, "user_key", key)
		return ErrUserZoneViolation
	}
	if targetID != senderUserID {
		slog.Warn("security.privacy_zone_violation",
			"reason", "cross_user", "path", resolvedPath,
			"target_user_key", key,
			"sender_user_id", senderUserID.String())
		return ErrUserZoneViolation
	}
	return nil
}

// extractUserKeyFromPath extracts <user_key> when resolvedPath sits under
// `<workspaceRoot>/users/<user_key>/...`. Returns ("", false) for paths
// outside any user zone.
//
// The workspaceRoot may be empty (no scoping) — in that case any `/users/<k>/...`
// segment in the path is treated as a user zone boundary.
func extractUserKeyFromPath(resolvedPath, workspaceRoot string) (string, bool) {
	rel := resolvedPath
	if workspaceRoot != "" && strings.HasPrefix(resolvedPath, workspaceRoot) {
		rel = strings.TrimPrefix(resolvedPath, workspaceRoot)
	}
	rel = strings.TrimPrefix(rel, "/")

	// Look for "users/<key>/..." anywhere in the relative path. The resolved
	// path is already canonical, so a literal "users/" segment with a
	// non-empty key after the next slash is sufficient.
	idx := strings.Index(rel, "users/")
	if idx == -1 {
		return "", false
	}
	// Boundary: the segment must be a directory boundary, not a substring
	// of another folder name (e.g. "myusers/").
	if idx > 0 && rel[idx-1] != '/' {
		return "", false
	}
	tail := rel[idx+len("users/"):]
	slash := strings.Index(tail, "/")
	switch {
	case slash == 0:
		// `users//...` — empty key, treat as malformed (not a zone).
		return "", false
	case slash < 0:
		// Path ends right after `users/<key>` (no trailing slash). Empty tail
		// is just the bare `users/` directory itself — no zone identity.
		if tail == "" {
			return "", false
		}
		return tail, true
	default:
		return tail[:slash], true
	}
}
