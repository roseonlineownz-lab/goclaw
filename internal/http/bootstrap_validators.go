package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"strings"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/auth"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// errAlreadyBootstrapped signals the race-loser path: another caller created
// the root user inside the advisory-locked TX between the IsBootstrapRequired
// check and the count query.
var errAlreadyBootstrapped = errors.New("bootstrap already done")

// validateEmail rejects empty / unparseable / display-name-bearing addresses.
func validateEmail(email string) error {
	if email == "" {
		return errors.New("empty")
	}
	addr, err := mail.ParseAddress(email)
	if err != nil {
		return err
	}
	// Reject display-name forms ("Foo <foo@example.com>") — keep it strict.
	if addr.Name != "" || addr.Address != email {
		return errors.New("display-name not allowed")
	}
	if !strings.Contains(addr.Address, "@") {
		return errors.New("missing @")
	}
	return nil
}

// pgAdvisoryBootstrapLock is the namespaced lock id for bootstrap.
// Value is arbitrary but stable; release happens on TX commit/rollback.
const pgAdvisoryBootstrapLock = 0xB007

// createRootUser performs the atomic bootstrap insert under an advisory lock.
// On PG: SERIALIZABLE-equivalent via pg_advisory_xact_lock.
// On SQLite: skip the lock; SQLite is single-writer.
//
// Concurrent callers either: (a) lose the lock race and see count > 0 (409),
// or (b) collide on the partial UNIQUE index `users_only_one_root` (DB-level
// fallback — surfaced as 409).
func (h *BootstrapHandler) createRootUser(r *http.Request, body bootstrapInitBody) (uuid.UUID, error) {
	ctx := r.Context()
	if h.db == nil {
		return h.createRootUserNoLock(ctx, body)
	}

	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return uuid.Nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if h.isPG {
		if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", pgAdvisoryBootstrapLock); err != nil {
			return uuid.Nil, err
		}
	}

	// Race guard: re-count inside lock.
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE deleted_at IS NULL").Scan(&count); err != nil {
		return uuid.Nil, err
	}
	if count > 0 {
		return uuid.Nil, errAlreadyBootstrapped
	}

	uid, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, err
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		return uuid.Nil, err
	}

	display := strings.TrimSpace(body.DisplayName)
	var displayPtr *string
	if display != "" {
		displayPtr = &display
	}

	// Insert via raw SQL to stay inside the TX.
	q := `INSERT INTO users (id, email, display_name, password_hash, role, status)
	      VALUES ($1, $2, $3, $4, $5, 'active')`
	if _, err := tx.ExecContext(ctx, q, uid, body.Email, displayPtr, hash, string(permissions.RoleRoot)); err != nil {
		return uuid.Nil, err
	}

	if err := tx.Commit(); err != nil {
		return uuid.Nil, err
	}
	return uid, nil
}

// createRootUserNoLock is the no-DB-handle fallback (e.g., test harness with only
// a fake UsersStore). Best-effort idempotency via store.GetByEmail probe.
func (h *BootstrapHandler) createRootUserNoLock(ctx context.Context, body bootstrapInitBody) (uuid.UUID, error) {
	if existing, _ := h.users.GetByEmail(ctx, body.Email); existing != nil {
		return uuid.Nil, errAlreadyBootstrapped
	}
	uid, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, err
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		return uuid.Nil, err
	}
	display := strings.TrimSpace(body.DisplayName)
	var displayPtr *string
	if display != "" {
		displayPtr = &display
	}
	u := &store.User{
		ID:           uid,
		Email:        body.Email,
		DisplayName:  displayPtr,
		PasswordHash: hash,
		Role:         string(permissions.RoleRoot),
		Status:       "active",
		Metadata:     json.RawMessage(`{}`),
	}
	if err := h.users.Create(ctx, u); err != nil {
		return uuid.Nil, err
	}
	return uid, nil
}
