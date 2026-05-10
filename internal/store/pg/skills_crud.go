package pg

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

func (s *PGSkillStore) CreateSkill(name, slug string, description *string, ownerID, visibility string, version int, filePath string, fileSize int64, fileHash *string) error {
	id := store.GenNewID()
	_, err := s.db.Exec(
		`INSERT INTO skills (id, name, slug, description, owner_id, visibility, version, status, file_path, file_size, file_hash, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, 'active', $8, $9, $10, NOW(), NOW())`,
		id, name, slug, description, ownerID, visibility, version, filePath, fileSize, fileHash,
	)
	if err == nil {
		s.BumpVersion()
	}
	return err
}

func (s *PGSkillStore) UpdateSkill(ctx context.Context, id uuid.UUID, updates map[string]any) error {
	if err := execMapUpdate(ctx, s.db, "skills", id, updates); err != nil {
		return err
	}
	s.BumpVersion()
	return nil
}

func (s *PGSkillStore) DeleteSkill(ctx context.Context, id uuid.UUID) error {
	// Reject deletion of builtin skills.
	var source string
	if err := s.db.QueryRowContext(ctx, "SELECT source FROM skills WHERE id = $1", id).Scan(&source); err != nil {
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

	// Cascade: remove all agent grants for this skill.
	if _, err := tx.ExecContext(ctx, "DELETE FROM skill_agent_grants WHERE skill_id = $1", id); err != nil {
		return fmt.Errorf("delete skill grants: %w", err)
	}

	// Cascade: remove all user grants for this skill.
	if _, err := tx.ExecContext(ctx, "DELETE FROM skill_user_grants WHERE skill_id = $1", id); err != nil {
		return fmt.Errorf("delete skill user grants: %w", err)
	}

	// Soft-delete the skill (use 'deleted' status, distinct from 'archived' which means missing deps).
	if _, err := tx.ExecContext(ctx, "UPDATE skills SET status = 'deleted' WHERE id = $1", id); err != nil {
		return fmt.Errorf("delete skill: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	s.BumpVersion()
	return nil
}

// slugAdvisoryLock returns a stable int64 lock key derived from the slug,
// suitable for use with pg_advisory_xact_lock. Serializes concurrent callers
// on the same slug so version calculation and upsert are atomic.
func slugAdvisoryLock(slug string) int64 {
	h := fnv.New64a()
	h.Write([]byte(slug))
	return int64(h.Sum64())
}

// CreateSkillManaged creates or updates a skill from upload parameters.
// It uses a transaction with an advisory lock on the slug to prevent concurrent
// callers from racing on version calculation and upsert.
// The RETURNING id clause ensures the actual row ID is returned (new insert or
// existing row on conflict), so callers always receive a valid ID.
func (s *PGSkillStore) CreateSkillManaged(ctx context.Context, p store.SkillCreateParams) (uuid.UUID, error) {
	if err := store.ValidateUserID(p.OwnerID); err != nil {
		return uuid.Nil, err
	}

	// Marshal frontmatter to JSON for DB storage.
	fmJSON := []byte("{}")
	if len(p.Frontmatter) > 0 {
		if b, err := json.Marshal(p.Frontmatter); err == nil {
			fmJSON = b
		}
	}
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

	// Acquire advisory lock scoped to this transaction so concurrent calls for
	// the same slug serialize version calculation and the upsert atomically.
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", slugAdvisoryLock(p.Slug)); err != nil {
		return uuid.Nil, fmt.Errorf("advisory lock: %w", err)
	}

	// Compute next version atomically under the lock.
	var version int
	if err := tx.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(version), 0) + 1 FROM skills WHERE slug = $1",
		p.Slug,
	).Scan(&version); err != nil {
		return uuid.Nil, fmt.Errorf("get next version: %w", err)
	}

	id := store.GenNewID()
	var returnedID uuid.UUID
	err = tx.QueryRowContext(ctx,
		`INSERT INTO skills (id, name, slug, description, owner_id, visibility, version, status, source, deps, frontmatter, file_path, file_size, file_hash, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, NOW(), NOW())
		 ON CONFLICT (slug) DO UPDATE SET
		   name = EXCLUDED.name, description = EXCLUDED.description,
		   version = EXCLUDED.version, frontmatter = EXCLUDED.frontmatter,
		   file_path = EXCLUDED.file_path, deps = EXCLUDED.deps,
		   file_size = EXCLUDED.file_size, file_hash = EXCLUDED.file_hash,
		   visibility = CASE WHEN skills.status IN ('archived', 'deleted') THEN 'private' ELSE skills.visibility END,
		   status = EXCLUDED.status, updated_at = NOW()
		 RETURNING id`,
		id, p.Name, p.Slug, p.Description, p.OwnerID, p.Visibility, version,
		status, source, depsJSON, fmJSON, p.FilePath, p.FileSize, p.FileHash,
	).Scan(&returnedID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("upsert skill: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return uuid.Nil, fmt.Errorf("commit: %w", err)
	}

	s.BumpVersion()
	// Generate embedding asynchronously.
	desc := ""
	if p.Description != nil {
		desc = *p.Description
	}
	go s.generateEmbedding(context.Background(), p.Slug, p.Name, desc)

	return returnedID, nil
}

// GetSkillFilePath returns the filesystem path, slug, version, source, and ok for a skill by UUID.
// source == "builtin" means the skill is a builtin/system skill.
func (s *PGSkillStore) GetSkillFilePath(ctx context.Context, id uuid.UUID) (filePath string, slug string, version int, source string, ok bool) {
	err := s.db.QueryRowContext(ctx,
		"SELECT file_path, slug, version, source FROM skills WHERE id = $1 AND status = 'active'",
		id,
	).Scan(&filePath, &slug, &version, &source)
	return filePath, slug, version, source, err == nil
}

// MarkSkillUsed updates last_used_at and increments usage_count (best-effort sidecar).
func (s *PGSkillStore) MarkSkillUsed(ctx context.Context, id uuid.UUID) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE skills SET last_used_at = NOW(), usage_count = usage_count + 1 WHERE id = $1`, id)
	return err
}

// MarkSkillViewed updates last_viewed_at (best-effort sidecar).
func (s *PGSkillStore) MarkSkillViewed(ctx context.Context, id uuid.UUID) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE skills SET last_viewed_at = NOW() WHERE id = $1`, id)
	return err
}

// PinSkill sets the pinned flag for a skill.
func (s *PGSkillStore) PinSkill(ctx context.Context, id uuid.UUID, pinned bool) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE skills SET pinned = $1, updated_at = NOW() WHERE id = $2`, pinned, id)
	if err == nil {
		s.BumpVersion()
	}
	return err
}

// GetNextVersion returns the next version number for a skill slug.
// NOTE: This function has an inherent race condition — two concurrent callers
// for the same slug can receive the same version number. Use it only for
// informational purposes (e.g. display). For write paths, use CreateSkillManaged
// which computes the version atomically under a pg_advisory_xact_lock.
func (s *PGSkillStore) GetNextVersion(ctx context.Context, slug string) int {
	var maxVersion int
	s.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM skills WHERE slug = $1", slug).Scan(&maxVersion)
	return maxVersion + 1
}

// GetSkillHashBySlug returns the file_hash and version of the latest non-deleted skill
// version for the given slug.
// Returns ok=false when no matching row exists.
func (s *PGSkillStore) GetSkillHashBySlug(ctx context.Context, slug string) (string, int, bool) {
	var hash string
	var version int
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(file_hash, ''), version FROM skills
		 WHERE slug = $1 AND status != 'deleted'
		 ORDER BY version DESC LIMIT 1`,
		slug,
	).Scan(&hash, &version)
	return hash, version, err == nil
}

// GetNextVersionLocked computes the next version atomically using an advisory lock.
// Safe for concurrent write paths (patch, create). Returns version and a cleanup func
// that MUST be called to release the lock (commits the transaction).
func (s *PGSkillStore) GetNextVersionLocked(ctx context.Context, slug string) (int, func() error, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, nil, fmt.Errorf("begin tx: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", slugAdvisoryLock(slug)); err != nil {
		tx.Rollback()
		return 0, nil, fmt.Errorf("advisory lock: %w", err)
	}
	var version int
	if err := tx.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(version), 0) + 1 FROM skills WHERE slug = $1", slug,
	).Scan(&version); err != nil {
		tx.Rollback()
		return 0, nil, fmt.Errorf("get next version: %w", err)
	}
	return version, func() error { return tx.Commit() }, nil
}

// ToggleSkill enables or disables a skill by UUID.
func (s *PGSkillStore) ToggleSkill(ctx context.Context, id uuid.UUID, enabled bool) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE skills SET enabled = $1, updated_at = NOW() WHERE id = $2`,
		enabled, id,
	)
	if err == nil {
		s.BumpVersion()
	}
	return err
}

// parseDepsColumn extracts the missing deps list from the deps JSONB column.
func parseDepsColumn(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	var d struct {
		Missing []string `json:"missing"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil
	}
	if len(d.Missing) == 0 {
		return nil
	}
	return d.Missing
}

func parseFrontmatterAuthor(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var fm map[string]string
	if err := json.Unmarshal(raw, &fm); err != nil {
		return ""
	}
	return fm["author"]
}

func marshalFrontmatter(fm map[string]string) []byte {
	if len(fm) == 0 {
		return []byte("{}")
	}
	b, err := json.Marshal(fm)
	if err != nil {
		return []byte("{}")
	}
	return b
}
