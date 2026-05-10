package pg

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// PGProjectStore implements store.ProjectStore using PostgreSQL.
type PGProjectStore struct {
	db *sql.DB
}

// NewPGProjectStore creates a new PostgreSQL-backed project store.
func NewPGProjectStore(db *sql.DB) *PGProjectStore {
	return &PGProjectStore{db: db}
}

// Create inserts a new project row. If p.ID is uuid.Nil a v7 UUID is generated.
func (s *PGProjectStore) Create(ctx context.Context, p *store.Project) error {
	if p.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("uuid v7: %w", err)
		}
		p.ID = id
	}
	if len(p.Metadata) == 0 {
		p.Metadata = json.RawMessage("{}")
	}
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO projects (id, slug, owner_user_id, status, metadata, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		p.ID, p.Slug, p.OwnerUserID, p.Status, []byte(p.Metadata), now, now,
	)
	if err != nil {
		return err
	}
	p.CreatedAt = now
	p.UpdatedAt = now
	return nil
}

// Get fetches a project by UUID. Returns sql.ErrNoRows when not found.
func (s *PGProjectStore) Get(ctx context.Context, id uuid.UUID) (*store.Project, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, slug, owner_user_id, status, metadata, created_at, updated_at
		 FROM projects WHERE id = $1`,
		id,
	)
	return scanProject(row)
}

// GetBySlug fetches a project by its unique slug. Returns sql.ErrNoRows when not found.
func (s *PGProjectStore) GetBySlug(ctx context.Context, slug string) (*store.Project, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, slug, owner_user_id, status, metadata, created_at, updated_at
		 FROM projects WHERE slug = $1`,
		slug,
	)
	return scanProject(row)
}

// List returns projects matching the filter, ordered by created_at DESC.
func (s *PGProjectStore) List(ctx context.Context, f store.ListProjectsFilter) ([]*store.Project, error) {
	q := `SELECT id, slug, owner_user_id, status, metadata, created_at, updated_at
	      FROM projects WHERE 1=1`
	var args []any
	n := 1

	if f.OwnerUserID != uuid.Nil {
		q += fmt.Sprintf(" AND owner_user_id = $%d", n)
		args = append(args, f.OwnerUserID)
		n++
	}
	if f.Status != "" {
		q += fmt.Sprintf(" AND status = $%d", n)
		args = append(args, f.Status)
		n++
	}
	q += " ORDER BY created_at DESC"

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var projects []*store.Project
	for rows.Next() {
		p, err := scanProjectRow(rows)
		if err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

// Slug immutability is enforced by the ProjectStore interface itself — no
// UpdateSlug method exists. UpdateStatus and UpdateMetadata only touch the
// status and metadata columns respectively, so slug cannot be mutated through
// the public API. The slug couples to the FS workspace path
// ({ws}/projects/{slug}/...); changing it would orphan files silently.
//
// If a future generic Update(updates map[string]any) is added, that helper
// MUST strip the "slug" key before passing to the SQL builder — mirror the
// agent_key / team_key strip pattern.

// UpdateStatus sets the project status. Returns sql.ErrNoRows when id not found.
func (s *PGProjectStore) UpdateStatus(ctx context.Context, id uuid.UUID, status string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE projects SET status = $1, updated_at = $2 WHERE id = $3`,
		status, time.Now().UTC(), id,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// UpdateMetadata replaces the project metadata JSON. Returns sql.ErrNoRows when not found.
func (s *PGProjectStore) UpdateMetadata(ctx context.Context, id uuid.UUID, metadata json.RawMessage) error {
	if len(metadata) == 0 {
		metadata = json.RawMessage("{}")
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE projects SET metadata = $1, updated_at = $2 WHERE id = $3`,
		[]byte(metadata), time.Now().UTC(), id,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// scanProject scans a single projects row from a *sql.Row.
func scanProject(row *sql.Row) (*store.Project, error) {
	var p store.Project
	var meta []byte
	err := row.Scan(
		&p.ID, &p.Slug, &p.OwnerUserID, &p.Status, &meta,
		&p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	p.Metadata = json.RawMessage(jsonOrEmpty(meta))
	return &p, nil
}

// scanProjectRow scans a single projects row from *sql.Rows.
func scanProjectRow(rows *sql.Rows) (*store.Project, error) {
	var p store.Project
	var meta []byte
	err := rows.Scan(
		&p.ID, &p.Slug, &p.OwnerUserID, &p.Status, &meta,
		&p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	p.Metadata = json.RawMessage(jsonOrEmpty(meta))
	return &p, nil
}

// compile-time interface check
var _ store.ProjectStore = (*PGProjectStore)(nil)
