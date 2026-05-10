package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// MCPServerData represents an MCP server in the database.
// Scope is determined by TeamID/ProjectID:
//   - both nil  → global (accessible to all agents with a grant)
//   - TeamID set   → team-scoped (accessible within that team)
//   - ProjectID set → project-scoped (accessible within that project)
//
// Invariant enforced by DB CHECK: TeamID and ProjectID cannot both be non-nil.
type MCPServerData struct {
	BaseModel
	Name        string          `json:"name" db:"name"`
	DisplayName string          `json:"display_name,omitempty" db:"display_name"`
	Transport   string          `json:"transport" db:"transport"`
	Command     string          `json:"command,omitempty" db:"command"`
	Args        json.RawMessage `json:"args,omitempty" db:"args"`
	URL         string          `json:"url,omitempty" db:"url"`
	Headers     json.RawMessage `json:"headers,omitempty" db:"headers"`
	Env         json.RawMessage `json:"env,omitempty" db:"env"`
	APIKey      string          `json:"api_key,omitempty" db:"api_key"`
	ToolPrefix  string          `json:"tool_prefix,omitempty" db:"tool_prefix"`
	TimeoutSec  int             `json:"timeout_sec" db:"timeout_sec"`
	Settings    json.RawMessage `json:"settings,omitempty" db:"settings"`
	Enabled     bool            `json:"enabled" db:"enabled"`
	CreatedBy string `json:"created_by" db:"created_by"`
	// Metadata is free-form JSONB describing the MCP server. Recognized key:
	//
	//   "primitives": []string — admin-populated list of permission primitives this
	//     server's tools touch, e.g. ["read_file","write_file","exec_shell"].
	//     Used by the admin assist UI: when agent_config_permissions denies a primitive
	//     on agent X, the UI cross-references mcp_agent_grants.server_id →
	//     metadata.primitives and surfaces "Server Y exposes write_file — add to
	//     tool_deny? [Apply]". This bridges the Option A gate trade-off (see
	//     GrantChecker) without DB-level cross-gating.
	//     rc1: admin populates manually during server registration.
	//     Post-rc1: auto-detect from tool descriptions.
	//
	//   Other keys are reserved; consumer code must tolerate unknown keys.
	Metadata  json.RawMessage `json:"metadata,omitempty" db:"metadata"`
	TeamID    *uuid.UUID      `json:"team_id,omitempty" db:"team_id"`
	ProjectID *uuid.UUID      `json:"project_id,omitempty" db:"project_id"`
}

// Scope returns the server's visibility scope: "global", "team", or "project".
func (s *MCPServerData) Scope() string {
	switch {
	case s.ProjectID != nil:
		return "project"
	case s.TeamID != nil:
		return "team"
	default:
		return "global"
	}
}

// MCPAgentGrant represents an MCP server grant to an agent.
type MCPAgentGrant struct {
	ID              uuid.UUID       `json:"id" db:"id"`
	ServerID        uuid.UUID       `json:"server_id" db:"server_id"`
	AgentID         uuid.UUID       `json:"agent_id" db:"agent_id"`
	Enabled         bool            `json:"enabled" db:"enabled"`
	ToolAllow       json.RawMessage `json:"tool_allow,omitempty" db:"tool_allow"`             // JSONB
	ToolDeny        json.RawMessage `json:"tool_deny,omitempty" db:"tool_deny"`               // JSONB
	ConfigOverrides json.RawMessage `json:"config_overrides,omitempty" db:"config_overrides"` // JSONB
	GrantedBy       string          `json:"granted_by" db:"granted_by"`
	CreatedAt       time.Time       `json:"created_at" db:"created_at"`
}

// MCPUserGrant represents an MCP server grant to a user.
type MCPUserGrant struct {
	ID        uuid.UUID       `json:"id" db:"id"`
	ServerID  uuid.UUID       `json:"server_id" db:"server_id"`
	UserID    string          `json:"user_id" db:"user_id"`
	Enabled   bool            `json:"enabled" db:"enabled"`
	ToolAllow json.RawMessage `json:"tool_allow,omitempty" db:"tool_allow"` // JSONB
	ToolDeny  json.RawMessage `json:"tool_deny,omitempty" db:"tool_deny"`   // JSONB
	GrantedBy string          `json:"granted_by" db:"granted_by"`
	CreatedAt time.Time       `json:"created_at" db:"created_at"`
}

// MCPAccessRequest represents a request for MCP server access.
// Scope values: "agent" | "user".
// Status lifecycle: "pending" → "granted" | "denied" | "revoked".
// Shape invariant: scope='agent' requires agent_id set + user_id nil;
//                  scope='user' requires user_id set + agent_id nil.
type MCPAccessRequest struct {
	ID          uuid.UUID       `json:"id" db:"id"`
	ServerID    uuid.UUID       `json:"server_id" db:"server_id"`
	AgentID     *uuid.UUID      `json:"agent_id,omitempty" db:"agent_id"`
	UserID      string          `json:"user_id,omitempty" db:"user_id"`
	Scope       string          `json:"scope" db:"scope"`
	Status      string          `json:"status" db:"status"`
	Reason      string          `json:"reason,omitempty" db:"reason"`
	ToolAllow   json.RawMessage `json:"tool_allow,omitempty" db:"tool_allow"` // JSONB
	RequestedBy string          `json:"requested_by" db:"requested_by"`
	ReviewedBy  string          `json:"reviewed_by,omitempty" db:"reviewed_by"`
	ReviewedAt  *time.Time      `json:"reviewed_at,omitempty" db:"reviewed_at"`
	ReviewNote  string          `json:"review_note,omitempty" db:"review_note"`
	CreatedAt   time.Time       `json:"created_at" db:"created_at"`
}

// MCP access request scope constants.
const (
	MCPRequestScopeAgent = "agent"
	MCPRequestScopeUser  = "user"
)

// MCP access request status lifecycle constants.
const (
	MCPRequestStatusPending = "pending"
	MCPRequestStatusGranted = "granted"
	MCPRequestStatusDenied  = "denied"
	MCPRequestStatusRevoked = "revoked"
)

// MCPAccessInfo combines server data with grant-level tool filters for runtime resolution.
type MCPAccessInfo struct {
	Server    MCPServerData `json:"server" db:"-"`
	ToolAllow []string      `json:"tool_allow,omitempty" db:"-"` // effective allow list (nil = all)
	ToolDeny  []string      `json:"tool_deny,omitempty" db:"-"`  // effective deny list
}

// MCPUserCredentials holds per-user credential overrides for an MCP server.
type MCPUserCredentials struct {
	APIKey  string            `json:"api_key,omitempty" db:"-"`  // decrypted
	Headers map[string]string `json:"headers,omitempty" db:"-"`  // decrypted
	Env     map[string]string `json:"env,omitempty" db:"-"`      // decrypted
}

// MCPServerStore manages MCP server configs and access grants.
type MCPServerStore interface {
	// Server CRUD
	CreateServer(ctx context.Context, s *MCPServerData) error
	GetServer(ctx context.Context, id uuid.UUID) (*MCPServerData, error)
	GetServerByName(ctx context.Context, name string) (*MCPServerData, error)
	ListServers(ctx context.Context) ([]MCPServerData, error)
	UpdateServer(ctx context.Context, id uuid.UUID, updates map[string]any) error
	DeleteServer(ctx context.Context, id uuid.UUID) error

	// Agent grants
	GrantToAgent(ctx context.Context, g *MCPAgentGrant) error
	RevokeFromAgent(ctx context.Context, serverID, agentID uuid.UUID) error
	ListAgentGrants(ctx context.Context, agentID uuid.UUID) ([]MCPAgentGrant, error)
	ListServerGrants(ctx context.Context, serverID uuid.UUID) ([]MCPAgentGrant, error)

	// User grants
	GrantToUser(ctx context.Context, g *MCPUserGrant) error
	RevokeFromUser(ctx context.Context, serverID uuid.UUID, userID string) error

	// Counts: agent grant counts per server (for listing UI)
	CountAgentGrantsByServer(ctx context.Context) (map[uuid.UUID]int, error)

	// Resolution: all accessible MCP servers + tool filters for agent+user (flat, no scope filter)
	ListAccessible(ctx context.Context, agentID uuid.UUID, userID string) ([]MCPAccessInfo, error)

	// ListAccessibleServers returns servers the agent can reach, filtered by scope context.
	// Visibility = (global servers) UNION (team-scoped if teamID non-nil) UNION
	// (project-scoped if projectID non-nil), intersected with active agent grants.
	// Returns ordered by name. An agent without a grant gets an empty slice.
	ListAccessibleServers(ctx context.Context, agentID uuid.UUID, teamID, projectID *uuid.UUID) ([]MCPServerData, error)

	// Access requests
	CreateRequest(ctx context.Context, req *MCPAccessRequest) error
	ListPendingRequests(ctx context.Context) ([]MCPAccessRequest, error)
	ReviewRequest(ctx context.Context, requestID uuid.UUID, approved bool, reviewedBy, note string) error

	// MarkGranted transitions a pending request to 'granted' and inserts the
	// appropriate grant row in a single transaction.
	MarkGranted(ctx context.Context, requestID uuid.UUID, reviewedBy string) error

	// MarkDenied transitions a pending request to 'denied', recording the reviewer.
	MarkDenied(ctx context.Context, requestID uuid.UUID, reviewedBy, note string) error

	// MarkRevoked transitions a granted request to 'revoked' and deletes the
	// associated grant row in a single transaction. One-way: re-request requires
	// a new CreateRequest (partial UNIQUE allows it since 'revoked' rows are excluded).
	MarkRevoked(ctx context.Context, requestID uuid.UUID) error

	// Per-user credentials
	GetUserCredentials(ctx context.Context, serverID uuid.UUID, userID string) (*MCPUserCredentials, error)
	SetUserCredentials(ctx context.Context, serverID uuid.UUID, userID string, creds MCPUserCredentials) error
	DeleteUserCredentials(ctx context.Context, serverID uuid.UUID, userID string) error
}
