package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// Refresh-token sentinel errors. The .Error() string is the i18n key so the
// HTTP layer can translate it. Use errors.Is for branching.
var (
	ErrRefreshInvalid = errors.New("error.refresh_token_invalid")
	ErrRefreshExpired = errors.New("error.refresh_token_expired")
	ErrRefreshRevoked = errors.New("error.refresh_token_revoked")
)

// IssueRefresh generates a new opaque refresh token, persists its sha256 hash
// in the user_sessions table, and returns the raw token and the created session.
//
// Token format: 32 random bytes, hex-encoded (64 ASCII chars).
// Hash format:  sha256(rawToken), hex-encoded (64 ASCII chars).
// The raw token is NEVER stored — only the hash reaches the database.
func IssueRefresh(
	ctx context.Context,
	sessStore store.UserSessionsStore,
	userID, familyID uuid.UUID,
	ttl time.Duration,
) (rawToken string, sess *store.UserSession, err error) {
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("auth: generate refresh token: %w", err)
	}
	rawToken = hex.EncodeToString(raw)
	hash := hashRefreshToken(rawToken)

	id, err := uuid.NewV7()
	if err != nil {
		return "", nil, fmt.Errorf("auth: generate session ID: %w", err)
	}
	sess = &store.UserSession{
		ID:               id,
		UserID:           userID,
		FamilyID:         familyID,
		RefreshTokenHash: hash,
		ExpiresAt:        time.Now().Add(ttl),
	}
	if err = sessStore.Create(ctx, sess); err != nil {
		return "", nil, fmt.Errorf("auth: create session: %w", err)
	}
	return rawToken, sess, nil
}

// VerifyRefresh looks up rawToken by its sha256 hash and validates it.
//
// Theft detection (RFC 6749 §10.4): if the token is already revoked but has
// not yet expired, a reuse-of-revoked signal is detected. The entire token
// family is immediately revoked and a security warning is emitted.
//
// Error mapping:
//   - token not in DB           → i18n.MsgRefreshTokenInvalid
//   - revoked + not-yet-expired → i18n.MsgRefreshTokenRevoked  (+ family revocation)
//   - expired                   → i18n.MsgRefreshTokenExpired
//   - revoked (expired)         → i18n.MsgRefreshTokenRevoked
func VerifyRefresh(
	ctx context.Context,
	sessStore store.UserSessionsStore,
	rawToken string,
) (*store.UserSession, error) {
	hash := hashRefreshToken(rawToken)
	sess, err := sessStore.GetByHash(ctx, hash)
	if err != nil {
		return nil, ErrRefreshInvalid
	}

	now := time.Now()

	// Theft detection: revoked but not-yet-expired means an old token from the
	// family was re-used after rotation — likely stolen and replayed.
	if sess.RevokedAt != nil && sess.ExpiresAt.After(now) {
		// Revoke the entire family to invalidate all descendants.
		if rErr := sessStore.RevokeFamily(ctx, sess.FamilyID); rErr != nil {
			slog.Warn("security.auth.refresh_theft_revoke_family_failed",
				"family_id", sess.FamilyID,
				"user_id", sess.UserID,
				"err", rErr,
			)
		}
		slog.Warn("security.auth.refresh_theft_detected",
			"family_id", sess.FamilyID,
			"user_id", sess.UserID,
		)
		return nil, ErrRefreshRevoked
	}

	// Expired check (catches both active-expired and revoked-expired).
	if !sess.ExpiresAt.After(now) {
		return nil, ErrRefreshExpired
	}

	// Revoked (but not caught by theft path above — e.g. explicit logout).
	if sess.RevokedAt != nil {
		return nil, ErrRefreshRevoked
	}

	return sess, nil
}

// RotateRefresh atomically retires the old refresh token and issues a new one
// in the same family. The new session inherits oldSess.FamilyID.
//
// Race note: the Revoke call uses WHERE id=$1 AND revoked_at IS NULL, which
// acts as an optimistic row-level guard. A concurrent rotation of the same
// token will cause the second Revoke to be a no-op (already revoked), and
// the IssueRefresh for the second caller will succeed with a distinct new row.
// This tiny race window is acceptable; theft detection will fire if the same
// *old* raw token is re-presented after rotation.
func RotateRefresh(
	ctx context.Context,
	sessStore store.UserSessionsStore,
	oldRawToken string,
	ttl time.Duration,
) (newRawToken string, newSess *store.UserSession, err error) {
	oldSess, err := VerifyRefresh(ctx, sessStore, oldRawToken)
	if err != nil {
		return "", nil, err
	}

	if err = sessStore.Revoke(ctx, oldSess.ID); err != nil {
		return "", nil, fmt.Errorf("auth: revoke old session: %w", err)
	}

	newRawToken, newSess, err = IssueRefresh(ctx, sessStore, oldSess.UserID, oldSess.FamilyID, ttl)
	if err != nil {
		return "", nil, err
	}
	return newRawToken, newSess, nil
}

// RevokeAllForUser revokes all active sessions for a user via a single bulk
// UPDATE. Used by POST /v1/auth/logout and the reset-password CLI command.
// Returns a non-nil error if the bulk update fails — callers MUST treat that
// as a hard failure (do not return success to the client). For the password
// change path use UsersStore.ChangePasswordAndRevokeSessions which composes
// the same primitive inside a cross-store transaction.
func RevokeAllForUser(
	ctx context.Context,
	sessStore store.UserSessionsStore,
	userID uuid.UUID,
) error {
	if err := sessStore.RevokeAllActiveByUser(ctx, sessStore.DB(), userID); err != nil {
		return fmt.Errorf("auth: revoke all sessions: %w", err)
	}
	return nil
}

// hashRefreshToken returns the hex-encoded sha256 of rawToken.
func hashRefreshToken(rawToken string) string {
	sum := sha256.Sum256([]byte(rawToken))
	return hex.EncodeToString(sum[:])
}
