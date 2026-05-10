package store

import (
	"context"
	"time"
)

// TeamUserMember represents a single user↔team membership row.
// Roles (viewer/member/admin) govern what the user may do within the team
// (manage agents, invite users, accept project grants). These are distinct
// from project_grants.role which controls project-level access.
type TeamUserMember struct {
	TeamID    string
	UserID    string
	Role      string
	AddedBy   *string   // nil when the granting user has been deleted
	CreatedAt time.Time
}

// TeamUserMemberStore manages user↔team membership.
// It is the canonical source for the team axis of the project access resolver.
// Membership rows alone grant nothing; the project_grants resolver consults
// this table to expand implicit team grants.
type TeamUserMemberStore interface {
	// AddMember inserts a membership row. addedBy may be nil.
	// Returns an error (unique/PK violation) if the (teamID, userID) pair already exists.
	AddMember(ctx context.Context, teamID, userID, role string, addedBy *string) error

	// RemoveMember deletes a membership row. No-op if the row does not exist.
	RemoveMember(ctx context.Context, teamID, userID string) error

	// ListByTeam returns all members of a team, ordered by created_at.
	// Returns an empty slice (not sql.ErrNoRows) when the team has no members.
	ListByTeam(ctx context.Context, teamID string) ([]*TeamUserMember, error)

	// ListByUser returns all team memberships for a user, ordered by created_at.
	// Returns an empty slice (not sql.ErrNoRows) when the user has no memberships.
	ListByUser(ctx context.Context, userID string) ([]*TeamUserMember, error)

	// GetRole returns the role for the (teamID, userID) pair.
	// found is false (and err nil) when no membership row exists.
	GetRole(ctx context.Context, teamID, userID string) (role string, found bool, err error)
}
