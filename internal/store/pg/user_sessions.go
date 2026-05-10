package pg

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/store/base"
)

// PGUserSessionsStore implements store.UserSessionsStore on PostgreSQL.
type PGUserSessionsStore struct {
	db *sql.DB
}

// NewPGUserSessionsStore returns a UserSessionsStore backed by Postgres.
func NewPGUserSessionsStore(db *sql.DB) *PGUserSessionsStore {
	return &PGUserSessionsStore{db: db}
}

const userSessionsSelectColumns = `id, user_id, family_id, refresh_token_hash,
	expires_at, revoked_at, metadata, created_at`

func (s *PGUserSessionsStore) Create(ctx context.Context, sess *store.UserSession) error {
	if sess.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("uuid v7: %w", err)
		}
		sess.ID = id
	}
	if len(sess.Metadata) == 0 {
		sess.Metadata = []byte("{}")
	}
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO user_sessions (id, user_id, family_id, refresh_token_hash, expires_at, metadata)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+userSessionsSelectColumns,
		sess.ID, sess.UserID, sess.FamilyID, sess.RefreshTokenHash, sess.ExpiresAt, sess.Metadata,
	)
	return scanUserSession(row, sess)
}

func (s *PGUserSessionsStore) GetByHash(ctx context.Context, hash string) (*store.UserSession, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+userSessionsSelectColumns+` FROM user_sessions WHERE refresh_token_hash = $1`,
		hash)
	var sess store.UserSession
	if err := scanUserSession(row, &sess); err != nil {
		return nil, err
	}
	return &sess, nil
}

func (s *PGUserSessionsStore) Revoke(ctx context.Context, id uuid.UUID) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE user_sessions SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("user_sessions revoke: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		// either id missing or already revoked — surface NotFound for missing,
		// nil for already-revoked (idempotent).
		var exists bool
		if err := s.db.QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM user_sessions WHERE id = $1)`, id).Scan(&exists); err != nil {
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
func (s *PGUserSessionsStore) RevokeAllActiveByUser(ctx context.Context, exec base.Executor, userID uuid.UUID) error {
	_, err := exec.ExecContext(ctx,
		`UPDATE user_sessions SET revoked_at = now()
		   WHERE user_id = $1 AND revoked_at IS NULL`, userID)
	if err != nil {
		return fmt.Errorf("user_sessions revoke all active: %w", err)
	}
	return nil
}

// DB exposes the underlying *sql.DB as a base.Executor so cross-store helpers
// can wrap their writes and this store's writes inside one transaction.
func (s *PGUserSessionsStore) DB() base.Executor { return s.db }

func (s *PGUserSessionsStore) RevokeFamily(ctx context.Context, familyID uuid.UUID) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE user_sessions SET revoked_at = now()
		   WHERE family_id = $1 AND revoked_at IS NULL`, familyID)
	if err != nil {
		return fmt.Errorf("user_sessions revoke family: %w", err)
	}
	return nil
}

func (s *PGUserSessionsStore) ListActiveByUser(ctx context.Context, userID uuid.UUID) ([]store.UserSession, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+userSessionsSelectColumns+`
		   FROM user_sessions
		  WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > now()
		  ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("user_sessions list active: %w", err)
	}
	defer rows.Close()
	var out []store.UserSession
	for rows.Next() {
		var sess store.UserSession
		if err := scanUserSession(rows, &sess); err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

func scanUserSession(r rowScanner, sess *store.UserSession) error {
	err := r.Scan(
		&sess.ID, &sess.UserID, &sess.FamilyID, &sess.RefreshTokenHash,
		&sess.ExpiresAt, &sess.RevokedAt, &sess.Metadata, &sess.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("scan user_session: %w", err)
	}
	return nil
}
