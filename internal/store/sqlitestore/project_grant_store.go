//go:build sqlite || sqliteonly

package sqlitestore

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// SQLiteProjectGrantStore implements store.ProjectGrantStore backed by SQLite.
type SQLiteProjectGrantStore struct {
	db *sql.DB
}

// NewSQLiteProjectGrantStore creates a new SQLite-backed project grant store.
func NewSQLiteProjectGrantStore(db *sql.DB) *SQLiteProjectGrantStore {
	return &SQLiteProjectGrantStore{db: db}
}

// Create inserts a new project grant. g.ID is set when it was empty.
func (s *SQLiteProjectGrantStore) Create(ctx context.Context, g *store.ProjectGrant) error {
	if g.ID == "" {
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("uuid v7: %w", err)
		}
		g.ID = id.String()
	}
	now := time.Now().UTC()
	var userIDVal, teamIDVal, grantedByVal any
	if g.UserID != nil {
		userIDVal = *g.UserID
	}
	if g.TeamID != nil {
		teamIDVal = *g.TeamID
	}
	if g.GrantedBy != nil {
		grantedByVal = *g.GrantedBy
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO project_grants (id, project_id, user_id, team_id, role, granted_by, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		g.ID, g.ProjectID, userIDVal, teamIDVal,
		g.Role, grantedByVal, now.Format(time.RFC3339Nano),
	)
	if err != nil {
		return err
	}
	g.CreatedAt = now
	return nil
}

// Get fetches a grant by ID. Returns sql.ErrNoRows when not found.
func (s *SQLiteProjectGrantStore) Get(ctx context.Context, id string) (*store.ProjectGrant, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, project_id, user_id, team_id, role, granted_by, created_at
		 FROM project_grants WHERE id = ?`,
		id,
	)
	return scanSQLiteGrant(row)
}

// List returns all grants for a project ordered by created_at ASC.
func (s *SQLiteProjectGrantStore) List(ctx context.Context, projectID string) ([]*store.ProjectGrant, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, project_id, user_id, team_id, role, granted_by, created_at
		 FROM project_grants WHERE project_id = ? ORDER BY created_at ASC`,
		projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSQLiteGrantRows(rows)
}

// ListForUser returns all direct user grants for a given userID.
func (s *SQLiteProjectGrantStore) ListForUser(ctx context.Context, userID string) ([]*store.ProjectGrant, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, project_id, user_id, team_id, role, granted_by, created_at
		 FROM project_grants WHERE user_id = ? ORDER BY created_at ASC`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSQLiteGrantRows(rows)
}

// ListForTeam returns all team grants for a given teamID.
func (s *SQLiteProjectGrantStore) ListForTeam(ctx context.Context, teamID string) ([]*store.ProjectGrant, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, project_id, user_id, team_id, role, granted_by, created_at
		 FROM project_grants WHERE team_id = ? ORDER BY created_at ASC`,
		teamID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSQLiteGrantRows(rows)
}

// Delete removes a grant by ID. No-op when the row does not exist.
func (s *SQLiteProjectGrantStore) Delete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM project_grants WHERE id = ?`, id,
	)
	return err
}

// ResolveProjectRole computes the effective role for (userID, projectID) using a
// single UNION ALL query covering three access paths:
//  1. Owner: projects.owner_user_id = userID → rank 3, isOwner 1
//  2. Direct grant: project_grants.user_id = userID → rank by role
//  3. Team grant: team has grant AND user is member of team (via team_user_members)
//
// SQLite uses MAX(role_rank) and MAX(is_owner) (INTEGER 0/1) instead of BOOL_OR.
// Returns found=false (rank=0, err=nil) when the user has no access.
func (s *SQLiteProjectGrantStore) ResolveProjectRole(ctx context.Context, userID, projectID string) (int, bool, bool, error) {
	const q = `
		SELECT MAX(role_rank), MAX(is_owner)
		FROM (
			SELECT 3 AS role_rank, 1 AS is_owner
			FROM projects
			WHERE id = ? AND owner_user_id = ?

			UNION ALL

			SELECT
				CASE role
					WHEN 'viewer' THEN 1
					WHEN 'member' THEN 2
					WHEN 'editor' THEN 3
				END AS role_rank,
				0 AS is_owner
			FROM project_grants
			WHERE project_id = ? AND user_id = ?

			UNION ALL

			SELECT
				CASE pg.role
					WHEN 'viewer' THEN 1
					WHEN 'member' THEN 2
					WHEN 'editor' THEN 3
				END AS role_rank,
				0 AS is_owner
			FROM project_grants pg
			JOIN team_user_members tum ON tum.team_id = pg.team_id AND tum.user_id = ?
			WHERE pg.project_id = ?
		) r
	`
	var rank sql.NullInt64
	var isOwnerInt sql.NullInt64
	// Bind order matches the 6 placeholders in the query.
	err := s.db.QueryRowContext(ctx, q,
		projectID, userID, // owner check
		projectID, userID, // direct grant
		userID, projectID, // team grant
	).Scan(&rank, &isOwnerInt)
	if err != nil {
		return 0, false, false, fmt.Errorf("resolve project role: %w", err)
	}
	if !rank.Valid {
		return 0, false, false, nil
	}
	return int(rank.Int64), isOwnerInt.Int64 == 1, true, nil
}

// --- scan helpers ---

// grantRowScanner is satisfied by both *sql.Row and *sql.Rows.
type grantRowScanner interface{ Scan(dest ...any) error }

// scanSQLiteGrantFrom scans one project grant row from any Scanner (sql.Row or sql.Rows).
func scanSQLiteGrantFrom(r grantRowScanner) (*store.ProjectGrant, error) {
	var g store.ProjectGrant
	var userID, teamID, grantedBy sql.NullString
	createdAt := &sqliteTime{}
	if err := r.Scan(&g.ID, &g.ProjectID, &userID, &teamID, &g.Role, &grantedBy, createdAt); err != nil {
		return nil, err
	}
	if userID.Valid {
		g.UserID = &userID.String
	}
	if teamID.Valid {
		g.TeamID = &teamID.String
	}
	if grantedBy.Valid {
		g.GrantedBy = &grantedBy.String
	}
	g.CreatedAt = createdAt.Time
	return &g, nil
}

// scanSQLiteGrant scans a single *sql.Row result.
func scanSQLiteGrant(row *sql.Row) (*store.ProjectGrant, error) { return scanSQLiteGrantFrom(row) }

func scanSQLiteGrantRows(rows *sql.Rows) ([]*store.ProjectGrant, error) {
	var grants []*store.ProjectGrant
	for rows.Next() {
		g, err := scanSQLiteGrantFrom(rows)
		if err != nil {
			return nil, err
		}
		grants = append(grants, g)
	}
	return grants, rows.Err()
}

// compile-time interface check
var _ store.ProjectGrantStore = (*SQLiteProjectGrantStore)(nil)
