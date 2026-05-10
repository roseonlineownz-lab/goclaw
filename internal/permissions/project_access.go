package permissions

import (
	"context"
	"fmt"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// ProjectRole is the effective access level a user holds on a project.
// The three levels form a strict hierarchy: viewer < member < editor.
type ProjectRole string

const (
	ProjectRoleViewer ProjectRole = "viewer"
	ProjectRoleMember ProjectRole = "member"
	ProjectRoleEditor ProjectRole = "editor"
)

// projectRoleRank maps each role to its numeric rank for comparison.
// editor == owner rank (3) — owner is tracked separately via isOwner flag.
var projectRoleRank = map[ProjectRole]int{
	ProjectRoleViewer: 1,
	ProjectRoleMember: 2,
	ProjectRoleEditor: 3,
}

// rankToRole maps a numeric rank back to the canonical ProjectRole string.
var rankToRole = map[int]ProjectRole{
	1: ProjectRoleViewer,
	2: ProjectRoleMember,
	3: ProjectRoleEditor,
}

// ResolveProjectRole returns the effective ProjectRole for a user on a project.
//
// Resolution order (single SQL query, max rank wins):
//  1. Owner: user created the project → editor-equivalent rank, isOwner=true
//  2. Direct grant: project_grants row with user_id = userID
//  3. Team grant: project_grants row with team_id T, and user is a member of T
//     (via team_user_members join — the user-membership route)
//
// Returns found=false (and nil error) when the user has no access at all.
// Callers must gate access before proceeding with project-scoped operations.
func ResolveProjectRole(
	ctx context.Context,
	grants store.ProjectGrantStore,
	userID, projectID string,
) (role ProjectRole, isOwner bool, found bool, err error) {
	rank, owner, ok, err := grants.ResolveProjectRole(ctx, userID, projectID)
	if err != nil {
		return "", false, false, fmt.Errorf("project access resolver: %w", err)
	}
	if !ok {
		return "", false, false, nil
	}
	r, valid := rankToRole[rank]
	if !valid {
		return "", false, false, fmt.Errorf("project access resolver: unexpected rank %d", rank)
	}
	return r, owner, true, nil
}

// CanAccessProject reports whether a user meets the minimum role requirement on a project.
//
// Returns false (nil error) when the user has no access or falls below minRole.
// Returns an error only on storage failures.
func CanAccessProject(
	ctx context.Context,
	grants store.ProjectGrantStore,
	userID, projectID string,
	minRole ProjectRole,
) (bool, error) {
	role, _, found, err := ResolveProjectRole(ctx, grants, userID, projectID)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	return projectRoleRank[role] >= projectRoleRank[minRole], nil
}
