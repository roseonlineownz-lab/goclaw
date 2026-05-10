//go:build sqlite || sqliteonly

package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// SQLiteTeamUserMemberStore implements store.TeamUserMemberStore backed by SQLite.
type SQLiteTeamUserMemberStore struct {
	db *sql.DB
}

// NewSQLiteTeamUserMemberStore creates a new SQLite-backed team user member store.
func NewSQLiteTeamUserMemberStore(db *sql.DB) *SQLiteTeamUserMemberStore {
	return &SQLiteTeamUserMemberStore{db: db}
}

// AddMember inserts a (team_id, user_id, role) membership row.
// Returns an error on duplicate (team_id, user_id) — composite PK violation.
func (s *SQLiteTeamUserMemberStore) AddMember(ctx context.Context, teamID, userID, role string, addedBy *string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var addedByVal any
	if addedBy != nil {
		addedByVal = *addedBy
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO team_user_members (team_id, user_id, role, added_by, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		teamID, userID, role, addedByVal, now,
	)
	if err != nil {
		return fmt.Errorf("team_user_members insert: %w", err)
	}
	return nil
}

// RemoveMember deletes a membership row. No-op when the row does not exist.
func (s *SQLiteTeamUserMemberStore) RemoveMember(ctx context.Context, teamID, userID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM team_user_members WHERE team_id = ? AND user_id = ?`,
		teamID, userID,
	)
	return err
}

// ListByTeam returns all members of a team ordered by created_at ASC.
// Returns an empty (non-nil) slice when the team has no members.
func (s *SQLiteTeamUserMemberStore) ListByTeam(ctx context.Context, teamID string) ([]*store.TeamUserMember, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT team_id, user_id, role, added_by, created_at
		 FROM team_user_members
		 WHERE team_id = ?
		 ORDER BY created_at ASC`,
		teamID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSQLiteMemberRows(rows)
}

// ListByUser returns all team memberships for a user ordered by created_at ASC.
// Returns an empty (non-nil) slice when the user has no memberships.
func (s *SQLiteTeamUserMemberStore) ListByUser(ctx context.Context, userID string) ([]*store.TeamUserMember, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT team_id, user_id, role, added_by, created_at
		 FROM team_user_members
		 WHERE user_id = ?
		 ORDER BY created_at ASC`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSQLiteMemberRows(rows)
}

// GetRole returns the role for the (teamID, userID) pair.
// found is false and err is nil when no row exists.
func (s *SQLiteTeamUserMemberStore) GetRole(ctx context.Context, teamID, userID string) (string, bool, error) {
	var role string
	err := s.db.QueryRowContext(ctx,
		`SELECT role FROM team_user_members WHERE team_id = ? AND user_id = ?`,
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

// scanSQLiteMemberRows scans all rows from a team_user_members query.
func scanSQLiteMemberRows(rows *sql.Rows) ([]*store.TeamUserMember, error) {
	var members []*store.TeamUserMember
	for rows.Next() {
		m, err := scanSQLiteMemberRow(rows)
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

// scanSQLiteMemberRow scans a single row from *sql.Rows into a TeamUserMember.
// created_at is stored as RFC3339Nano text; sqliteTime handles parsing.
func scanSQLiteMemberRow(rows *sql.Rows) (*store.TeamUserMember, error) {
	var m store.TeamUserMember
	var addedBy sql.NullString
	createdAt := &sqliteTime{}
	err := rows.Scan(&m.TeamID, &m.UserID, &m.Role, &addedBy, createdAt)
	if err != nil {
		return nil, err
	}
	if addedBy.Valid {
		m.AddedBy = &addedBy.String
	}
	m.CreatedAt = createdAt.Time
	return &m, nil
}

// compile-time interface check
var _ store.TeamUserMemberStore = (*SQLiteTeamUserMemberStore)(nil)
