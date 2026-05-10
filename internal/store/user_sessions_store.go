package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store/base"
)

// UserSession mirrors a row of `user_sessions` — the refresh-token registry.
// One row per active refresh token.
//
//   - FamilyID groups rotating refresh tokens so the auth layer can revoke an
//     entire family when reuse-of-revoked is detected (theft signal).
//   - RefreshTokenHash is sha256(opaque_token) — UNIQUE in DB.
//   - RevokedAt nil means active; non-nil means explicitly revoked.
//
// The store does not interpret expires_at — caller filters expired sessions
// in ListActiveByUser via the `expires_at > NOW()` clause.
type UserSession struct {
	ID               uuid.UUID       `db:"id"`
	UserID           uuid.UUID       `db:"user_id"`
	FamilyID         uuid.UUID       `db:"family_id"`
	RefreshTokenHash string          `db:"refresh_token_hash"`
	ExpiresAt        time.Time       `db:"expires_at"`
	RevokedAt        *time.Time      `db:"revoked_at"`
	Metadata         json.RawMessage `db:"metadata"`
	CreatedAt        time.Time       `db:"created_at"`
}

// UserSessionsStore manages refresh-token sessions.
type UserSessionsStore interface {
	Create(ctx context.Context, s *UserSession) error
	GetByHash(ctx context.Context, hash string) (*UserSession, error)
	Revoke(ctx context.Context, id uuid.UUID) error
	RevokeFamily(ctx context.Context, familyID uuid.UUID) error
	ListActiveByUser(ctx context.Context, userID uuid.UUID) ([]UserSession, error)
	// RevokeAllActiveByUser bulk-revokes every active session for userID via a
	// single UPDATE. Caller controls the executor: pass the store's own DB for
	// stand-alone use, or an active *sql.Tx when composing into a cross-store
	// transaction (see UsersStore.ChangePasswordAndRevokeSessions).
	RevokeAllActiveByUser(ctx context.Context, exec base.Executor, userID uuid.UUID) error
	// DB exposes the underlying executor so cross-store helpers can pair this
	// store's writes with another store's writes inside one transaction without
	// each store having to re-implement the bulk SQL.
	DB() base.Executor
}
