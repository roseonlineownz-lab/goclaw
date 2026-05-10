package store

import (
	"context"
	"time"
)

// ProjectGrant represents a single row in project_grants.
// Exactly one of UserID/TeamID is non-nil (XOR enforced by DB CHECK constraint).
type ProjectGrant struct {
	ID        string
	ProjectID string
	// UserID is set for direct user grants; nil for team grants.
	UserID *string
	// TeamID is set for team grants; nil for direct user grants.
	TeamID    *string
	Role      string // "viewer" | "member" | "editor"
	GrantedBy *string
	CreatedAt time.Time
}

// ProjectGrantStore manages project-level access grants.
// It is the backing store for the project access resolver in internal/permissions.
type ProjectGrantStore interface {
	// Create inserts a new grant. g.ID is set on success when it was empty.
	Create(ctx context.Context, g *ProjectGrant) error

	// Get fetches a single grant by ID. Returns sql.ErrNoRows when not found.
	Get(ctx context.Context, id string) (*ProjectGrant, error)

	// List returns all grants for a project, ordered by created_at ASC.
	List(ctx context.Context, projectID string) ([]*ProjectGrant, error)

	// ListForUser returns all direct user grants for a given userID.
	ListForUser(ctx context.Context, userID string) ([]*ProjectGrant, error)

	// ListForTeam returns all team grants for a given teamID.
	ListForTeam(ctx context.Context, teamID string) ([]*ProjectGrant, error)

	// Delete removes a grant by ID. No-op when the row does not exist.
	Delete(ctx context.Context, id string) error

	// ResolveProjectRole runs the single-query resolver for a (user, project) pair.
	// Returns the effective role rank (viewer=1, member=2, editor=3), whether the
	// user is the project owner, and whether any access was found.
	// The result is consumed by internal/permissions.ResolveProjectRole.
	ResolveProjectRole(ctx context.Context, userID, projectID string) (roleRank int, isOwner bool, found bool, err error)
}
