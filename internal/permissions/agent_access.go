package permissions

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
)

// Share role names returned by AgentAccessResolver. ShareOwner is implicit
// (agents.owner_id == userID); ShareNone signals "no access". These differ
// from the platform RoleAdmin/RoleMember constants in policy.go which gate
// RPC routing — share roles gate per-agent access.
const (
	ShareNone   = ""
	ShareViewer = "viewer"
	ShareMember = "member"
	ShareEditor = "editor"
	ShareOwner  = "owner"
)

// sharePrecedence maps a share role to its numeric precedence. Higher wins
// when multiple grants converge on the same (user, agent) pair.
var sharePrecedence = map[string]int{
	ShareNone:   0,
	ShareViewer: 1,
	ShareMember: 2,
	ShareEditor: 3,
	ShareOwner:  4,
}

func shareFromPrecedence(p int) string {
	switch p {
	case 4:
		return ShareOwner
	case 3:
		return ShareEditor
	case 2:
		return ShareMember
	case 1:
		return ShareViewer
	default:
		return ShareNone
	}
}

// AgentAccessResolver computes a user's effective role on an agent by union
// of: ownership, explicit user grants on agent_shares, and implicit team
// membership via team_user_grants joined to team-target agent_shares.
//
// All sources flow through a single SQL roundtrip in ResolveRole — owner
// path uses CASE precedence so we never need a second query.
type AgentAccessResolver struct {
	db *sql.DB
}

// NewAgentAccessResolver wires the resolver to the gateway database. Pass
// nil only in tests that exercise the precedence helper directly.
func NewAgentAccessResolver(db *sql.DB) *AgentAccessResolver {
	return &AgentAccessResolver{db: db}
}

// ResolveRole returns the highest-precedence role for (userID, agentID).
// Returns RoleNone with nil error when the user has no relation to the agent.
func (r *AgentAccessResolver) ResolveRole(ctx context.Context, userID, agentID uuid.UUID) (string, error) {
	if r.db == nil {
		return ShareNone, errors.New("agent_access: resolver has no DB handle")
	}
	const q = `
		SELECT COALESCE(MAX(precedence), 0) FROM (
			-- Ownership. owner_user_id is the UUID FK; legacy owner_id (VARCHAR)
			-- holds channel-style identities and is not used for share decisions.
			SELECT 4 AS precedence
			  FROM agents
			 WHERE id = $1 AND owner_user_id = $2 AND deleted_at IS NULL
			UNION ALL
			-- Explicit user grant.
			SELECT CASE role
			       WHEN 'editor' THEN 3
			       WHEN 'member' THEN 2
			       WHEN 'viewer' THEN 1
			       ELSE 0 END AS precedence
			  FROM agent_shares
			 WHERE agent_id = $1 AND shared_with_user_id = $2
			UNION ALL
			-- Implicit team grant: user is in team T (via team_user_grants),
			-- and agent is shared to team T.
			SELECT CASE s.role
			       WHEN 'editor' THEN 3
			       WHEN 'member' THEN 2
			       WHEN 'viewer' THEN 1
			       ELSE 0 END AS precedence
			  FROM agent_shares AS s
			  JOIN team_user_grants AS g
			    ON g.team_id = s.shared_with_team_id
			 WHERE s.agent_id = $1 AND g.user_id = $2
		) AS rows
	`
	var prec int
	err := r.db.QueryRowContext(ctx, q, agentID, userID).Scan(&prec)
	if err != nil {
		return ShareNone, err
	}
	return shareFromPrecedence(prec), nil
}

// HighestShareRole picks the highest-precedence share role from the inputs.
// Useful when callers compose multiple resolved sources outside the DB.
func HighestShareRole(roles ...string) string {
	best := 0
	for _, r := range roles {
		if p := sharePrecedence[r]; p > best {
			best = p
		}
	}
	return shareFromPrecedence(best)
}
