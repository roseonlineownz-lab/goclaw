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
	"github.com/nextlevelbuilder/goclaw/internal/store/base"
)

// SQLiteUserSessionsStore implements store.UserSessionsStore on SQLite.
type SQLiteUserSessionsStore struct {
	db *sql.DB
}

// NewSQLiteUserSessionsStore returns a UserSessionsStore backed by SQLite.
func NewSQLiteUserSessionsStore(db *sql.DB) *SQLiteUserSessionsStore {
	return &SQLiteUserSessionsStore{db: db}
}

const userSessionsSelectColumns = `id, user_id, family_id, refresh_token_hash,
	expires_at, revoked_at, created_at`

func (s *SQLiteUserSessionsStore) Create(ctx context.Context, sess *store.UserSession) error {
	if sess.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("uuid v7: %w", err)
		}
		sess.ID = id
	}
	if sess.CreatedAt.IsZero() {
		sess.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO user_sessions (id, user_id, family_id, refresh_token_hash, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		sess.ID, sess.UserID, sess.FamilyID, sess.RefreshTokenHash,
		sess.ExpiresAt.UTC().Format(time.RFC3339Nano),
		sess.CreatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("user_sessions insert: %w", err)
	}
	return nil
}

func (s *SQLiteUserSessionsStore) GetByHash(ctx context.Context, hash string) (*store.UserSession, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+userSessionsSelectColumns+` FROM user_sessions WHERE refresh_token_hash = ?`,
		hash)
	return scanSQLiteUserSession(row)
}

func (s *SQLiteUserSessionsStore) Revoke(ctx context.Context, id uuid.UUID) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx,
		`UPDATE user_sessions SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, now, id)
	if err != nil {
		return fmt.Errorf("user_sessions revoke: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		var exists bool
		if err := s.db.QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM user_sessions WHERE id = ?)`, id).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return store.ErrNotFound
		}
	}
	return nil
}

// RevokeAllActiveByUser bulk-revokes every active session for the user via a
// single UPDATE. Pass s.DB() for stand-alone use, or an active *sql.Tx when
// composing inside a cross-store transaction.
func (s *SQLiteUserSessionsStore) RevokeAllActiveByUser(ctx context.Context, exec base.Executor, userID uuid.UUID) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := exec.ExecContext(ctx,
		`UPDATE user_sessions SET revoked_at = ?
		   WHERE user_id = ? AND revoked_at IS NULL`, now, userID)
	if err != nil {
		return fmt.Errorf("user_sessions revoke all active: %w", err)
	}
	return nil
}

// DB exposes the underlying *sql.DB as a base.Executor so cross-store helpers
// can wrap their writes and this store's writes inside one transaction.
func (s *SQLiteUserSessionsStore) DB() base.Executor { return s.db }

func (s *SQLiteUserSessionsStore) RevokeFamily(ctx context.Context, familyID uuid.UUID) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx,
		`UPDATE user_sessions SET revoked_at = ?
		   WHERE family_id = ? AND revoked_at IS NULL`, now, familyID)
	if err != nil {
		return fmt.Errorf("user_sessions revoke family: %w", err)
	}
	return nil
}

func (s *SQLiteUserSessionsStore) ListActiveByUser(ctx context.Context, userID uuid.UUID) ([]store.UserSession, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+userSessionsSelectColumns+`
		   FROM user_sessions
		  WHERE user_id = ? AND revoked_at IS NULL AND expires_at > ?
		  ORDER BY created_at DESC`, userID, now)
	if err != nil {
		return nil, fmt.Errorf("user_sessions list active: %w", err)
	}
	defer rows.Close()
	var out []store.UserSession
	for rows.Next() {
		sess, err := scanSQLiteUserSessionRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sess)
	}
	return out, rows.Err()
}

func scanSQLiteUserSession(row *sql.Row) (*store.UserSession, error) {
	return scanSQLiteUserSessionRow(row)
}

func scanSQLiteUserSessionRow(r sqliteRowScanner) (*store.UserSession, error) {
	var sess store.UserSession
	var expiresAt sqliteTime
	var revokedAt nullSqliteTime
	var createdAt sqliteTime
	err := r.Scan(
		&sess.ID, &sess.UserID, &sess.FamilyID, &sess.RefreshTokenHash,
		&expiresAt, &revokedAt, &createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan user_session: %w", err)
	}
	sess.ExpiresAt = expiresAt.Time
	sess.CreatedAt = createdAt.Time
	if revokedAt.Valid {
		t := revokedAt.Time
		sess.RevokedAt = &t
	}
	return &sess, nil
}
