//go:build integration

// Tests that pairing, bind, and merge are orthogonal operations — none
// cross-mutates the other's table. These tests lock in the separation
// invariant: pairing never touches channel_contacts.merged_id; merge never
// touches paired_devices; BindUser is a separate optional step.
package integration

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/store/pg"
)

// ─── Fixtures ────────────────────────────────────────────────────────────────

// seedMergeUser inserts a minimal users row for merge tests.
// Returns userID; cleans up on t.Cleanup.
func seedMergeUser(t *testing.T, db *sql.DB, suffix string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := db.Exec(
		`INSERT INTO users (id, email, password_hash, display_name, role, kind, user_key)
		 VALUES ($1, $2, 'x', 'test', 'member', 'human', $3)`,
		id, "merge-"+suffix+"@local", "merge-"+suffix,
	)
	if err != nil {
		t.Fatalf("seedMergeUser(%s): %v", suffix, err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM users WHERE id = $1", id) })
	return id
}

// seedContact inserts a bare channel_contacts row (merged_id NULL).
// Returns contactID; cleans up on t.Cleanup.
func seedContact(t *testing.T, db *sql.DB, senderID, channelType string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := db.Exec(
		`INSERT INTO channel_contacts (id, channel_type, sender_id, contact_type)
		 VALUES ($1, $2, $3, 'user')`,
		id, channelType, senderID,
	)
	if err != nil {
		t.Fatalf("seedContact(%s/%s): %v", channelType, senderID, err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM channel_contacts WHERE id = $1", id) })
	return id
}

// seedPairedDevice inserts a paired_devices row (user_id NULL).
// Returns nothing; cleans up on t.Cleanup.
func seedPairedDevice(t *testing.T, db *sql.DB, senderID, channel, chatID string) {
	t.Helper()
	id := uuid.New()
	_, err := db.Exec(
		`INSERT INTO paired_devices (id, sender_id, channel, chat_id, paired_by)
		 VALUES ($1, $2, $3, $4, 'admin')`,
		id, senderID, channel, chatID,
	)
	if err != nil {
		t.Fatalf("seedPairedDevice(%s/%s): %v", channel, senderID, err)
	}
	t.Cleanup(func() {
		db.Exec("DELETE FROM paired_devices WHERE sender_id = $1 AND channel = $2", senderID, channel)
	})
}

// ─── Test 1: Pairing approval never writes to channel_contacts ───────────────

// TestPairingDoesNotMutateMergedID asserts that ApprovePairing only inserts
// into paired_devices and leaves channel_contacts.merged_id NULL.
// Separation invariant: pairing is channel-scope gating; merged_id is
// admin-triggered identity unification — distinct operations.
func TestPairingDoesNotMutateMergedID(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	suf := uuid.New().String()[:8]
	senderID := "tg-" + suf
	contactID := seedContact(t, db, senderID, "telegram")

	pairing := pg.NewPGPairingStore(db)

	// Simulate: insert a pairing request then approve it.
	// RequestPairing requires account_id; use a dummy value.
	code, err := pairing.RequestPairing(ctx, senderID, "telegram", "chat-"+suf, "acc-"+suf, nil)
	if err != nil {
		t.Fatalf("RequestPairing: %v", err)
	}
	if _, err := pairing.ApprovePairing(ctx, code, "admin"); err != nil {
		t.Fatalf("ApprovePairing: %v", err)
	}

	// Assert: channel_contacts.merged_id must still be NULL.
	var mergedID *string
	if err := db.QueryRowContext(ctx,
		`SELECT merged_id::text FROM channel_contacts WHERE id = $1`,
		contactID,
	).Scan(&mergedID); err != nil {
		t.Fatalf("select merged_id: %v", err)
	}
	if mergedID != nil {
		t.Errorf("pairing wrote merged_id=%v; must remain NULL — pairing must not touch channel_contacts", *mergedID)
	}
}

// ─── Test 2: Merge never writes to paired_devices ────────────────────────────

// TestMergeDoesNotMutatePairedDevices asserts that MergeUserAggregate only
// updates channel_contacts (+ripple tables) and leaves paired_devices intact.
func TestMergeDoesNotMutatePairedDevices(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	suf := uuid.New().String()[:8]
	senderID := "tg-m-" + suf
	userIDStr := suf

	targetUser := seedMergeUser(t, db, "target-"+suf)
	contactID := seedContact(t, db, senderID, "telegram")
	seedPairedDevice(t, db, senderID, "telegram", "chat-m-"+suf)

	// Snapshot user_id of the paired_devices row before merge.
	var beforeUserID *string
	db.QueryRowContext(ctx,
		`SELECT user_id::text FROM paired_devices WHERE sender_id = $1 AND channel = 'telegram'`,
		senderID,
	).Scan(&beforeUserID)

	contacts := pg.NewPGContactStore(db)

	req := store.MergeUserAggregateRequest{
		ContactIDs:    []uuid.UUID{contactID},
		SourceUserIDs: []uuid.UUID{},
		TargetUserID:  targetUser,
		MergeAudit:    []byte(`{"merged_by":"admin-test"}`),
	}
	if err := contacts.MergeUserAggregate(ctx, req); err != nil {
		t.Fatalf("MergeUserAggregate: %v", err)
	}

	// Assert: channel_contacts.merged_id must be set to targetUser.
	var mergedID string
	if err := db.QueryRowContext(ctx,
		`SELECT merged_id::text FROM channel_contacts WHERE id = $1`,
		contactID,
	).Scan(&mergedID); err != nil {
		t.Fatalf("select merged_id after merge: %v", err)
	}
	if mergedID != targetUser.String() {
		t.Errorf("merged_id: got %v want %v", mergedID, targetUser)
	}

	// Assert: paired_devices.user_id must be unchanged (merge must not touch it).
	var afterUserID *string
	db.QueryRowContext(ctx,
		`SELECT user_id::text FROM paired_devices WHERE sender_id = $1 AND channel = 'telegram'`,
		senderID,
	).Scan(&afterUserID)

	_ = userIDStr // used above to document intent

	// Both before and after must be identical (both NULL pre-BindUser).
	beforeStr := "<nil>"
	afterStr := "<nil>"
	if beforeUserID != nil {
		beforeStr = *beforeUserID
	}
	if afterUserID != nil {
		afterStr = *afterUserID
	}
	if beforeStr != afterStr {
		t.Errorf("paired_devices.user_id changed after merge: %v → %v; merge must not touch paired_devices", beforeStr, afterStr)
	}
}

// ─── Test 3: BindUser is a separate optional step from Pairing ───────────────

// TestBindUserOptionalSeparateFromPairing asserts the transition:
// paired_devices.user_id is NULL after ApprovePairing, then set after BindUser.
// This documents that BindUser is an explicit, optional second step.
func TestBindUserOptionalSeparateFromPairing(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	suf := uuid.New().String()[:8]
	senderID := "tg-b-" + suf
	userID := seedMergeUser(t, db, "bind-"+suf)

	pairing := pg.NewPGPairingStore(db)

	// Step 1: Request + approve pairing. user_id must be NULL (no BindUser yet).
	code, err := pairing.RequestPairing(ctx, senderID, "telegram", "chat-b-"+suf, "acc-b-"+suf, nil)
	if err != nil {
		t.Fatalf("RequestPairing: %v", err)
	}
	if _, err := pairing.ApprovePairing(ctx, code, "admin"); err != nil {
		t.Fatalf("ApprovePairing: %v", err)
	}
	t.Cleanup(func() {
		db.Exec("DELETE FROM paired_devices WHERE sender_id = $1 AND channel = 'telegram'", senderID)
	})

	var uidAfterApprove *string
	db.QueryRowContext(ctx,
		`SELECT user_id::text FROM paired_devices WHERE sender_id = $1 AND channel = 'telegram'`,
		senderID,
	).Scan(&uidAfterApprove)
	if uidAfterApprove != nil {
		t.Errorf("paired_devices.user_id should be NULL after ApprovePairing (pre-BindUser), got %v", *uidAfterApprove)
	}

	// Step 2: BindUser. user_id must now be populated.
	if err := pairing.BindUser(ctx, senderID, "telegram", userID); err != nil {
		t.Fatalf("BindUser: %v", err)
	}

	var uidAfterBind *string
	db.QueryRowContext(ctx,
		`SELECT user_id::text FROM paired_devices WHERE sender_id = $1 AND channel = 'telegram'`,
		senderID,
	).Scan(&uidAfterBind)
	if uidAfterBind == nil {
		t.Fatal("paired_devices.user_id must be set after BindUser, got NULL")
	}
	if *uidAfterBind != userID.String() {
		t.Errorf("paired_devices.user_id: got %v want %v", *uidAfterBind, userID)
	}
}

// ─── Test 4: Merge guard rejects already-merged source ───────────────────────

// TestMergeRejectsAlreadyMergedSource asserts ErrMergeSourceAlreadyMerged when
// the source contact already has merged_id set. Chained merges are forbidden.
func TestMergeRejectsAlreadyMergedSource(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	suf := uuid.New().String()[:8]

	firstTarget := seedMergeUser(t, db, "chain-target1-"+suf)
	secondTarget := seedMergeUser(t, db, "chain-target2-"+suf)
	contactID := seedContact(t, db, "tg-chain-"+suf, "telegram")

	contacts := pg.NewPGContactStore(db)

	// First merge: succeeds.
	req1 := store.MergeUserAggregateRequest{
		ContactIDs:    []uuid.UUID{contactID},
		SourceUserIDs: []uuid.UUID{},
		TargetUserID:  firstTarget,
	}
	if err := contacts.MergeUserAggregate(ctx, req1); err != nil {
		t.Fatalf("first MergeUserAggregate: %v", err)
	}

	// Second merge using same contact (already merged): must reject.
	req2 := store.MergeUserAggregateRequest{
		ContactIDs:    []uuid.UUID{contactID},
		SourceUserIDs: []uuid.UUID{firstTarget},
		TargetUserID:  secondTarget,
	}
	err := contacts.MergeUserAggregate(ctx, req2)
	if !isErrMergeSourceAlreadyMerged(err) {
		t.Errorf("expected ErrMergeSourceAlreadyMerged for chained merge, got: %v", err)
	}
}

// ─── Test 5: Bind + concurrent merge divergence is allowed by design ─────────

// TestBindUserAndConcurrentMergeDivergence documents the by-design divergence:
// after an admin binds a device to userA AND merges the contact into userB,
// paired_devices.user_id == userA (device-scope) AND
// channel_contacts.merged_id == userB (identity-scope).
// Divergence is intentional — paired device is per-device, merge is
// per-contact-identity. Admins must be aware: the two scopes are independent.
func TestBindUserAndConcurrentMergeDivergence(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	suf := uuid.New().String()[:8]
	senderID := "tg-div-" + suf

	userA := seedMergeUser(t, db, "div-a-"+suf)
	userB := seedMergeUser(t, db, "div-b-"+suf)
	contactID := seedContact(t, db, senderID, "telegram")
	seedPairedDevice(t, db, senderID, "telegram", "chat-div-"+suf)

	pairing := pg.NewPGPairingStore(db)
	contacts := pg.NewPGContactStore(db)

	// Admin action 1: bind the device to userA.
	if err := pairing.BindUser(ctx, senderID, "telegram", userA); err != nil {
		t.Fatalf("BindUser to userA: %v", err)
	}

	// Admin action 2: merge the contact into userB (distinct user).
	req := store.MergeUserAggregateRequest{
		ContactIDs:    []uuid.UUID{contactID},
		SourceUserIDs: []uuid.UUID{},
		TargetUserID:  userB,
	}
	if err := contacts.MergeUserAggregate(ctx, req); err != nil {
		t.Fatalf("MergeUserAggregate into userB: %v", err)
	}

	// Assert device still bound to userA (merge did not change it).
	var deviceUserID string
	if err := db.QueryRowContext(ctx,
		`SELECT user_id::text FROM paired_devices WHERE sender_id = $1 AND channel = 'telegram'`,
		senderID,
	).Scan(&deviceUserID); err != nil {
		t.Fatalf("select paired_devices.user_id: %v", err)
	}
	if deviceUserID != userA.String() {
		t.Errorf("paired_devices.user_id: got %v want %v (userA); merge must not overwrite device binding",
			deviceUserID, userA)
	}

	// Assert contact is merged into userB (pairing did not change it).
	var mergedID string
	if err := db.QueryRowContext(ctx,
		`SELECT merged_id::text FROM channel_contacts WHERE id = $1`,
		contactID,
	).Scan(&mergedID); err != nil {
		t.Fatalf("select channel_contacts.merged_id: %v", err)
	}
	if mergedID != userB.String() {
		t.Errorf("channel_contacts.merged_id: got %v want %v (userB); pairing must not overwrite merge",
			mergedID, userB)
	}

	// Document the divergence explicitly.
	if deviceUserID == mergedID {
		t.Logf("(note: userA == userB in this run — divergence test is trivially satisfied; use distinct users)")
	} else {
		t.Logf("OK: device bound to %v (userA), contact merged into %v (userB) — divergence by design", deviceUserID, mergedID)
	}
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// isErrMergeSourceAlreadyMerged checks whether the error is (or wraps)
// store.ErrMergeSourceAlreadyMerged.
func isErrMergeSourceAlreadyMerged(err error) bool {
	if err == nil {
		return false
	}
	// errors.Is works for wrapped errors too.
	if err == store.ErrMergeSourceAlreadyMerged {
		return true
	}
	// Unwrap chain via errors.As / manual check.
	type unwrapper interface{ Unwrap() error }
	for err != nil {
		if err == store.ErrMergeSourceAlreadyMerged {
			return true
		}
		u, ok := err.(unwrapper)
		if !ok {
			break
		}
		err = u.Unwrap()
	}
	return false
}
