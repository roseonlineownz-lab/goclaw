package auth

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/i18n"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/store/base"
)

// fakeSessionStore is an in-memory implementation of store.UserSessionsStore
// for unit tests. All methods are goroutine-safe.
type fakeSessionStore struct {
	mu                sync.Mutex
	rows              map[uuid.UUID]*store.UserSession // keyed by session ID
	revokeFamilyCalls []uuid.UUID
}

func newFakeSessionStore() *fakeSessionStore {
	return &fakeSessionStore{rows: make(map[uuid.UUID]*store.UserSession)}
}

func (f *fakeSessionStore) Create(_ context.Context, s *store.UserSession) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *s
	f.rows[s.ID] = &cp
	return nil
}

func (f *fakeSessionStore) GetByHash(_ context.Context, hash string) (*store.UserSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.rows {
		if s.RefreshTokenHash == hash {
			cp := *s
			return &cp, nil
		}
	}
	return nil, errors.New("not found")
}

func (f *fakeSessionStore) Revoke(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.rows[id]
	if !ok {
		return errors.New("session not found")
	}
	if s.RevokedAt != nil {
		return nil // already revoked — idempotent
	}
	now := time.Now()
	s.RevokedAt = &now
	return nil
}

func (f *fakeSessionStore) RevokeFamily(_ context.Context, familyID uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revokeFamilyCalls = append(f.revokeFamilyCalls, familyID)
	now := time.Now()
	for _, s := range f.rows {
		if s.FamilyID == familyID && s.RevokedAt == nil {
			s.RevokedAt = &now
		}
	}
	return nil
}

func (f *fakeSessionStore) ListActiveByUser(_ context.Context, userID uuid.UUID) ([]store.UserSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.UserSession
	now := time.Now()
	for _, s := range f.rows {
		if s.UserID == userID && s.RevokedAt == nil && s.ExpiresAt.After(now) {
			out = append(out, *s)
		}
	}
	return out, nil
}

// fakeExecutor satisfies base.Executor for in-memory tests — never invoked
// because RevokeAllActiveByUser short-circuits on the fake's own state.
type fakeExecutor struct{}

func (fakeExecutor) ExecContext(_ context.Context, _ string, _ ...any) (sql.Result, error) {
	return nil, nil
}

func (fakeExecutor) QueryRowContext(_ context.Context, _ string, _ ...any) *sql.Row {
	return nil
}

func (f *fakeSessionStore) RevokeAllActiveByUser(_ context.Context, _ base.Executor, userID uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	for _, s := range f.rows {
		if s.UserID == userID && s.RevokedAt == nil {
			s.RevokedAt = &now
		}
	}
	return nil
}

func (f *fakeSessionStore) DB() base.Executor { return fakeExecutor{} }

// revokeFamilyCalledWith returns true if RevokeFamily was called with familyID.
func (f *fakeSessionStore) revokeFamilyCalledWith(familyID uuid.UUID) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.revokeFamilyCalls, familyID)
}

// --- tests ---

func TestIssueAndVerify_HappyPath(t *testing.T) {
	ctx := context.Background()
	fs := newFakeSessionStore()
	userID := uuid.Must(uuid.NewV7())
	familyID := uuid.Must(uuid.NewV7())

	rawToken, sess, err := IssueRefresh(ctx, fs, userID, familyID, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("IssueRefresh: %v", err)
	}
	if rawToken == "" {
		t.Fatal("expected non-empty raw token")
	}
	if sess.UserID != userID {
		t.Errorf("session userID mismatch")
	}

	got, err := VerifyRefresh(ctx, fs, rawToken)
	if err != nil {
		t.Fatalf("VerifyRefresh: %v", err)
	}
	if got.ID != sess.ID {
		t.Errorf("session ID mismatch: got %v want %v", got.ID, sess.ID)
	}
}

func TestVerify_UnknownToken_ReturnsInvalid(t *testing.T) {
	ctx := context.Background()
	fs := newFakeSessionStore()

	_, err := VerifyRefresh(ctx, fs, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() != i18n.MsgRefreshTokenInvalid {
		t.Errorf("want %q, got %q", i18n.MsgRefreshTokenInvalid, err.Error())
	}
}

func TestVerify_ExpiredToken_ReturnsExpired(t *testing.T) {
	ctx := context.Background()
	fs := newFakeSessionStore()
	userID := uuid.Must(uuid.NewV7())
	familyID := uuid.Must(uuid.NewV7())

	rawToken, _, err := IssueRefresh(ctx, fs, userID, familyID, -time.Second)
	if err != nil {
		t.Fatalf("IssueRefresh: %v", err)
	}

	_, err = VerifyRefresh(ctx, fs, rawToken)
	if err == nil {
		t.Fatal("expected error for expired token")
	}
	if err.Error() != i18n.MsgRefreshTokenExpired {
		t.Errorf("want %q, got %q", i18n.MsgRefreshTokenExpired, err.Error())
	}
}

func TestVerify_RevokedToken_ReturnsRevoked(t *testing.T) {
	ctx := context.Background()
	fs := newFakeSessionStore()
	userID := uuid.Must(uuid.NewV7())
	familyID := uuid.Must(uuid.NewV7())

	// Issue with negative TTL so it's both revoked AND expired (no theft path).
	rawToken, sess, err := IssueRefresh(ctx, fs, userID, familyID, -time.Second)
	if err != nil {
		t.Fatalf("IssueRefresh: %v", err)
	}
	if err := fs.Revoke(ctx, sess.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	_, err = VerifyRefresh(ctx, fs, rawToken)
	if err == nil {
		t.Fatal("expected error for revoked token")
	}
	// Expired is checked before revoked for already-expired sessions.
	if err.Error() != i18n.MsgRefreshTokenExpired && err.Error() != i18n.MsgRefreshTokenRevoked {
		t.Errorf("unexpected error %q", err.Error())
	}
}

func TestRotation_OldRevokedNewIssued_SameFamily(t *testing.T) {
	ctx := context.Background()
	fs := newFakeSessionStore()
	userID := uuid.Must(uuid.NewV7())
	familyID := uuid.Must(uuid.NewV7())

	rawToken1, sess1, err := IssueRefresh(ctx, fs, userID, familyID, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("IssueRefresh: %v", err)
	}

	rawToken2, sess2, err := RotateRefresh(ctx, fs, rawToken1, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("RotateRefresh: %v", err)
	}

	// Old session must be revoked.
	fs.mu.Lock()
	oldRow := fs.rows[sess1.ID]
	fs.mu.Unlock()
	if oldRow.RevokedAt == nil {
		t.Error("old session should be revoked after rotation")
	}

	// New session must be in same family.
	if sess2.FamilyID != familyID {
		t.Errorf("family mismatch: got %v want %v", sess2.FamilyID, familyID)
	}

	// New raw token must verify.
	got, err := VerifyRefresh(ctx, fs, rawToken2)
	if err != nil {
		t.Fatalf("VerifyRefresh new token: %v", err)
	}
	if got.ID != sess2.ID {
		t.Errorf("session ID mismatch after rotation")
	}
}

func TestTheftDetection_RevokesEntireFamily(t *testing.T) {
	ctx := context.Background()
	fs := newFakeSessionStore()
	userID := uuid.Must(uuid.NewV7())
	familyID := uuid.Must(uuid.NewV7())

	// Issue T1 in family F1.
	rawToken1, _, err := IssueRefresh(ctx, fs, userID, familyID, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("IssueRefresh T1: %v", err)
	}

	// Legitimate rotation: T1 → T2 (T1 becomes revoked, T2 active, same family).
	_, _, err = RotateRefresh(ctx, fs, rawToken1, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("RotateRefresh T1→T2: %v", err)
	}

	// Attacker (or confused client) re-uses T1 — theft signal.
	_, err = VerifyRefresh(ctx, fs, rawToken1)
	if err == nil {
		t.Fatal("expected error on reuse of revoked token T1")
	}
	if err.Error() != i18n.MsgRefreshTokenRevoked {
		t.Errorf("want %q, got %q", i18n.MsgRefreshTokenRevoked, err.Error())
	}

	// RevokeFamily must have been called with the correct family ID.
	if !fs.revokeFamilyCalledWith(familyID) {
		t.Error("RevokeFamily was not called for the compromised family")
	}

	// All sessions in the family should now be revoked (T2 included).
	active, err := fs.ListActiveByUser(ctx, userID)
	if err != nil {
		t.Fatalf("ListActiveByUser: %v", err)
	}
	if len(active) != 0 {
		t.Errorf("expected 0 active sessions after family revocation, got %d", len(active))
	}
}

func TestRevokeAllForUser_RevokesAllActive(t *testing.T) {
	ctx := context.Background()
	fs := newFakeSessionStore()
	userID := uuid.Must(uuid.NewV7())

	// Issue 3 sessions with different families.
	for i := range 3 {
		fid := uuid.Must(uuid.NewV7())
		if _, _, err := IssueRefresh(ctx, fs, userID, fid, 30*24*time.Hour); err != nil {
			t.Fatalf("IssueRefresh %d: %v", i, err)
		}
	}

	active, _ := fs.ListActiveByUser(ctx, userID)
	if len(active) != 3 {
		t.Fatalf("expected 3 active sessions before revoke, got %d", len(active))
	}

	if err := RevokeAllForUser(ctx, fs, userID); err != nil {
		t.Fatalf("RevokeAllForUser: %v", err)
	}

	active, _ = fs.ListActiveByUser(ctx, userID)
	if len(active) != 0 {
		t.Errorf("expected 0 active sessions after RevokeAllForUser, got %d", len(active))
	}
}
