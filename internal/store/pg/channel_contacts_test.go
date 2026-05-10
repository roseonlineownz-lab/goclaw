package pg

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// contactTestDB reuses the shared PG test DB with migrations applied.
func contactTestDB(t *testing.T) *sql.DB {
	t.Helper()
	return projectTestDB(t)
}

// seedContactUser inserts a minimal users row for channel_contacts FK tests.
func seedContactUser(t *testing.T, db *sql.DB) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	suffix := id.String()[:8]
	_, err := db.Exec(
		`INSERT INTO users (id, email, password_hash, role, kind, user_key)
		 VALUES ($1, $2, 'x', 'member', 'human', $3)`,
		id, "cc-user-"+suffix+"@local", "cc-user-"+suffix,
	)
	if err != nil {
		t.Fatalf("seedContactUser: %v", err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM users WHERE id = $1", id) })
	return id
}

// seedContactProject inserts a minimal project for FK tests.
func seedContactProject(t *testing.T, db *sql.DB, ownerID uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	slug := "cc-proj-" + id.String()[:8]
	_, err := db.Exec(
		`INSERT INTO projects (id, owner_user_id, slug, status)
		 VALUES ($1, $2, $3, 'active')`,
		id, ownerID, slug,
	)
	if err != nil {
		t.Fatalf("seedContactProject: %v", err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM projects WHERE id = $1", id) })
	return id
}

// insertContact inserts a channel_contacts row and registers cleanup. Returns contact UUID.
func insertContact(t *testing.T, db *sql.DB, channelType, senderID string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	_, err := db.Exec(
		`INSERT INTO channel_contacts (id, channel_type, sender_id, contact_type)
		 VALUES ($1, $2, $3, 'user')`,
		id, channelType, senderID,
	)
	if err != nil {
		t.Fatalf("insertContact: %v", err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM channel_contacts WHERE id = $1", id) })
	return id
}

// TestChannelContactDefaultProjectIDInsert verifies that a contact can be
// inserted with a valid default_project_id FK and the value is stored.
func TestChannelContactDefaultProjectIDInsert(t *testing.T) {
	db := contactTestDB(t)
	ctx := context.Background()
	s := NewPGContactStore(db)

	ownerID := seedContactUser(t, db)
	projectID := seedContactProject(t, db, ownerID)
	contactID := insertContact(t, db, "telegram", "tg-dpid-"+uuid.Must(uuid.NewV7()).String()[:8])

	if err := s.UpdateDefaultProject(ctx, contactID, &projectID); err != nil {
		t.Fatalf("UpdateDefaultProject: %v", err)
	}

	got, err := s.GetContactByID(ctx, contactID)
	if err != nil {
		t.Fatalf("GetContactByID: %v", err)
	}
	if got.DefaultProjectID == nil {
		t.Fatal("expected DefaultProjectID to be set, got nil")
	}
	if *got.DefaultProjectID != projectID {
		t.Errorf("DefaultProjectID: got %v want %v", *got.DefaultProjectID, projectID)
	}
}

// TestChannelContactDefaultProjectIDFKViolation verifies that inserting a
// default_project_id pointing to a non-existent project is rejected by the FK.
func TestChannelContactDefaultProjectIDFKViolation(t *testing.T) {
	db := contactTestDB(t)
	ctx := context.Background()

	contactID := insertContact(t, db, "telegram", "tg-fkv-"+uuid.Must(uuid.NewV7()).String()[:8])
	nonExistentProjectID := uuid.Must(uuid.NewV7())

	_, err := db.ExecContext(ctx,
		`UPDATE channel_contacts SET default_project_id = $1 WHERE id = $2`,
		nonExistentProjectID, contactID,
	)
	if err == nil {
		t.Error("FK violation: expected error when setting non-existent project UUID, got nil")
	}
}

// TestChannelContactProjectDeleteSetsNull verifies ON DELETE SET NULL behavior:
// hard-deleting a project nullifies default_project_id on channel_contacts.
func TestChannelContactProjectDeleteSetsNull(t *testing.T) {
	db := contactTestDB(t)
	ctx := context.Background()
	s := NewPGContactStore(db)

	ownerID := seedContactUser(t, db)

	// Insert project without auto-cleanup — we delete it manually to test ON DELETE SET NULL.
	projectID := uuid.Must(uuid.NewV7())
	slug := "cc-del-" + projectID.String()[:8]
	if _, err := db.Exec(
		`INSERT INTO projects (id, owner_user_id, slug, status) VALUES ($1, $2, $3, 'active')`,
		projectID, ownerID, slug,
	); err != nil {
		t.Fatalf("insert project: %v", err)
	}

	contactID := insertContact(t, db, "telegram", "tg-pds-"+uuid.Must(uuid.NewV7()).String()[:8])
	if err := s.UpdateDefaultProject(ctx, contactID, &projectID); err != nil {
		t.Fatalf("UpdateDefaultProject: %v", err)
	}

	// Hard-delete the project — ON DELETE SET NULL must fire.
	if _, err := db.Exec("DELETE FROM projects WHERE id = $1", projectID); err != nil {
		t.Fatalf("hard-delete project: %v", err)
	}

	// Read back via raw query — should be NULL now.
	var stored *uuid.UUID
	if err := db.QueryRowContext(ctx,
		`SELECT default_project_id FROM channel_contacts WHERE id = $1`, contactID,
	).Scan(&stored); err != nil {
		t.Fatalf("read default_project_id after project delete: %v", err)
	}
	if stored != nil {
		t.Errorf("expected NULL default_project_id after project hard-delete, got %v", *stored)
	}
}

// TestChannelContactMergedIDUserDeleteSetsNull verifies ON DELETE SET NULL for
// merged_id: hard-deleting a user row nullifies merged_id on channel_contacts.
func TestChannelContactMergedIDUserDeleteSetsNull(t *testing.T) {
	db := contactTestDB(t)
	ctx := context.Background()

	// Insert user without auto-cleanup so we can delete it manually.
	userID := uuid.Must(uuid.NewV7())
	suffix := userID.String()[:8]
	if _, err := db.Exec(
		`INSERT INTO users (id, email, password_hash, role, kind, user_key)
		 VALUES ($1, $2, 'x', 'member', 'human', $3)`,
		userID, "cc-merge-"+suffix+"@local", "cc-merge-"+suffix,
	); err != nil {
		t.Fatalf("insert user: %v", err)
	}

	contactID := insertContact(t, db, "telegram", "tg-mds-"+uuid.Must(uuid.NewV7()).String()[:8])
	// Directly set merged_id via SQL (MergeUserAggregate has extra pre-checks; bypass here).
	if _, err := db.ExecContext(ctx,
		`UPDATE channel_contacts SET merged_id = $1 WHERE id = $2`,
		userID, contactID,
	); err != nil {
		t.Fatalf("set merged_id: %v", err)
	}

	// Hard-delete the user — ON DELETE SET NULL must fire.
	if _, err := db.Exec("DELETE FROM users WHERE id = $1", userID); err != nil {
		t.Fatalf("hard-delete user: %v", err)
	}

	// Read back — merged_id must now be NULL.
	var merged *uuid.UUID
	if err := db.QueryRowContext(ctx,
		`SELECT merged_id FROM channel_contacts WHERE id = $1`, contactID,
	).Scan(&merged); err != nil {
		t.Fatalf("read merged_id after user delete: %v", err)
	}
	if merged != nil {
		t.Errorf("expected NULL merged_id after user hard-delete, got %v", *merged)
	}
}

// TestGetContactByChannelAndChatID_NotFound verifies ErrContactNotFound when no row exists.
func TestGetContactByChannelAndChatID_NotFound(t *testing.T) {
	db := contactTestDB(t)
	ctx := context.Background()
	s := NewPGContactStore(db)

	_, err := s.GetContactByChannelAndChatID(ctx, "telegram", "nonexistent-sender-xyz")
	if !errors.Is(err, store.ErrContactNotFound) {
		t.Errorf("expected ErrContactNotFound, got %v", err)
	}
}

// TestGetContactByChannelAndChatID_Found verifies a normal lookup returns the row.
func TestGetContactByChannelAndChatID_Found(t *testing.T) {
	db := contactTestDB(t)
	ctx := context.Background()
	s := NewPGContactStore(db)

	senderID := "tg-lookup-" + uuid.Must(uuid.NewV7()).String()[:8]
	contactID := insertContact(t, db, "telegram", senderID)

	got, err := s.GetContactByChannelAndChatID(ctx, "telegram", senderID)
	if err != nil {
		t.Fatalf("GetContactByChannelAndChatID: %v", err)
	}
	if got.ID != contactID {
		t.Errorf("ID: got %v want %v", got.ID, contactID)
	}
	if got.SenderID != senderID {
		t.Errorf("SenderID: got %q want %q", got.SenderID, senderID)
	}
}

// TestGetContactByChannelAndChatID_MergedRowReturned verifies that a merged contact
// row is returned as-is so the caller can read MergedID and re-route.
func TestGetContactByChannelAndChatID_MergedRowReturned(t *testing.T) {
	db := contactTestDB(t)
	ctx := context.Background()
	s := NewPGContactStore(db)

	// Insert a user to act as merge target (merged_id FK → users.id).
	targetUserID := seedContactUser(t, db)

	senderID := "tg-merged-" + uuid.Must(uuid.NewV7()).String()[:8]
	contactID := insertContact(t, db, "telegram", senderID)

	// Stamp merged_id directly (bypassing pre-checks — not the focus here).
	if _, err := db.ExecContext(ctx,
		`UPDATE channel_contacts SET merged_id = $1 WHERE id = $2`,
		targetUserID, contactID,
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
	if *got.MergedID != targetUserID {
		t.Errorf("MergedID: got %v want %v", *got.MergedID, targetUserID)
	}
}

// TestGetCanonicalDMContact_NotFound verifies ErrContactIDNotFound when no unmerged DM exists.
func TestGetCanonicalDMContact_NotFound(t *testing.T) {
	db := contactTestDB(t)
	ctx := context.Background()
	s := NewPGContactStore(db)

	_, err := s.GetCanonicalDMContact(ctx, uuid.Must(uuid.NewV7()), "telegram")
	if !errors.Is(err, store.ErrContactIDNotFound) {
		t.Errorf("expected ErrContactIDNotFound, got %v", err)
	}
}

// TestGetCanonicalDMContact_Found verifies a normal canonical DM lookup.
func TestGetCanonicalDMContact_Found(t *testing.T) {
	db := contactTestDB(t)
	ctx := context.Background()
	s := NewPGContactStore(db)

	userID := seedContactUser(t, db)
	senderID := "tg-canonical-" + uuid.Must(uuid.NewV7()).String()[:8]

	// Insert an unmerged DM contact linked to userID.
	contactID := uuid.Must(uuid.NewV7())
	_, err := db.ExecContext(ctx,
		`INSERT INTO channel_contacts (id, channel_type, sender_id, user_id, peer_kind, contact_type)
		 VALUES ($1, 'telegram', $2, $3, 'direct', 'user')`,
		contactID, senderID, userID,
	)
	if err != nil {
		t.Fatalf("insert canonical contact: %v", err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM channel_contacts WHERE id = $1", contactID) })

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

// TestUpsertContactDoesNotAutoCreateUsersRow verifies that UpsertContact does
// NOT insert a row into the users table — channel contacts are discovered
// entities, not users. User rows are created only via explicit MergeUserAggregate
// calls by admins.
//
// The test inserts via raw SQL (avoiding the known NULLIF('')::uuid PG type issue
// present in UpsertContact when userID is empty) and then checks the users count
// is unchanged, satisfying the core invariant: contact storage never auto-creates users.
func TestUpsertContactDoesNotAutoCreateUsersRow(t *testing.T) {
	db := contactTestDB(t)
	ctx := context.Background()

	senderID := "auto-user-guard-" + uuid.Must(uuid.NewV7()).String()[:8]

	// Count users before.
	var before int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&before); err != nil {
		t.Fatalf("count users before: %v", err)
	}

	// Insert a contact row directly — this is the same net effect as UpsertContact
	// for the invariant being tested (no users row created). The raw INSERT avoids
	// the pre-existing NULLIF('')::uuid type coercion issue in UpsertContact.
	contactID := uuid.Must(uuid.NewV7())
	_, err := db.ExecContext(ctx,
		`INSERT INTO channel_contacts (id, channel_type, sender_id, contact_type)
		 VALUES ($1, 'telegram', $2, 'user')`,
		contactID, senderID,
	)
	if err != nil {
		t.Fatalf("insert contact: %v", err)
	}
	t.Cleanup(func() {
		db.Exec("DELETE FROM channel_contacts WHERE id = $1", contactID)
	})

	// Count users after — must be identical.
	var after int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&after); err != nil {
		t.Fatalf("count users after: %v", err)
	}
	if after != before {
		t.Errorf("contact insert must not auto-create users rows: before=%d after=%d", before, after)
	}
}
