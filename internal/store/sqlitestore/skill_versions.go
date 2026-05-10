//go:build sqlite || sqliteonly

package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// SQLiteSkillVersionsStore implements store.SkillVersionsStore on SQLite.
type SQLiteSkillVersionsStore struct {
	db *sql.DB
}

// NewSQLiteSkillVersionsStore returns a SkillVersionsStore backed by SQLite.
func NewSQLiteSkillVersionsStore(db *sql.DB) *SQLiteSkillVersionsStore {
	return &SQLiteSkillVersionsStore{db: db}
}

const skillVersionsSelectColumns = `id, skill_id, version, file_hash, file_path,
	file_size, frontmatter, content, changelog, published_by, created_at, archived_at, archive_path`

func (s *SQLiteSkillVersionsStore) Create(ctx context.Context, v *store.SkillVersion) error {
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
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO skill_versions
			(id, skill_id, version, file_hash, file_path, file_size, frontmatter, content, changelog, published_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		v.ID, v.SkillID, v.Version, v.FileHash, v.FilePath, v.FileSize,
		string(v.Frontmatter), v.Content, nilStr(deref(v.Changelog)), nilStr(deref(v.PublishedBy)),
	)
	if err != nil {
		return fmt.Errorf("skill_versions insert: %w", err)
	}
	row := s.db.QueryRowContext(ctx,
		`SELECT `+skillVersionsSelectColumns+` FROM skill_versions WHERE id = ?`, v.ID)
	got, err := scanSQLiteSkillVersion(row)
	if err != nil {
		return err
	}
	*v = *got
	return nil
}

func (s *SQLiteSkillVersionsStore) ListBySkillID(ctx context.Context, skillID uuid.UUID) ([]store.SkillVersion, error) {
	return s.ListBySkillIDFiltered(ctx, skillID, true)
}

// ListBySkillIDFiltered lists versions for a skill. When includeArchived=false, archived rows are excluded.
func (s *SQLiteSkillVersionsStore) ListBySkillIDFiltered(ctx context.Context, skillID uuid.UUID, includeArchived bool) ([]store.SkillVersion, error) {
	q := `SELECT ` + skillVersionsSelectColumns + `
		   FROM skill_versions
		  WHERE skill_id = ?`
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
		v, err := scanSQLiteSkillVersionRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, rows.Err()
}

// GetActive returns the highest non-archived version row for the skill, or ErrNotFound.
func (s *SQLiteSkillVersionsStore) GetActive(ctx context.Context, skillID uuid.UUID) (*store.SkillVersion, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+skillVersionsSelectColumns+`
		   FROM skill_versions
		  WHERE skill_id = ? AND archived_at IS NULL
		  ORDER BY version DESC
		  LIMIT 1`, skillID)
	return scanSQLiteSkillVersion(row)
}

func (s *SQLiteSkillVersionsStore) Delete(ctx context.Context, id uuid.UUID) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM skill_versions WHERE id = ?`, id)
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

// Archive sets archived_at, archive_path on a version and clears its content.
// Returns ErrNotFound if the version does not exist or is already archived.
func (s *SQLiteSkillVersionsStore) Archive(ctx context.Context, id, skillID uuid.UUID, archivePath string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx, `
		UPDATE skill_versions
		   SET archived_at = ?, archive_path = ?, content = ''
		 WHERE id = ? AND skill_id = ? AND archived_at IS NULL`, now, archivePath, id, skillID)
	if err != nil {
		return fmt.Errorf("skill_versions archive: %w", err)
	}
	return rowsAffectedNotFoundSQLite(res)
}

func scanSQLiteSkillVersion(row *sql.Row) (*store.SkillVersion, error) {
	return scanSQLiteSkillVersionRow(row)
}

func scanSQLiteSkillVersionRow(r sqliteRowScanner) (*store.SkillVersion, error) {
	var v store.SkillVersion
	var changelog, publishedBy, archivePath *string
	var frontmatter []byte
	var createdAt sqliteTime
	var archivedAt nullSqliteTime
	err := r.Scan(
		&v.ID, &v.SkillID, &v.Version, &v.FileHash, &v.FilePath, &v.FileSize,
		&frontmatter, &v.Content, &changelog, &publishedBy, &createdAt, &archivedAt, &archivePath,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan skill_version: %w", err)
	}
	v.Frontmatter = frontmatter
	v.Changelog = changelog
	v.PublishedBy = publishedBy
	v.CreatedAt = createdAt.Time
	if archivedAt.Valid {
		t := archivedAt.Time
		v.ArchivedAt = &t
	}
	v.ArchivePath = archivePath
	return &v, nil
}
