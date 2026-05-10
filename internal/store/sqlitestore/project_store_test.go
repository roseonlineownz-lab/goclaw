//go:build sqlite || sqliteonly

package sqlitestore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// newTestProjectStore opens an in-memory SQLite DB and returns a project store.
func newTestProjectStore(t *testing.T) (context.Context, *SQLiteProjectStore, *SQLiteUsersStore) {
	t.Helper()
	db, err := OpenDB(filepath.Join(t.TempDir(), "projects.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := EnsureSchema(db); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	return context.Background(), NewSQLiteProjectStore(db), NewSQLiteUsersStore(db)
}

// seedSQLiteOwner inserts a minimal user and returns their UUID.
func seedSQLiteOwner(t *testing.T, users *SQLiteUsersStore) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	suffix := id.String()[:8]
	u := &store.User{
		ID:           id,
		Email:        "proj-owner-" + suffix + "@local",
		PasswordHash: "x",
		Role:         "member",
		Kind:         "human",
		UserKey:      "proj-owner-" + suffix,
	}
	if err := users.Create(context.Background(), u); err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	return id
}

func TestSQLiteProjectStore_CreateAndGet(t *testing.T) {
	ctx, s, users := newTestProjectStore(t)
	ownerID := seedSQLiteOwner(t, users)

	p := &store.Project{
		Slug:        "test-create-" + uuid.Must(uuid.NewV7()).String()[:8],
		OwnerUserID: ownerID,
		Status:      "active",
	}
	if err := s.Create(ctx, p); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if p.ID == uuid.Nil {
		t.Fatal("Create must set ID")
	}

	got, err := s.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Slug != p.Slug {
		t.Errorf("slug: got %q want %q", got.Slug, p.Slug)
	}
	if got.Status != "active" {
		t.Errorf("status: got %q want active", got.Status)
	}
	if got.OwnerUserID != ownerID {
		t.Errorf("owner: got %v want %v", got.OwnerUserID, ownerID)
	}

	// Not found
	_, err = s.Get(ctx, uuid.Must(uuid.NewV7()))
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("Get(missing): expected sql.ErrNoRows, got %v", err)
	}
}

func TestSQLiteProjectStore_GetBySlug(t *testing.T) {
	ctx, s, users := newTestProjectStore(t)
	ownerID := seedSQLiteOwner(t, users)
	slug := "by-slug-" + uuid.Must(uuid.NewV7()).String()[:8]

	p := &store.Project{Slug: slug, OwnerUserID: ownerID, Status: "active"}
	if err := s.Create(ctx, p); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := s.GetBySlug(ctx, slug)
	if err != nil {
		t.Fatalf("GetBySlug: %v", err)
	}
	if got.ID != p.ID {
		t.Errorf("id: got %v want %v", got.ID, p.ID)
	}

	// Not found
	_, err = s.GetBySlug(ctx, "no-such-slug-xyz")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetBySlug(missing): expected sql.ErrNoRows, got %v", err)
	}
}

func TestSQLiteProjectStore_List(t *testing.T) {
	ctx, s, users := newTestProjectStore(t)
	ownerID := seedSQLiteOwner(t, users)
	suffix := uuid.Must(uuid.NewV7()).String()[:8]

	pA := &store.Project{Slug: "list-a-" + suffix, OwnerUserID: ownerID, Status: "active"}
	pB := &store.Project{Slug: "list-b-" + suffix, OwnerUserID: ownerID, Status: "archived"}
	for _, p := range []*store.Project{pA, pB} {
		if err := s.Create(ctx, p); err != nil {
			t.Fatalf("Create %s: %v", p.Slug, err)
		}
	}

	// Filter by owner — both returned
	all, err := s.List(ctx, store.ListProjectsFilter{OwnerUserID: ownerID})
	if err != nil {
		t.Fatalf("List by owner: %v", err)
	}
	if len(all) < 2 {
		t.Errorf("expected ≥2 projects, got %d", len(all))
	}

	// Filter by owner + status active
	active, err := s.List(ctx, store.ListProjectsFilter{OwnerUserID: ownerID, Status: "active"})
	if err != nil {
		t.Fatalf("List active: %v", err)
	}
	for _, p := range active {
		if p.Status != "active" {
			t.Errorf("expected active status, got %q", p.Status)
		}
	}

	// Filter by owner + status archived
	archived, err := s.List(ctx, store.ListProjectsFilter{OwnerUserID: ownerID, Status: "archived"})
	if err != nil {
		t.Fatalf("List archived: %v", err)
	}
	if len(archived) != 1 {
		t.Errorf("expected 1 archived project, got %d", len(archived))
	}
}

func TestSQLiteProjectStore_UpdateStatus(t *testing.T) {
	ctx, s, users := newTestProjectStore(t)
	ownerID := seedSQLiteOwner(t, users)

	p := &store.Project{
		Slug:        "upd-status-" + uuid.Must(uuid.NewV7()).String()[:8],
		OwnerUserID: ownerID,
		Status:      "active",
	}
	if err := s.Create(ctx, p); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := s.UpdateStatus(ctx, p.ID, "archived"); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	got, err := s.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if got.Status != "archived" {
		t.Errorf("expected archived, got %q", got.Status)
	}

	// Not found
	if err := s.UpdateStatus(ctx, uuid.Must(uuid.NewV7()), "archived"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("expected sql.ErrNoRows, got %v", err)
	}
}

func TestSQLiteProjectStore_UpdateMetadata(t *testing.T) {
	ctx, s, users := newTestProjectStore(t)
	ownerID := seedSQLiteOwner(t, users)

	p := &store.Project{
		Slug:        "upd-meta-" + uuid.Must(uuid.NewV7()).String()[:8],
		OwnerUserID: ownerID,
		Status:      "active",
	}
	if err := s.Create(ctx, p); err != nil {
		t.Fatalf("Create: %v", err)
	}

	meta := json.RawMessage(`{"env":"test"}`)
	if err := s.UpdateMetadata(ctx, p.ID, meta); err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}
	got, err := s.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("Get after metadata update: %v", err)
	}
	var m map[string]string
	if err := json.Unmarshal(got.Metadata, &m); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if m["env"] != "test" {
		t.Errorf("metadata env mismatch: got %v", m)
	}

	// Not found
	if err := s.UpdateMetadata(ctx, uuid.Must(uuid.NewV7()), meta); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("expected sql.ErrNoRows, got %v", err)
	}
}
