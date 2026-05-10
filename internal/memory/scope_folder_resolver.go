package memory

import (
	"fmt"

	"github.com/nextlevelbuilder/goclaw/internal/workspace"
)

// scopeFolder resolves a ScopeKey to a relative folder path under the workspace root.
//
// Path matrix (L71):
//
//	personal agent, no team/contact/project → agents/{agentKey}/
//	team only                               → teams/{teamKey}/
//	user scoped                             → users/{userKey}/
//	contact scoped (channel group/DM)       → contacts/{contactID}/
//	project scoped                          → projects/{projectSlug}/
//	team + user                             → teams/{teamKey}/users/{userKey}/
//	project + user                          → projects/{projectSlug}/users/{userKey}/
//
// In all paths, segment components are sanitized through SanitizeSegment to
// prevent directory traversal via embedded path separators or control chars.
//
// The agent_id is the primary disambiguator; it is always the root prefix for
// scopes that do not have a dedicated top-level bucket (personal agents).
func scopeFolder(scope ScopeKey) (string, error) {
	if scope.AgentID == "" {
		return "", fmt.Errorf("memory.fs: ScopeKey.AgentID is required")
	}

	agentSeg := workspace.SanitizeSegment(scope.AgentID)

	switch {
	// Project + User: project owns the root, user isolates within
	case scope.ProjectID != "" && scope.UserID != "":
		return fmt.Sprintf("projects/%s/users/%s",
			workspace.SanitizeSegment(scope.ProjectID),
			workspace.SanitizeSegment(scope.UserID),
		), nil

	// Project only
	case scope.ProjectID != "":
		return fmt.Sprintf("projects/%s", workspace.SanitizeSegment(scope.ProjectID)), nil

	// Contact scoped (channel DM / group)
	case scope.ContactID != "":
		return fmt.Sprintf("contacts/%s", workspace.SanitizeSegment(scope.ContactID)), nil

	// Team + User: team owns root, user isolates within
	case scope.TeamID != "" && scope.UserID != "":
		return fmt.Sprintf("teams/%s/users/%s",
			workspace.SanitizeSegment(scope.TeamID),
			workspace.SanitizeSegment(scope.UserID),
		), nil

	// Team only (shared workspace)
	case scope.TeamID != "":
		return fmt.Sprintf("teams/%s", workspace.SanitizeSegment(scope.TeamID)), nil

	// User scoped (personal predefined agent, per-user isolation)
	case scope.UserID != "":
		return fmt.Sprintf("users/%s", workspace.SanitizeSegment(scope.UserID)), nil

	// Agent-only (personal open/predefined without user context)
	default:
		return fmt.Sprintf("agents/%s", agentSeg), nil
	}
}
