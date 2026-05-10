package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Project represents a top-level project entity.
// Slug is immutable post-create — it couples to the FS workspace path.
type Project struct {
	ID          uuid.UUID       `db:"id"`
	Slug        string          `db:"slug"`
	OwnerUserID uuid.UUID       `db:"owner_user_id"`
	Status      string          `db:"status"`
	Metadata    json.RawMessage `db:"metadata"`
	CreatedAt   time.Time       `db:"created_at"`
	UpdatedAt   time.Time       `db:"updated_at"`
}

// ListProjectsFilter constrains Project list queries.
// Zero-value fields are ignored (no filtering on that dimension).
type ListProjectsFilter struct {
	OwnerUserID uuid.UUID // filter by owner; zero → all owners
	Status      string    // filter by status; "" → all statuses
}

// ProjectStore defines CRUD operations for projects.
// Delete is intentionally absent in rc1 — use UpdateStatus("archived") instead.
type ProjectStore interface {
	// Create inserts a new project. p.ID is set on success if it was uuid.Nil.
	Create(ctx context.Context, p *Project) error

	// Get fetches a project by UUID. Returns sql.ErrNoRows when not found.
	Get(ctx context.Context, id uuid.UUID) (*Project, error)

	// GetBySlug fetches a project by its unique slug. Returns sql.ErrNoRows when not found.
	GetBySlug(ctx context.Context, slug string) (*Project, error)

	// List returns projects matching the filter, ordered by created_at DESC.
	List(ctx context.Context, f ListProjectsFilter) ([]*Project, error)

	// UpdateStatus sets the project status to "active" or "archived".
	// Returns sql.ErrNoRows when id is not found.
	UpdateStatus(ctx context.Context, id uuid.UUID, status string) error

	// UpdateMetadata replaces the project metadata JSON.
	// Returns sql.ErrNoRows when id is not found.
	UpdateMetadata(ctx context.Context, id uuid.UUID, metadata json.RawMessage) error
}
