package pg

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// PGTeamUserMemberStore implements store.TeamUserMemberStore using PostgreSQL.
type PGTeamUserMemberStore struct {
	db *sql.DB
}

// NewPGTeamUserMemberStore creates a new PostgreSQL-backed team user member store.
func NewPGTeamUserMemberStore(db *sql.DB) *PGTeamUserMemberStore {
	return &PGTeamUserMemberStore{db: db}
}

// AddMember inserts a (team_id, user_id, role) membership row.
// Returns an error on duplicate (team_id, user_id) — composite PK violation.
func (s *PGTeamUserMemberStore) AddMember(ctx context.Context, teamID, userID, role string, addedBy *string) error {
	now := time.Now().UTC()
	var addedByVal sql.NullString
	if addedBy != nil {
		addedByVal = sql.NullString{String: *addedBy, Valid: true}
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO team_user_members (team_id, user_id, role, added_by, created_at)
		 VALUES ($1, $2, $3, $4, $5)`,
		teamID, userID, role, addedByVal, now,
	)
	if err != nil {
		return fmt.Errorf("team_user_members insert: %w", err)
	}
	return nil
}

// RemoveMember deletes a membership row. No-op when the row does not exist.
func (s *PGTeamUserMemberStore) RemoveMember(ctx context.Context, teamID, userID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM team_user_members WHERE team_id = $1 AND user_id = $2`,
		teamID, userID,
	)
	return err
}

// ListByTeam returns all members of a team ordered by created_at ASC.
// Returns an empty (non-nil) slice when the team has no members.
func (s *PGTeamUserMemberStore) ListByTeam(ctx context.Context, teamID string) ([]*store.TeamUserMember, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT team_id, user_id, role, added_by, created_at
		 FROM team_user_members
		 WHERE team_id = $1
		 ORDER BY created_at ASC`,
		teamID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMemberRows(rows)
}

// ListByUser returns all team memberships for a user ordered by created_at ASC.
// Returns an empty (non-nil) slice when the user has no memberships.
func (s *PGTeamUserMemberStore) ListByUser(ctx context.Context, userID string) ([]*store.TeamUserMember, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT team_id, user_id, role, added_by, created_at
		 FROM team_user_members
		 WHERE user_id = $1
		 ORDER BY created_at ASC`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMemberRows(rows)
}

// GetRole returns the role for the (teamID, userID) pair.
// found is false and err is nil when no row exists.
func (s *PGTeamUserMemberStore) GetRole(ctx context.Context, teamID, userID string) (string, bool, error) {
	var role string
	err := s.db.QueryRowContext(ctx,
		`SELECT role FROM team_user_members WHERE team_id = $1 AND user_id = $2`,
		teamID, userID,
	).Scan(&role)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("team_user_members get role: %w", err)
	}
	return role, true, nil
}

// scanMemberRows scans all rows from a team_user_members query.
func scanMemberRows(rows *sql.Rows) ([]*store.TeamUserMember, error) {
	var members []*store.TeamUserMember
	for rows.Next() {
		m, err := scanMemberRow(rows)
		if err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	if members == nil {
		members = []*store.TeamUserMember{}
	}
	return members, rows.Err()
}

// scanMemberRow scans a single row from *sql.Rows into a TeamUserMember.
func scanMemberRow(rows *sql.Rows) (*store.TeamUserMember, error) {
	var m store.TeamUserMember
	var addedBy sql.NullString
	err := rows.Scan(&m.TeamID, &m.UserID, &m.Role, &addedBy, &m.CreatedAt)
	if err != nil {
		return nil, err
	}
	if addedBy.Valid {
		m.AddedBy = &addedBy.String
	}
	return &m, nil
}

// compile-time interface check
var _ store.TeamUserMemberStore = (*PGTeamUserMemberStore)(nil)
