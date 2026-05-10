package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// SkillVersion mirrors a row of `skill_versions`.
//
// Schema has UNIQUE(skill_id, version). Archived versions have archived_at set
// and content cleared. Active version = MAX(version) WHERE archived_at IS NULL per skill.
type SkillVersion struct {
	ID          uuid.UUID       `db:"id"`
	SkillID     uuid.UUID       `db:"skill_id"`
	Version     int             `db:"version"`
	FileHash    string          `db:"file_hash"`
	FilePath    string          `db:"file_path"`
	FileSize    int64           `db:"file_size"`
	Frontmatter json.RawMessage `db:"frontmatter"`
	Content     string          `db:"content"`
	Changelog   *string         `db:"changelog"`
	PublishedBy *string         `db:"published_by"`
	Metadata    json.RawMessage `db:"metadata"`
	CreatedAt   time.Time       `db:"created_at"`
	ArchivedAt  *time.Time      `db:"archived_at"`
	ArchivePath *string         `db:"archive_path"`
}

// SkillVersionsStore manages immutable skill version snapshots.
type SkillVersionsStore interface {
	Create(ctx context.Context, v *SkillVersion) error
	ListBySkillID(ctx context.Context, skillID uuid.UUID) ([]SkillVersion, error)
	// ListBySkillIDFiltered lists versions for a skill. When includeArchived=false (default),
	// only non-archived versions are returned.
	ListBySkillIDFiltered(ctx context.Context, skillID uuid.UUID, includeArchived bool) ([]SkillVersion, error)
	GetActive(ctx context.Context, skillID uuid.UUID) (*SkillVersion, error)
	Delete(ctx context.Context, id uuid.UUID) error
	// Archive sets archived_at=NOW(), archive_path, and clears content. The skillID
	// guard prevents cross-skill archive attempts (caller must supply the parent
	// skill UUID; mismatched pairs return ErrNotFound). Returns ErrNotFound when the
	// version is already archived or no row matches (id, skillID, archived_at IS NULL).
	Archive(ctx context.Context, id, skillID uuid.UUID, archivePath string) error
}
