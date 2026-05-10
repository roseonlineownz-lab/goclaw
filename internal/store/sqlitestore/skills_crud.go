//go:build sqlite || sqliteonly

package sqlitestore

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

func (s *SQLiteSkillStore) CreateSkill(name, slug string, description *string, ownerID, visibility string, version int, filePath string, fileSize int64, fileHash *string) error {
	id := store.GenNewID()
	now := time.Now().UTC()
	_, err := s.db.Exec(
		`INSERT INTO skills (id, name, slug, description, owner_id, visibility, version, status, file_path, file_size, file_hash, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 'active', ?, ?, ?, ?, ?)`,
		id, name, slug, description, ownerID, visibility, version, filePath, fileSize, fileHash, now, now,
	)
	if err == nil {
		s.BumpVersion()
	}
	return err
}

func (s *SQLiteSkillStore) UpdateSkill(ctx context.Context, id uuid.UUID, updates map[string]any) error {
	if err := execMapUpdate(ctx, s.db, "skills", id, updates); err != nil {
		return err
	}
	s.BumpVersion()
	return nil
}

func (s *SQLiteSkillStore) DeleteSkill(ctx context.Context, id uuid.UUID) error {
	var source string
	if err := s.db.QueryRowContext(ctx, "SELECT source FROM skills WHERE id = ?", id).Scan(&source); err != nil {
		return fmt.Errorf("check skill: %w", err)
	}
	if source == "builtin" {
		return fmt.Errorf("cannot delete system skill")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "DELETE FROM skill_agent_grants WHERE skill_id = ?", id); err != nil {
		return fmt.Errorf("delete skill grants: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM skill_user_grants WHERE skill_id = ?", id); err != nil {
		return fmt.Errorf("delete skill user grants: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE skills SET status = 'deleted' WHERE id = ?", id); err != nil {
		return fmt.Errorf("delete skill: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	s.BumpVersion()
	return nil
}

// CreateSkillManaged creates or updates a skill from upload parameters.
// SQLite uses a serializable transaction to avoid the race on version calculation.
func (s *SQLiteSkillStore) CreateSkillManaged(ctx context.Context, p store.SkillCreateParams) (uuid.UUID, error) {
	if err := store.ValidateUserID(p.OwnerID); err != nil {
		return uuid.Nil, err
	}

	fmJSON := marshalFrontmatter(p.Frontmatter)
	depsJSON, err := marshalMissingDeps(p.MissingDeps)
	if err != nil {
		return uuid.Nil, fmt.Errorf("marshal deps: %w", err)
	}
	status := p.Status
	if status == "" {
		status = "active"
	}
	source := p.Source
	if source == "" {
		source = "user-uploaded"
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return uuid.Nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// Compute next version atomically under the transaction lock.
	var version int
	if err := tx.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(version), 0) + 1 FROM skills WHERE slug = ?", p.Slug,
	).Scan(&version); err != nil {
		return uuid.Nil, fmt.Errorf("get next version: %w", err)
	}

	now := time.Now().UTC()

	// Check for existing skill with same slug (for upsert).
	var existingID uuid.UUID
	err = tx.QueryRowContext(ctx,
		"SELECT id FROM skills WHERE slug = ?", p.Slug,
	).Scan(&existingID)

	var returnedID uuid.UUID
	if err == nil {
		// Update existing.
		_, err = tx.ExecContext(ctx,
			`UPDATE skills SET name = ?, description = ?, version = ?, frontmatter = ?,
			 file_path = ?, file_size = ?, file_hash = ?, deps = ?,
			 visibility = CASE WHEN status IN ('archived', 'deleted') THEN 'private' ELSE visibility END,
			 status = ?, updated_at = ? WHERE id = ?`,
			p.Name, p.Description, version, fmJSON,
			p.FilePath, p.FileSize, p.FileHash, depsJSON, status, now, existingID,
		)
		if err != nil {
			return uuid.Nil, fmt.Errorf("update skill: %w", err)
		}
		returnedID = existingID
	} else {
		// Insert new.
		newID := store.GenNewID()
		_, err = tx.ExecContext(ctx,
			`INSERT INTO skills (id, name, slug, description, owner_id, visibility, version, status, source, deps, frontmatter, file_path, file_size, file_hash, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			newID, p.Name, p.Slug, p.Description, p.OwnerID, p.Visibility, version,
			status, source, depsJSON, fmJSON, p.FilePath, p.FileSize, p.FileHash, now, now,
		)
		if err != nil {
			return uuid.Nil, fmt.Errorf("insert skill: %w", err)
		}
		returnedID = newID
	}

	if err := tx.Commit(); err != nil {
		return uuid.Nil, fmt.Errorf("commit: %w", err)
	}

	s.BumpVersion()
	return returnedID, nil
}

func (s *SQLiteSkillStore) GetSkillFilePath(ctx context.Context, id uuid.UUID) (filePath string, slug string, version int, source string, ok bool) {
	err := s.db.QueryRowContext(ctx,
		"SELECT file_path, slug, version, source FROM skills WHERE id = ? AND status = 'active'", id,
	).Scan(&filePath, &slug, &version, &source)
	return filePath, slug, version, source, err == nil
}

// MarkSkillUsed updates last_used_at and increments usage_count (best-effort sidecar).
func (s *SQLiteSkillStore) MarkSkillUsed(ctx context.Context, id uuid.UUID) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`UPDATE skills SET last_used_at = ?, usage_count = usage_count + 1 WHERE id = ?`,
		now.Format(time.RFC3339Nano), id)
	return err
}

// MarkSkillViewed updates last_viewed_at (best-effort sidecar).
func (s *SQLiteSkillStore) MarkSkillViewed(ctx context.Context, id uuid.UUID) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`UPDATE skills SET last_viewed_at = ? WHERE id = ?`,
		now.Format(time.RFC3339Nano), id)
	return err
}

// PinSkill sets the pinned flag for a skill.
func (s *SQLiteSkillStore) PinSkill(ctx context.Context, id uuid.UUID, pinned bool) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`UPDATE skills SET pinned = ?, updated_at = ? WHERE id = ?`,
		pinned, now.Format(time.RFC3339Nano), id)
	if err == nil {
		s.BumpVersion()
	}
	return err
}

// GetSkillHashBySlug returns the file_hash and version of the latest non-deleted skill
// version for the given slug. Returns ok=false when no matching row exists.
func (s *SQLiteSkillStore) GetSkillHashBySlug(ctx context.Context, slug string) (string, int, bool) {
	var hash string
	var version int
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(file_hash, ''), version FROM skills
		 WHERE slug = ? AND status != 'deleted'
		 ORDER BY version DESC LIMIT 1`,
		slug,
	).Scan(&hash, &version)
	return hash, version, err == nil
}

func (s *SQLiteSkillStore) GetNextVersion(ctx context.Context, slug string) int {
	var maxVersion int
	s.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM skills WHERE slug = ?", slug).Scan(&maxVersion)
	return maxVersion + 1
}

// GetNextVersionLocked returns the next version for a slug.
// SQLite has no advisory locks; returns a no-op closer.
func (s *SQLiteSkillStore) GetNextVersionLocked(ctx context.Context, slug string) (int, func() error, error) {
	return s.GetNextVersion(ctx, slug), func() error { return nil }, nil
}

func (s *SQLiteSkillStore) ToggleSkill(ctx context.Context, id uuid.UUID, enabled bool) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`UPDATE skills SET enabled = ?, updated_at = ? WHERE id = ?`, enabled, now, id)
	if err == nil {
		s.BumpVersion()
	}
	return err
}
