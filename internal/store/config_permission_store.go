package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Config type constants for agent_config_permissions.config_type column.
//
// Valid values: write_file, edit_file, delete_file, cron, heartbeat, *.
// The legacy "file_writer" value has been split into the three file gates above.
const (
	ConfigTypeWriteFile  = "write_file"  // Create new files in group context
	ConfigTypeEditFile   = "edit_file"   // Modify existing files in group context; also gates protected-file reads
	ConfigTypeDeleteFile = "delete_file" // Remove files in group context
	ConfigTypeHeartbeat  = "heartbeat"   // Heartbeat config access (active gate at heartbeat.go)
	ConfigTypeCron       = "cron"        // Cron job management access
)

// ConfigPermission represents an allow/deny rule for agent configuration.
type ConfigPermission struct {
	ID         uuid.UUID       `json:"id" db:"id"`
	AgentID    uuid.UUID       `json:"agentId" db:"agent_id"`
	Scope      string          `json:"scope" db:"scope"`           // "agent" | "group:telegram:-100456" | "group:*" | "*"
	ConfigType string          `json:"configType" db:"config_type"` // "write_file" | "edit_file" | "delete_file" | "cron" | "heartbeat" | "*"
	UserID     string          `json:"userId" db:"user_id"`
	Permission string          `json:"permission" db:"permission"` // "allow" | "deny"
	GrantedBy  *string         `json:"grantedBy,omitempty" db:"granted_by"`
	Metadata   json.RawMessage `json:"metadata,omitempty" db:"metadata"`
	DenyGlobs  []string        `json:"denyGlobs,omitempty" db:"deny_globs"` // per-grant deny glob patterns
	CreatedAt  time.Time       `json:"createdAt" db:"created_at"`
	UpdatedAt  time.Time       `json:"updatedAt" db:"updated_at"`
}

// ConfigPermissionStore manages agent configuration permissions with wildcard scope matching.
type ConfigPermissionStore interface {
	// CheckPermission checks if a user has permission for a given config action.
	// Evaluates deny rules first, then allow rules, using Go-level wildcard matching.
	CheckPermission(ctx context.Context, agentID uuid.UUID, scope, configType, userID string) (bool, error)

	Grant(ctx context.Context, perm *ConfigPermission) error
	Revoke(ctx context.Context, agentID uuid.UUID, scope, configType, userID string) error
	// List returns permissions for agentID+configType. If scope != "" only rows with that scope are returned.
	List(ctx context.Context, agentID uuid.UUID, configType, scope string) ([]ConfigPermission, error)
	// ListWriters returns cached allow permissions for a given agentID+scope+configType (hot-path).
	// Post-split semantic: callers pass ConfigTypeEditFile for the "writers" surface
	// (edit_file is the broadest practical write authority granted via /addwriter).
	ListWriters(ctx context.Context, agentID uuid.UUID, scope string, configType string) ([]ConfigPermission, error)
	// GetDenyGlobs returns the deduplicated union of deny_globs across all grant rows
	// matching (agentID, scope, userID). Returns baseline patterns when no row matches.
	GetDenyGlobs(ctx context.Context, agentID uuid.UUID, scope, userID string) ([]string, error)
}

// DefaultDenyGlobs is the baseline set of deny glob patterns applied to every
// agent_config_permissions row. These protect common secret/dotfile paths even
// when an agent has a broad folder-level write grant. Admin can extend per-agent
// via the existing grant RPC (config patch). DENY overrides grant — non-bypassable.
var DefaultDenyGlobs = []string{".env*", "secrets/**", ".git/**", "*.key", "*.pem"}

// checkAction is the shared scaffold for all file/cron gate functions.
// Policy: group/guild context only; admin bypass; synthetic sender block; DB lookup.
func checkAction(ctx context.Context, permStore ConfigPermissionStore, configType string) error {
	if permStore == nil {
		return nil
	}
	userID := UserIDFromContext(ctx)
	if !strings.HasPrefix(userID, "group:") && !strings.HasPrefix(userID, "guild:") {
		return nil // not a group context — allow
	}
	agentID := AgentIDFromContext(ctx)
	if agentID == uuid.Nil {
		return nil // no agent context
	}
	// RBAC bypass: admin / operator / owner roles are pre-authenticated by the
	// tenant RBAC system. Per-sender grants exist to gate random group members;
	// authenticated admins should not trip over them for legitimate dispatched work.
	if isAdminRole(ctx) {
		return nil
	}
	senderID := SenderIDFromContext(ctx)
	if senderID == "" || isSyntheticSender(senderID) {
		return fmt.Errorf("permission denied: system context cannot perform %s in group chats. If this is a legitimate user action, ensure the acting sender is preserved through the tool chain", configType)
	}
	numericID := strings.SplitN(senderID, "|", 2)[0]
	allowed, err := permStore.CheckPermission(ctx, agentID, userID, configType, numericID)
	if err != nil {
		return nil // fail-open on DB error only (availability)
	}
	if !allowed {
		return fmt.Errorf("permission denied: only authorized users can %s in this group. Use /addwriter to request access", configType)
	}
	return nil
}

// CheckWriteFilePermission gates creation of new files in group/guild contexts.
// Returns nil when write is allowed.
func CheckWriteFilePermission(ctx context.Context, permStore ConfigPermissionStore) error {
	return checkAction(ctx, permStore, ConfigTypeWriteFile)
}

// CheckEditFilePermission gates modification of existing files in group/guild contexts.
// Also doubles as the read-gate for protected files (SOUL.md, AGENTS.md): ability to
// modify implies read access; non-editors receive a redacted stub.
// Returns nil when edit is allowed.
func CheckEditFilePermission(ctx context.Context, permStore ConfigPermissionStore) error {
	return checkAction(ctx, permStore, ConfigTypeEditFile)
}

// CheckDeleteFilePermission gates removal of files in group/guild contexts.
// Returns nil when delete is allowed.
func CheckDeleteFilePermission(ctx context.Context, permStore ConfigPermissionStore) error {
	return checkAction(ctx, permStore, ConfigTypeDeleteFile)
}

// isAdminRole reports whether ctx carries an elevated RBAC role
// (admin / operator / owner) that should bypass per-user file gates.
func isAdminRole(ctx context.Context) bool {
	switch RoleFromContext(ctx) {
	case "admin", "operator", RoleRoot:
		return true
	}
	return false
}

// isSyntheticSender reports whether senderID is an internal system component
// (not a real user). Mirrors bus.IsInternalSender — kept here to avoid the
// store→bus import dependency. If prefixes change, update both.
func isSyntheticSender(senderID string) bool {
	return strings.HasPrefix(senderID, "system:") ||
		strings.HasPrefix(senderID, "notification:") ||
		strings.HasPrefix(senderID, "teammate:") ||
		strings.HasPrefix(senderID, "ticker:") ||
		strings.HasPrefix(senderID, "subagent:") ||
		senderID == "session_send_tool"
}

// CheckCronPermission returns an error if the caller is in a group context
// and does not have cron or edit_file permission. Returns nil if allowed.
// Same sender-policy as CheckEditFilePermission (see that fn's docstring).
func CheckCronPermission(ctx context.Context, permStore ConfigPermissionStore) error {
	if permStore == nil {
		return nil
	}
	userID := UserIDFromContext(ctx)
	if !strings.HasPrefix(userID, "group:") && !strings.HasPrefix(userID, "guild:") {
		return nil // not a group context
	}
	agentID := AgentIDFromContext(ctx)
	if agentID == uuid.Nil {
		return nil // no agent context
	}
	if isAdminRole(ctx) {
		return nil // RBAC bypass (admin/operator/owner)
	}
	senderID := SenderIDFromContext(ctx)
	if senderID == "" || isSyntheticSender(senderID) {
		return fmt.Errorf("permission denied: system context cannot manage cron jobs in group chats")
	}
	numericID := strings.SplitN(senderID, "|", 2)[0]

	// Check cron-specific permission first.
	allowed, err := permStore.CheckPermission(ctx, agentID, userID, ConfigTypeCron, numericID)
	if err != nil {
		return nil // fail-open
	}
	if allowed {
		return nil
	}
	// Fall back to edit_file (implies full mutation access — post-split semantic).
	allowed, err = permStore.CheckPermission(ctx, agentID, userID, ConfigTypeEditFile, numericID)
	if err != nil {
		return nil // fail-open
	}
	if !allowed {
		return fmt.Errorf("permission denied: only users with cron or edit_file permission can manage cron jobs in group chats")
	}
	return nil
}
