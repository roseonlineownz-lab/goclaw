package pg

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// PGSkillVersionsStore implements store.SkillVersionsStore on PostgreSQL.
type PGSkillVersionsStore struct {
	db *sql.DB
}

// NewPGSkillVersionsStore returns a SkillVersionsStore backed by Postgres.
func NewPGSkillVersionsStore(db *sql.DB) *PGSkillVersionsStore {
	return &PGSkillVersionsStore{db: db}
}

const skillVersionsSelectColumns = `id, skill_id, version, file_hash, file_path,
	file_size, frontmatter, content, changelog, published_by, metadata, created_at, archived_at, archive_path`

func (s *PGSkillVersionsStore) Create(ctx context.Context, v *store.SkillVersion) error {
	if v.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("uuid v7: %w", err)
		}
		v.ID = id
	}
	if len(v.Frontmatter) == 0 {
		v.Frontmatter = []byte("{}")
	}
	meta := v.Metadata
	if len(meta) == 0 {
		meta = []byte("{}")
	}
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO skill_versions
			(id, skill_id, version, file_hash, file_path, file_size, frontmatter, content, changelog, published_by, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING `+skillVersionsSelectColumns,
		v.ID, v.SkillID, v.Version, v.FileHash, v.FilePath, v.FileSize,
		v.Frontmatter, v.Content, nilStr(deref(v.Changelog)), nilStr(deref(v.PublishedBy)), meta,
	)
	return scanSkillVersion(row, v)
}

func (s *PGSkillVersionsStore) ListBySkillID(ctx context.Context, skillID uuid.UUID) ([]store.SkillVersion, error) {
	return s.ListBySkillIDFiltered(ctx, skillID, true)
}

// ListBySkillIDFiltered lists versions for a skill. When includeArchived=false, archived rows are excluded.
func (s *PGSkillVersionsStore) ListBySkillIDFiltered(ctx context.Context, skillID uuid.UUID, includeArchived bool) ([]store.SkillVersion, error) {
	q := `SELECT ` + skillVersionsSelectColumns + `
		   FROM skill_versions
		  WHERE skill_id = $1`
	if !includeArchived {
		q += ` AND archived_at IS NULL`
	}
	q += ` ORDER BY version DESC`
	rows, err := s.db.QueryContext(ctx, q, skillID)
	if err != nil {
		return nil, fmt.Errorf("skill_versions list: %w", err)
	}
	defer rows.Close()
	var out []store.SkillVersion
	for rows.Next() {
		var v store.SkillVersion
		if err := scanSkillVersion(rows, &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// GetActive returns the highest non-archived version row for the skill, or ErrNotFound.
func (s *PGSkillVersionsStore) GetActive(ctx context.Context, skillID uuid.UUID) (*store.SkillVersion, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+skillVersionsSelectColumns+`
		   FROM skill_versions
		  WHERE skill_id = $1 AND archived_at IS NULL
		  ORDER BY version DESC
		  LIMIT 1`, skillID)
	var v store.SkillVersion
	if err := scanSkillVersion(row, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

func (s *PGSkillVersionsStore) Delete(ctx context.Context, id uuid.UUID) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM skill_versions WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("skill_versions delete: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

// Archive sets archived_at=NOW(), archive_path on a version and clears its content.
// Returns ErrNotFound if the version does not exist or is already archived.
func (s *PGSkillVersionsStore) Archive(ctx context.Context, id, skillID uuid.UUID, archivePath string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE skill_versions
		   SET archived_at = NOW(), archive_path = $3, content = ''
		 WHERE id = $1 AND skill_id = $2 AND archived_at IS NULL`, id, skillID, archivePath)
	if err != nil {
		return fmt.Errorf("skill_versions archive: %w", err)
	}
	return rowsAffectedNotFound(res)
}

func scanSkillVersion(r rowScanner, v *store.SkillVersion) error {
	var changelog, publishedBy, archivePath *string
	err := r.Scan(
		&v.ID, &v.SkillID, &v.Version, &v.FileHash, &v.FilePath, &v.FileSize,
		&v.Frontmatter, &v.Content, &changelog, &publishedBy, &v.Metadata, &v.CreatedAt,
		&v.ArchivedAt, &archivePath,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("scan skill_version: %w", err)
	}
	v.Changelog = changelog
	v.PublishedBy = publishedBy
	v.ArchivePath = archivePath
	return nil
}
