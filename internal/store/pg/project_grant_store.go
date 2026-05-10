package pg

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// PGProjectGrantStore implements store.ProjectGrantStore using PostgreSQL.
type PGProjectGrantStore struct {
	db *sql.DB
}

// NewPGProjectGrantStore creates a new PostgreSQL-backed project grant store.
func NewPGProjectGrantStore(db *sql.DB) *PGProjectGrantStore {
	return &PGProjectGrantStore{db: db}
}

// Create inserts a new project grant. g.ID is set when it was empty.
func (s *PGProjectGrantStore) Create(ctx context.Context, g *store.ProjectGrant) error {
	if g.ID == "" {
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("uuid v7: %w", err)
		}
		g.ID = id.String()
	}
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO project_grants (id, project_id, user_id, team_id, role, granted_by, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		g.ID, g.ProjectID,
		ptrToNullString(g.UserID), ptrToNullString(g.TeamID),
		g.Role, ptrToNullString(g.GrantedBy), now,
	)
	if err != nil {
		return err
	}
	g.CreatedAt = now
	return nil
}

// Get fetches a grant by ID. Returns sql.ErrNoRows when not found.
func (s *PGProjectGrantStore) Get(ctx context.Context, id string) (*store.ProjectGrant, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, project_id, user_id, team_id, role, granted_by, created_at
		 FROM project_grants WHERE id = $1`,
		id,
	)
	return scanPGGrant(row)
}

// List returns all grants for a project ordered by created_at ASC.
func (s *PGProjectGrantStore) List(ctx context.Context, projectID string) ([]*store.ProjectGrant, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, project_id, user_id, team_id, role, granted_by, created_at
		 FROM project_grants WHERE project_id = $1 ORDER BY created_at ASC`,
		projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPGGrantRows(rows)
}

// ListForUser returns all direct user grants for a given userID.
func (s *PGProjectGrantStore) ListForUser(ctx context.Context, userID string) ([]*store.ProjectGrant, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, project_id, user_id, team_id, role, granted_by, created_at
		 FROM project_grants WHERE user_id = $1 ORDER BY created_at ASC`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPGGrantRows(rows)
}

// ListForTeam returns all team grants for a given teamID.
func (s *PGProjectGrantStore) ListForTeam(ctx context.Context, teamID string) ([]*store.ProjectGrant, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, project_id, user_id, team_id, role, granted_by, created_at
		 FROM project_grants WHERE team_id = $1 ORDER BY created_at ASC`,
		teamID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPGGrantRows(rows)
}

// Delete removes a grant by ID. No-op when the row does not exist.
func (s *PGProjectGrantStore) Delete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM project_grants WHERE id = $1`, id,
	)
	return err
}

// ResolveProjectRole computes the effective role for (userID, projectID) using a
// single UNION ALL query that covers three access paths:
//  1. Owner: projects.owner_user_id = userID → rank 3, isOwner true
//  2. Direct grant: project_grants.user_id = userID → rank by role
//  3. Team grant: team has grant AND user is member of team (via team_user_members)
//
// Returns the maximum rank and whether the user is the project owner.
// found=false (rank=0, err=nil) means the user has no access at all.
func (s *PGProjectGrantStore) ResolveProjectRole(ctx context.Context, userID, projectID string) (int, bool, bool, error) {
	const q = `
		SELECT MAX(role_rank), BOOL_OR(is_owner)
		FROM (
			SELECT 3 AS role_rank, TRUE AS is_owner
			FROM projects
			WHERE id = $1 AND owner_user_id = $2

			UNION ALL

			SELECT
				CASE role
					WHEN 'viewer' THEN 1
					WHEN 'member' THEN 2
					WHEN 'editor' THEN 3
				END AS role_rank,
				FALSE AS is_owner
			FROM project_grants
			WHERE project_id = $1 AND user_id = $2

			UNION ALL

			SELECT
				CASE pg.role
					WHEN 'viewer' THEN 1
					WHEN 'member' THEN 2
					WHEN 'editor' THEN 3
				END AS role_rank,
				FALSE AS is_owner
			FROM project_grants pg
			JOIN team_user_members tum ON tum.team_id = pg.team_id AND tum.user_id = $2
			WHERE pg.project_id = $1
		) r
	`
	var rank sql.NullInt64
	var isOwner sql.NullBool
	err := s.db.QueryRowContext(ctx, q, projectID, userID).Scan(&rank, &isOwner)
	if err != nil {
		return 0, false, false, fmt.Errorf("resolve project role: %w", err)
	}
	if !rank.Valid {
		// No rows matched — no access.
		return 0, false, false, nil
	}
	return int(rank.Int64), isOwner.Bool, true, nil
}

// --- scan helpers ---

// grantScanner is satisfied by both *sql.Row and *sql.Rows.
type grantScanner interface{ Scan(dest ...any) error }

// scanGrant scans one project grant row from any Scanner (sql.Row or sql.Rows).
func scanGrant(r grantScanner) (*store.ProjectGrant, error) {
	var g store.ProjectGrant
	var userID, teamID, grantedBy sql.NullString
	if err := r.Scan(&g.ID, &g.ProjectID, &userID, &teamID, &g.Role, &grantedBy, &g.CreatedAt); err != nil {
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
	return &g, nil
}

// scanPGGrant scans a single *sql.Row result.
func scanPGGrant(row *sql.Row) (*store.ProjectGrant, error) { return scanGrant(row) }

func scanPGGrantRows(rows *sql.Rows) ([]*store.ProjectGrant, error) {
	var grants []*store.ProjectGrant
	for rows.Next() {
		g, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}
		grants = append(grants, g)
	}
	return grants, rows.Err()
}

// ptrToNullString converts a *string to sql.NullString for nullable PG columns.
func ptrToNullString(s *string) sql.NullString {
	if s == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *s, Valid: true}
}

// compile-time interface check
var _ store.ProjectGrantStore = (*PGProjectGrantStore)(nil)
