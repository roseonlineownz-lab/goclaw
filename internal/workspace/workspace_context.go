// Package workspace provides unified workspace resolution for agent runs.
// Replaces dual ctxWorkspace + ctxTeamWorkspace with a single immutable context.
//
// V3 design: Phase 1B — foundation interface.
package workspace

import (
	"context"

	"github.com/google/uuid"
)

// Scope defines workspace access boundary.
type Scope string

const (
	ScopePersonal Scope = "personal"  // single user, isolated
	ScopeTeam     Scope = "team"      // team context, shared or isolated
	ScopeDelegate Scope = "delegate"  // delegated task, scoped access
	ScopeProject  Scope = "project"   // session bound to a project workspace
)

// WorkspaceContext is resolved ONCE at run start, immutable for the entire run.
// Eliminates dual ctxWorkspace + ctxTeamWorkspace confusion.
type WorkspaceContext struct {
	// ActivePath is THE path for all file operations (read/write/list/exec).
	ActivePath string

	// Scope describes the access boundary type.
	Scope Scope

	// ReadOnlyPaths are additional paths the agent can read but NOT write.
	ReadOnlyPaths []string

	// SharedPath is the shared delegate area (read/write by both delegator + delegatee).
	// nil when not in delegation context.
	SharedPath *string

	// TeamPath is the team workspace root (nil if not in team context).
	TeamPath *string

	// MemoryScope determines memory isolation.
	// Defaults to workspace scope. "shared" = all users in agent see same memory.
	MemoryScope string

	// KGScope determines knowledge graph isolation.
	KGScope string

	// OwnerID identifies who owns this workspace context (user ID or chat ID).
	OwnerID string

	// EnforcementLabel is injected into system prompt verbatim.
	EnforcementLabel string

	// ProjectID is set when the session is bound to a project.
	// nil means no project binding — falls through to standard 6-scenario resolution.
	ProjectID *uuid.UUID

	// ProjectSlug is the URL-safe slug used for filesystem path resolution.
	// Empty when ProjectID is nil.
	ProjectSlug string
}

// Resolver produces a WorkspaceContext from request parameters.
// Called once at ContextStage. Result is immutable.
type Resolver interface {
	Resolve(ctx context.Context, params ResolveParams) (*WorkspaceContext, error)

	// ResolveChannel handles the 12-scenario channel path matrix.
	// Returns (absolute_fs_path, ChannelScope, error).
	// Pure function — no DB calls; all resolved keys must be in ctx.
	ResolveChannel(ctx context.Context, c ChannelResolveCtx) (string, ChannelScope, error)
}

// ResolveParams captures inputs for the project-priority branch of Resolve().
// All non-project paths flow through ResolveChannel + ChannelResolveCtx.
type ResolveParams struct {
	AgentID string
	UserID  string
	ChatID  string
	BaseDir string

	// ProjectID and ProjectSlug activate the project-workspace branch.
	// Both required: nil ProjectID or empty ProjectSlug → Resolve returns
	// an error and the caller must use ResolveChannel.
	ProjectID   *uuid.UUID
	ProjectSlug string
}

// DefaultEnforcementLabel returns a human-readable workspace description
// for system prompt injection based on scope and sharing mode.
func DefaultEnforcementLabel(scope Scope, shared bool) string {
	switch scope {
	case ScopeDelegate:
		return "You are working on a delegated task. Only access files in your designated workspace."
	case ScopeTeam:
		if shared {
			return "You are working in a shared team workspace. Other members can see your files."
		}
		return "You are working in an isolated team workspace."
	case ScopeProject:
		return "You are working in a project workspace. Files are scoped to this project."
	default:
		return "You are working in the user's personal workspace."
	}
}

// ── Channel-aware resolver types ─────────────────────────────────────────────

// SenderKind classifies the origin of a channel message for workspace routing.
type SenderKind int

const (
	SenderWeb          SenderKind = iota // web UI — identified by user UUID
	SenderChannelDM                      // channel direct message — identified by sender_id
	SenderChannelGroup                   // channel group chat — identified by chat_id
)

// ChannelResolveCtx carries all inputs for the 12-scenario channel workspace
// resolver. It is separate from ResolveParams (web/delegation/project paths)
// because channel sessions resolve on different axes: sender identity ×
// agent context × merge state.
//
// All key fields (UserKey, AgentKey, TeamKey) use the human-readable slug form
// (users.user_key, agent.agent_key, team.team_key) — never raw UUIDs — because
// they map directly to filesystem path segments.
type ChannelResolveCtx struct {
	// BaseDir is the workspace root (required).
	BaseDir string

	// SenderKind classifies the message origin.
	SenderKind SenderKind

	// UserKey is the stable slug from users.user_key.
	// Required when Merged=true or SenderKind=SenderWeb.
	UserKey string

	// AgentKey is the agent slug (agent.agent_key or agent.id used as path segment).
	AgentKey string

	// TeamKey is the team slug. Non-empty activates the team-scoped branch.
	TeamKey string

	// ChannelType is the platform identifier (e.g. "telegram", "discord").
	ChannelType string

	// SenderID is the platform user identifier (used for DM contact subfolders).
	SenderID string

	// ChatID is the platform group/channel identifier (used for group subfolders).
	ChatID string

	// Merged indicates the contact has been merged into a canonical user account
	// (channel_contacts.merged_id IS NOT NULL). When true, the privacy hard rule
	// forces routing to the canonical users/{user_key}/... zone regardless of
	// sender kind — merged user data MUST NOT write to agent/team-shared zones.
	Merged bool
}

// ChannelScope is the resolved workspace scope returned by ResolveChannel.
// It carries routing metadata for downstream memory/permission layers.
type ChannelScope struct {
	// SenderKind echoes the input sender classification.
	SenderKind SenderKind

	// ZoneKind describes the resolved zone class.
	ZoneKind string // "user-agent" | "user-team" | "agent-contact" | "team-contact" | "agent-group" | "team-group"

	// Merged echoes whether the canonical-zone rule was applied.
	Merged bool
}

// context key for WorkspaceContext propagation.
type ctxKeyWorkspace struct{}

// FromContext extracts WorkspaceContext from context.
func FromContext(ctx context.Context) *WorkspaceContext {
	wc, _ := ctx.Value(ctxKeyWorkspace{}).(*WorkspaceContext)
	return wc
}

// WithContext stores WorkspaceContext in context.
func WithContext(ctx context.Context, wc *WorkspaceContext) context.Context {
	return context.WithValue(ctx, ctxKeyWorkspace{}, wc)
}
