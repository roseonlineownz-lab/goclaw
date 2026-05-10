//go:build sqlite || sqliteonly

package sqlitestore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// SQLiteProjectStore implements store.ProjectStore backed by SQLite.
type SQLiteProjectStore struct {
	db *sql.DB
}

// NewSQLiteProjectStore creates a new SQLite-backed project store.
func NewSQLiteProjectStore(db *sql.DB) *SQLiteProjectStore {
	return &SQLiteProjectStore{db: db}
}

// Create inserts a new project. UUID v7 is generated in Go when p.ID is uuid.Nil.
func (s *SQLiteProjectStore) Create(ctx context.Context, p *store.Project) error {
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
	if p.Status == "" {
		p.Status = "active"
	}
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO projects (id, slug, owner_user_id, status, metadata, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		p.ID.String(), p.Slug, p.OwnerUserID.String(), p.Status,
		string(p.Metadata), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
	)
	if err != nil {
		return err
	}
	p.CreatedAt = now
	p.UpdatedAt = now
	return nil
}

// Get fetches a project by UUID. Returns sql.ErrNoRows when not found.
func (s *SQLiteProjectStore) Get(ctx context.Context, id uuid.UUID) (*store.Project, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, slug, owner_user_id, status, metadata, created_at, updated_at
		 FROM projects WHERE id = ?`,
		id.String(),
	)
	return scanSQLiteProject(row)
}

// GetBySlug fetches a project by its unique slug. Returns sql.ErrNoRows when not found.
func (s *SQLiteProjectStore) GetBySlug(ctx context.Context, slug string) (*store.Project, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, slug, owner_user_id, status, metadata, created_at, updated_at
		 FROM projects WHERE slug = ?`,
		slug,
	)
	return scanSQLiteProject(row)
}

// List returns projects matching the filter, ordered by created_at DESC.
func (s *SQLiteProjectStore) List(ctx context.Context, f store.ListProjectsFilter) ([]*store.Project, error) {
	q := `SELECT id, slug, owner_user_id, status, metadata, created_at, updated_at
	      FROM projects WHERE 1=1`
	var args []any

	if f.OwnerUserID != uuid.Nil {
		q += " AND owner_user_id = ?"
		args = append(args, f.OwnerUserID.String())
	}
	if f.Status != "" {
		q += " AND status = ?"
		args = append(args, f.Status)
	}
	q += " ORDER BY created_at DESC"

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var projects []*store.Project
	for rows.Next() {
		p, err := scanSQLiteProjectRow(rows)
		if err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

// UpdateStatus sets the project status. Returns sql.ErrNoRows when id not found.
func (s *SQLiteProjectStore) UpdateStatus(ctx context.Context, id uuid.UUID, status string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE projects SET status = ?, updated_at = ? WHERE id = ?`,
		status, time.Now().UTC().Format(time.RFC3339Nano), id.String(),
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
func (s *SQLiteProjectStore) UpdateMetadata(ctx context.Context, id uuid.UUID, metadata json.RawMessage) error {
	if len(metadata) == 0 {
		metadata = json.RawMessage("{}")
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE projects SET metadata = ?, updated_at = ? WHERE id = ?`,
		string(metadata), time.Now().UTC().Format(time.RFC3339Nano), id.String(),
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

// scanSQLiteProject scans a single projects row from *sql.Row.
func scanSQLiteProject(row *sql.Row) (*store.Project, error) {
	var p store.Project
	var idStr, ownerStr string
	var meta []byte
	createdAt, updatedAt := scanTimePair()
	err := row.Scan(
		&idStr, &p.Slug, &ownerStr, &p.Status, &meta,
		createdAt, updatedAt,
	)
	if err != nil {
		return nil, err
	}
	p.ID, err = uuid.Parse(idStr)
	if err != nil {
		return nil, fmt.Errorf("project id: %w", err)
	}
	p.OwnerUserID, err = uuid.Parse(ownerStr)
	if err != nil {
		return nil, fmt.Errorf("project owner_user_id: %w", err)
	}
	p.Metadata = json.RawMessage(jsonOrEmpty(meta))
	p.CreatedAt = createdAt.Time
	p.UpdatedAt = updatedAt.Time
	return &p, nil
}

// scanSQLiteProjectRow scans a single projects row from *sql.Rows.
func scanSQLiteProjectRow(rows *sql.Rows) (*store.Project, error) {
	var p store.Project
	var idStr, ownerStr string
	var meta []byte
	createdAt, updatedAt := scanTimePair()
	err := rows.Scan(
		&idStr, &p.Slug, &ownerStr, &p.Status, &meta,
		createdAt, updatedAt,
	)
	if err != nil {
		return nil, err
	}
	p.ID, err = uuid.Parse(idStr)
	if err != nil {
		return nil, fmt.Errorf("project id: %w", err)
	}
	p.OwnerUserID, err = uuid.Parse(ownerStr)
	if err != nil {
		return nil, fmt.Errorf("project owner_user_id: %w", err)
	}
	p.Metadata = json.RawMessage(jsonOrEmpty(meta))
	p.CreatedAt = createdAt.Time
	p.UpdatedAt = updatedAt.Time
	return &p, nil
}

// compile-time interface check
var _ store.ProjectStore = (*SQLiteProjectStore)(nil)
