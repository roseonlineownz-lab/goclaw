package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store/base"
)

// ErrPasswordResetNotFound is returned by PasswordResetStore methods when a
// token is missing, already used, or expired. The single sentinel value avoids
// leaking which condition triggered (no enumeration / no timing oracle).
var ErrPasswordResetNotFound = errors.New("password reset token not found, expired, or used")

// PasswordResetStore manages single-use, time-bounded password reset tokens.
//
// Storage rule: only the SHA-256 hex hash of the raw token is persisted.
// The raw value is mailed/displayed exactly once at issue time.
//
// Atomicity rule: MarkUsed performs the compare-and-set in a single SQL
// statement (UPDATE ... WHERE used_at IS NULL AND expires_at > NOW()
// RETURNING user_id). There is no SELECT-then-UPDATE window — concurrent
// clients racing the same token cannot both succeed.
type PasswordResetStore interface {
	// Insert creates a new reset row and returns its UUID. tokenHash MUST be
	// the SHA-256 hex digest of the raw token (length 64). The unique
	// constraint on token_hash will surface duplicate inserts as an error.
	Insert(ctx context.Context, userID uuid.UUID, tokenHash string, expiresAt time.Time) (uuid.UUID, error)

	// GetActive returns userID + expiresAt for an unused, unexpired token.
	// Returns ErrPasswordResetNotFound if the token is missing, used, or expired.
	GetActive(ctx context.Context, tokenHash string) (uuid.UUID, time.Time, error)

	// MarkUsed atomically transitions used_at from NULL to NOW() and returns
	// the row's user_id. The update is gated by `used_at IS NULL AND
	// expires_at > NOW()` so callers cannot reuse expired or already-consumed
	// tokens. Pass exec to compose with a cross-store transaction (Phase 03
	// chains MarkUsed → users.UPDATE password_hash → sessions.RevokeAllActiveByUser
	// inside one TX).
	MarkUsed(ctx context.Context, exec base.Executor, tokenHash string) (uuid.UUID, error)

	// DeleteExpired removes rows where expires_at < `before`. Returns the
	// number of rows removed. Intended to be called by a periodic cron job;
	// safe to invoke concurrently (idempotent).
	DeleteExpired(ctx context.Context, before time.Time) (int64, error)
}
