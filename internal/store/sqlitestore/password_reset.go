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

// SQLitePasswordResetStore implements store.PasswordResetStore on SQLite.
type SQLitePasswordResetStore struct {
	db *sql.DB
}

// NewSQLitePasswordResetStore returns a PasswordResetStore backed by SQLite.
func NewSQLitePasswordResetStore(db *sql.DB) *SQLitePasswordResetStore {
	return &SQLitePasswordResetStore{db: db}
}

func (s *SQLitePasswordResetStore) Insert(ctx context.Context, userID uuid.UUID, tokenHash string, expiresAt time.Time) (uuid.UUID, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, fmt.Errorf("uuid v7: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO password_reset_tokens (id, user_id, token_hash, expires_at)
		 VALUES (?, ?, ?, ?)`,
		id, userID, tokenHash, expiresAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return uuid.Nil, fmt.Errorf("insert password reset: %w", err)
	}
	return id, nil
}

func (s *SQLitePasswordResetStore) GetActive(ctx context.Context, tokenHash string) (uuid.UUID, time.Time, error) {
	var (
		userID  uuid.UUID
		expires string
	)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	err := s.db.QueryRowContext(ctx,
		`SELECT user_id, expires_at FROM password_reset_tokens
		 WHERE token_hash = ? AND used_at IS NULL AND expires_at > ?`,
		tokenHash, now).Scan(&userID, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, time.Time{}, store.ErrPasswordResetNotFound
	}
	if err != nil {
		return uuid.Nil, time.Time{}, fmt.Errorf("get active password reset: %w", err)
	}
	expiresAt, perr := time.Parse(time.RFC3339Nano, expires)
	if perr != nil {
		return uuid.Nil, time.Time{}, fmt.Errorf("parse expires_at: %w", perr)
	}
	return userID, expiresAt, nil
}

// MarkUsed performs the compare-and-set in a single SQL statement using the
// modernc.org/sqlite RETURNING extension (≥ v1.20). Filters on
// used_at IS NULL AND expires_at > now(); ErrNoRows collapses missing,
// expired, and already-used into ErrPasswordResetNotFound.
func (s *SQLitePasswordResetStore) MarkUsed(ctx context.Context, exec base.Executor, tokenHash string) (uuid.UUID, error) {
	if exec == nil {
		exec = s.db
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var userID uuid.UUID
	err := exec.QueryRowContext(ctx,
		`UPDATE password_reset_tokens
		 SET used_at = ?
		 WHERE token_hash = ? AND used_at IS NULL AND expires_at > ?
		 RETURNING user_id`,
		now, tokenHash, now).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, store.ErrPasswordResetNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("mark password reset used: %w", err)
	}
	return userID, nil
}

func (s *SQLitePasswordResetStore) DeleteExpired(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM password_reset_tokens WHERE expires_at < ?`,
		before.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("delete expired password resets: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, nil
}
