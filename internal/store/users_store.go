package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// User mirrors a row of the `users` table.
//
// Identity: ID is a UUID v7 generated DB-side (DEFAULT uuid_generate_v7()) on
// PG or by the Go layer (uuid.NewV7()) on SQLite — SQLite schema has no
// DEFAULT. The partial unique index `users_only_one_root` enforces at most one
// root user; bootstrap is responsible for honoring that invariant. The store
// layer treats `role` as opaque text.
//
// UserKey is the stable workspace folder identifier derived from the email
// local-part at creation time. It is immutable: Update() strips any attempt to
// change it; use SetKind to transition kind+channel_type atomically.
type User struct {
	ID           uuid.UUID       `db:"id"`
	Email        string          `db:"email"`
	DisplayName  *string         `db:"display_name"`
	PasswordHash string          `db:"password_hash"`
	Role         string          `db:"role"`   // root|admin|member|viewer
	Status       string          `db:"status"` // active|suspended|...
	DeletedAt    *time.Time      `db:"deleted_at"`
	Metadata     json.RawMessage `db:"metadata"`
	// UserKey is the stable slug used for workspace folder naming.
	// Auto-generated from email on Create if empty; never mutated after.
	UserKey     string  `db:"user_key"`
	// Kind is 'human' (default) or 'channel' (merged channel identity).
	Kind        string  `db:"kind"`
	// ChannelType is the channel platform (e.g. 'telegram') when Kind='channel';
	// always nil when Kind='human'. Enforced by DB shape constraint.
	ChannelType *string `db:"channel_type"`
	CreatedAt   time.Time       `db:"created_at"`
	UpdatedAt   time.Time       `db:"updated_at"`
}

// UsersStore provides CRUD over the `users` table.
//
// PasswordHash is opaque text at this layer; the auth layer is responsible for
// enforcing the Argon2id format on write.
//
// SetKind atomically updates the (kind, channel_type) pair. It is the only
// sanctioned path for transitioning a user identity from 'human' to 'channel';
// the DB shape constraint enforces coherence. Update() ignores kind/channel_type
// keys to prevent accidental mutation via the generic map path.
type UsersStore interface {
	Create(ctx context.Context, u *User) error
	Get(ctx context.Context, id uuid.UUID) (*User, error)
	GetByEmail(ctx context.Context, email string) (*User, error)
	List(ctx context.Context, limit, offset int) ([]User, error)
	Update(ctx context.Context, id uuid.UUID, fields map[string]any) error
	Delete(ctx context.Context, id uuid.UUID) error
	// SetKind atomically sets (kind, channel_type) in a single transaction.
	// The DB shape constraint guarantees the pair is always coherent:
	// kind='human' requires channelType=nil; kind='channel' requires non-nil.
	SetKind(ctx context.Context, id uuid.UUID, kind string, channelType *string) error
	// ChangePasswordAndRevokeSessions atomically updates password_hash and
	// revokes every active refresh session for the user inside a single
	// transaction. On any error the password is NOT updated AND sessions stay
	// active. Implementations require a UserSessionsStore reference wired via
	// UseSessions before this method is callable.
	ChangePasswordAndRevokeSessions(ctx context.Context, userID uuid.UUID, newHash string) error
	// ConfirmPasswordReset chains the Phase 02 atomic MarkUsed primitive with
	// the Phase 01 password+revoke primitive inside a single transaction:
	// MarkUsed RETURNING user_id → UPDATE users.password_hash → bulk
	// RevokeAllActiveByUser. Returns ErrPasswordResetNotFound if codeHash is
	// missing/expired/used; on any error the password and sessions are
	// untouched. Implementations require both UserSessionsStore (UseSessions)
	// and PasswordResetStore (UseResetTokens) wired before this is callable.
	ConfirmPasswordReset(ctx context.Context, codeHash, newHash string) error
}
