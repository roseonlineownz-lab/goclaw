//go:build sqlite || sqliteonly

package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// newTestContactStore opens an in-memory SQLite DB (with FK enforcement on)
// and returns a contact store ready for use.
func newTestContactStore(t *testing.T) (context.Context, *SQLiteContactStore, *sql.DB) {
	t.Helper()
	db, err := OpenDB(filepath.Join(t.TempDir(), "contacts.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := EnsureSchema(db); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	return context.Background(), NewSQLiteContactStore(db), db
}

// seedSQLiteContactUser inserts a minimal users row for FK reference in contact tests.
func seedSQLiteContactUser(t *testing.T, db *sql.DB) string {
	t.Helper()
	id := uuid.Must(uuid.NewV7()).String()
	suffix := id[:8]
	_, err := db.Exec(
		`INSERT INTO users (id, email, password_hash, role, kind, user_key)
		 VALUES (?, ?, 'x', 'member', 'human', ?)`,
		id, "cc-user-"+suffix+"@local", "cc-user-"+suffix,
	)
	if err != nil {
		t.Fatalf("seedSQLiteContactUser: %v", err)
	}
	return id
}

// seedSQLiteContactProject inserts a minimal project for FK reference.
func seedSQLiteContactProject(t *testing.T, db *sql.DB, ownerID string) string {
	t.Helper()
	id := uuid.Must(uuid.NewV7()).String()
	slug := "cc-proj-" + id[:8]
	_, err := db.Exec(
		`INSERT INTO projects (id, owner_user_id, slug, status)
		 VALUES (?, ?, ?, 'active')`,
		id, ownerID, slug,
	)
	if err != nil {
		t.Fatalf("seedSQLiteContactProject: %v", err)
	}
	return id
}

// insertSQLiteContact inserts a channel_contacts row and returns its UUID string.
func insertSQLiteContact(t *testing.T, db *sql.DB, channelType, senderID string) string {
	t.Helper()
	id := uuid.Must(uuid.NewV7()).String()
	_, err := db.Exec(
		`INSERT INTO channel_contacts (id, channel_type, sender_id, contact_type)
		 VALUES (?, ?, ?, 'user')`,
		id, channelType, senderID,
	)
	if err != nil {
		t.Fatalf("insertSQLiteContact: %v", err)
	}
	return id
}

// TestChannelContactDefaultProjectIDInsert verifies a contact can store a valid
// default_project_id FK and it is readable via GetContactByID.
func TestChannelContactDefaultProjectIDInsert(t *testing.T) {
	ctx, s, db := newTestContactStore(t)

	ownerID := seedSQLiteContactUser(t, db)
	projectID := seedSQLiteContactProject(t, db, ownerID)
	contactID := insertSQLiteContact(t, db, "telegram", "tg-dpid-"+uuid.Must(uuid.NewV7()).String()[:8])

	pid := uuid.MustParse(projectID)
	if err := s.UpdateDefaultProject(ctx, uuid.MustParse(contactID), &pid); err != nil {
		t.Fatalf("UpdateDefaultProject: %v", err)
	}

	got, err := s.GetContactByID(ctx, uuid.MustParse(contactID))
	if err != nil {
		t.Fatalf("GetContactByID: %v", err)
	}
	if got.DefaultProjectID == nil {
		t.Fatal("expected DefaultProjectID to be set, got nil")
	}
	if got.DefaultProjectID.String() != projectID {
		t.Errorf("DefaultProjectID: got %v want %v", got.DefaultProjectID, projectID)
	}
}

// TestChannelContactDefaultProjectIDFKViolation verifies that setting a
// default_project_id to a non-existent project UUID is rejected by the FK constraint
// (requires PRAGMA foreign_keys = ON, set in OpenDB).
func TestChannelContactDefaultProjectIDFKViolation(t *testing.T) {
	ctx, _, db := newTestContactStore(t)

	contactID := insertSQLiteContact(t, db, "telegram", "tg-fkv-"+uuid.Must(uuid.NewV7()).String()[:8])
	nonExistentProjectID := uuid.Must(uuid.NewV7()).String()

	_, err := db.ExecContext(ctx,
		`UPDATE channel_contacts SET default_project_id = ? WHERE id = ?`,
		nonExistentProjectID, contactID,
	)
	if err == nil {
		t.Error("FK violation: expected error when setting non-existent project UUID, got nil")
	}
}

// TestChannelContactProjectDeleteSetsNull verifies ON DELETE SET NULL: removing
// the projects row nullifies default_project_id on channel_contacts.
func TestChannelContactProjectDeleteSetsNull(t *testing.T) {
	ctx, s, db := newTestContactStore(t)

	ownerID := seedSQLiteContactUser(t, db)

	// Insert project without relying on seedSQLiteContactProject so we can delete it manually.
	projectID := uuid.Must(uuid.NewV7()).String()
	slug := "cc-del-" + projectID[:8]
	if _, err := db.Exec(
		`INSERT INTO projects (id, owner_user_id, slug, status) VALUES (?, ?, ?, 'active')`,
		projectID, ownerID, slug,
	); err != nil {
		t.Fatalf("insert project: %v", err)
	}

	contactID := insertSQLiteContact(t, db, "telegram", "tg-pds-"+uuid.Must(uuid.NewV7()).String()[:8])
	pid := uuid.MustParse(projectID)
	if err := s.UpdateDefaultProject(ctx, uuid.MustParse(contactID), &pid); err != nil {
		t.Fatalf("UpdateDefaultProject: %v", err)
	}

	// Hard-delete the project — ON DELETE SET NULL must fire.
	if _, err := db.Exec("DELETE FROM projects WHERE id = ?", projectID); err != nil {
		t.Fatalf("hard-delete project: %v", err)
	}

	// Read back — should be NULL.
	var stored *string
	if err := db.QueryRowContext(ctx,
		`SELECT default_project_id FROM channel_contacts WHERE id = ?`, contactID,
	).Scan(&stored); err != nil {
		t.Fatalf("read default_project_id after project delete: %v", err)
	}
	if stored != nil {
		t.Errorf("expected NULL default_project_id after project hard-delete, got %v", *stored)
	}
}

// TestChannelContactMergedIDUserDeleteSetsNull verifies ON DELETE SET NULL for
// merged_id: deleting a users row nullifies merged_id on channel_contacts.
func TestChannelContactMergedIDUserDeleteSetsNull(t *testing.T) {
	ctx, _, db := newTestContactStore(t)

	// Insert user without relying on seed helper so we can delete it manually.
	userID := uuid.Must(uuid.NewV7()).String()
	suffix := userID[:8]
	if _, err := db.Exec(
		`INSERT INTO users (id, email, password_hash, role, kind, user_key)
		 VALUES (?, ?, 'x', 'member', 'human', ?)`,
		userID, "cc-merge-"+suffix+"@local", "cc-merge-"+suffix,
	); err != nil {
		t.Fatalf("insert user: %v", err)
	}

	contactID := insertSQLiteContact(t, db, "telegram", "tg-mds-"+uuid.Must(uuid.NewV7()).String()[:8])
	// Set merged_id directly (bypassing MergeUserAggregate pre-checks, which are not the focus).
	if _, err := db.ExecContext(ctx,
		`UPDATE channel_contacts SET merged_id = ? WHERE id = ?`,
		userID, contactID,
	); err != nil {
		t.Fatalf("set merged_id: %v", err)
	}

	// Hard-delete the user — ON DELETE SET NULL must fire.
	if _, err := db.Exec("DELETE FROM users WHERE id = ?", userID); err != nil {
		t.Fatalf("hard-delete user: %v", err)
	}

	var merged *string
	if err := db.QueryRowContext(ctx,
		`SELECT merged_id FROM channel_contacts WHERE id = ?`, contactID,
	).Scan(&merged); err != nil {
		t.Fatalf("read merged_id after user delete: %v", err)
	}
	if merged != nil {
		t.Errorf("expected NULL merged_id after user hard-delete, got %v", *merged)
	}
}

// TestGetContactByChannelAndChatID_NotFound verifies ErrContactNotFound when no row exists.
func TestGetContactByChannelAndChatID_NotFound(t *testing.T) {
	ctx, s, _ := newTestContactStore(t)

	_, err := s.GetContactByChannelAndChatID(ctx, "telegram", "nonexistent-sender-xyz")
	if !errors.Is(err, store.ErrContactNotFound) {
		t.Errorf("expected ErrContactNotFound, got %v", err)
	}
}

// TestGetContactByChannelAndChatID_Found verifies a normal lookup returns the row.
func TestGetContactByChannelAndChatID_Found(t *testing.T) {
	ctx, s, db := newTestContactStore(t)

	senderID := "tg-lookup-" + uuid.Must(uuid.NewV7()).String()[:8]
	contactIDStr := insertSQLiteContact(t, db, "telegram", senderID)

	got, err := s.GetContactByChannelAndChatID(ctx, "telegram", senderID)
	if err != nil {
		t.Fatalf("GetContactByChannelAndChatID: %v", err)
	}
	if got.ID.String() != contactIDStr {
		t.Errorf("ID: got %v want %v", got.ID, contactIDStr)
	}
	if got.SenderID != senderID {
		t.Errorf("SenderID: got %q want %q", got.SenderID, senderID)
	}
}

// TestGetContactByChannelAndChatID_MergedRowReturned verifies a merged contact row
// is returned as-is so the caller can read MergedID and re-route.
func TestGetContactByChannelAndChatID_MergedRowReturned(t *testing.T) {
	ctx, s, db := newTestContactStore(t)

	targetUserID := seedSQLiteContactUser(t, db)
	senderID := "tg-merged-" + uuid.Must(uuid.NewV7()).String()[:8]
	contactIDStr := insertSQLiteContact(t, db, "telegram", senderID)

	// Stamp merged_id directly.
	if _, err := db.ExecContext(ctx,
		`UPDATE channel_contacts SET merged_id = ? WHERE id = ?`,
		targetUserID, contactIDStr,
	); err != nil {
		t.Fatalf("set merged_id: %v", err)
	}

	got, err := s.GetContactByChannelAndChatID(ctx, "telegram", senderID)
	if err != nil {
		t.Fatalf("GetContactByChannelAndChatID: %v", err)
	}
	if got.MergedID == nil {
		t.Fatal("expected MergedID to be set, got nil")
	}
	if got.MergedID.String() != targetUserID {
		t.Errorf("MergedID: got %v want %v", got.MergedID, targetUserID)
	}
}

// TestGetCanonicalDMContact_NotFound verifies ErrContactIDNotFound when no unmerged DM exists.
func TestGetCanonicalDMContact_NotFound(t *testing.T) {
	ctx, s, _ := newTestContactStore(t)

	_, err := s.GetCanonicalDMContact(ctx, uuid.Must(uuid.NewV7()), "telegram")
	if !errors.Is(err, store.ErrContactIDNotFound) {
		t.Errorf("expected ErrContactIDNotFound, got %v", err)
	}
}

// TestGetCanonicalDMContact_Found verifies a normal canonical DM lookup.
func TestGetCanonicalDMContact_Found(t *testing.T) {
	ctx, s, db := newTestContactStore(t)

	userIDStr := seedSQLiteContactUser(t, db)
	userID := uuid.MustParse(userIDStr)
	senderID := "tg-canonical-" + uuid.Must(uuid.NewV7()).String()[:8]

	// Insert an unmerged DM contact linked to userID.
	contactID := uuid.Must(uuid.NewV7()).String()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO channel_contacts (id, channel_type, sender_id, user_id, peer_kind, contact_type)
		 VALUES (?, 'telegram', ?, ?, 'direct', 'user')`,
		contactID, senderID, userIDStr,
	); err != nil {
		t.Fatalf("insert canonical contact: %v", err)
	}

	got, err := s.GetCanonicalDMContact(ctx, userID, "telegram")
	if err != nil {
		t.Fatalf("GetCanonicalDMContact: %v", err)
	}
	if got.SenderID != senderID {
		t.Errorf("SenderID: got %q want %q", got.SenderID, senderID)
	}
	if got.MergedID != nil {
		t.Error("canonical contact must have nil merged_id")
	}
}

// TestUpsertContactDoesNotAutoCreateUsersRow verifies that UpsertContact does NOT
// insert any row into the users table — contacts are discovered entities only.
// A users row is created only via explicit admin MergeUserAggregate calls.
func TestUpsertContactDoesNotAutoCreateUsersRow(t *testing.T) {
	ctx, s, db := newTestContactStore(t)

	senderID := "auto-user-guard-" + uuid.Must(uuid.NewV7()).String()[:8]

	var before int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&before); err != nil {
		t.Fatalf("count users before: %v", err)
	}

	if err := s.UpsertContact(ctx,
		"telegram", "test-instance", senderID,
		"", "Auto Guard Test", "autoguard", "direct", "user", "", "",
	); err != nil {
		t.Fatalf("UpsertContact: %v", err)
	}

	var after int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&after); err != nil {
		t.Fatalf("count users after: %v", err)
	}
	if after != before {
		t.Errorf("UpsertContact must not auto-create users rows: before=%d after=%d", before, after)
	}
}
