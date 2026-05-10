package pg

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

// PGPasswordResetStore implements store.PasswordResetStore on PostgreSQL.
type PGPasswordResetStore struct {
	db *sql.DB
}

// NewPGPasswordResetStore returns a PasswordResetStore backed by Postgres.
func NewPGPasswordResetStore(db *sql.DB) *PGPasswordResetStore {
	return &PGPasswordResetStore{db: db}
}

func (s *PGPasswordResetStore) Insert(ctx context.Context, userID uuid.UUID, tokenHash string, expiresAt time.Time) (uuid.UUID, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, fmt.Errorf("uuid v7: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO password_reset_tokens (id, user_id, token_hash, expires_at)
		 VALUES ($1, $2, $3, $4)`,
		id, userID, tokenHash, expiresAt); err != nil {
		return uuid.Nil, fmt.Errorf("insert password reset: %w", err)
	}
	return id, nil
}

func (s *PGPasswordResetStore) GetActive(ctx context.Context, tokenHash string) (uuid.UUID, time.Time, error) {
	var (
		userID    uuid.UUID
		expiresAt time.Time
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT user_id, expires_at FROM password_reset_tokens
		 WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()`,
		tokenHash).Scan(&userID, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, time.Time{}, store.ErrPasswordResetNotFound
	}
	if err != nil {
		return uuid.Nil, time.Time{}, fmt.Errorf("get active password reset: %w", err)
	}
	return userID, expiresAt, nil
}

// MarkUsed performs the compare-and-set in a single SQL statement so two
// concurrent callers cannot both succeed. The WHERE clause filters on
// used_at IS NULL AND expires_at > NOW(); zero rows updated → token is
// missing, expired, or already used (collapsed into ErrPasswordResetNotFound
// to avoid enumeration).
func (s *PGPasswordResetStore) MarkUsed(ctx context.Context, exec base.Executor, tokenHash string) (uuid.UUID, error) {
	if exec == nil {
		exec = s.db
	}
	var userID uuid.UUID
	err := exec.QueryRowContext(ctx,
		`UPDATE password_reset_tokens
		 SET used_at = now()
		 WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()
		 RETURNING user_id`,
		tokenHash).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, store.ErrPasswordResetNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("mark password reset used: %w", err)
	}
	return userID, nil
}

func (s *PGPasswordResetStore) DeleteExpired(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM password_reset_tokens WHERE expires_at < $1`, before)
	if err != nil {
		return 0, fmt.Errorf("delete expired password resets: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, nil
}
