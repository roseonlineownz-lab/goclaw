package pg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// projectTestDB opens the test PG connection, runs migrations, and skips if unavailable.
func projectTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PG project store tests")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open DB: %v", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		t.Skipf("PG not reachable: %v", err)
	}
	m, err := migrate.New("file://../../../migrations", dsn)
	if err != nil {
		db.Close()
		t.Fatalf("migrate.New: %v", err)
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		db.Close()
		t.Fatalf("migrate up: %v", err)
	}
	m.Close()
	t.Cleanup(func() { db.Close() })
	return db
}

// seedOwner inserts a minimal users row for FK references.
func seedOwner(t *testing.T, db *sql.DB) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	suffix := id.String()[:8]
	_, err := db.Exec(
		`INSERT INTO users (id, email, password_hash, role, kind, user_key)
		 VALUES ($1, $2, 'x', 'member', 'human', $3)`,
		id, "proj-owner-"+suffix+"@local", "proj-owner-"+suffix,
	)
	if err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM users WHERE id = $1", id) })
	return id
}

func TestPGProjectStore_CreateAndGet(t *testing.T) {
	db := projectTestDB(t)
	s := NewPGProjectStore(db)
	ctx := context.Background()
	ownerID := seedOwner(t, db)

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
	t.Cleanup(func() { db.Exec("DELETE FROM projects WHERE id = $1", p.ID) })

	got, err := s.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Slug != p.Slug {
		t.Errorf("slug mismatch: got %q want %q", got.Slug, p.Slug)
	}
	if got.Status != "active" {
		t.Errorf("status mismatch: got %q", got.Status)
	}
	if got.OwnerUserID != ownerID {
		t.Errorf("owner mismatch: got %v want %v", got.OwnerUserID, ownerID)
	}
}

func TestPGProjectStore_GetBySlug(t *testing.T) {
	db := projectTestDB(t)
	s := NewPGProjectStore(db)
	ctx := context.Background()
	ownerID := seedOwner(t, db)
	slug := "by-slug-" + uuid.Must(uuid.NewV7()).String()[:8]

	p := &store.Project{Slug: slug, OwnerUserID: ownerID, Status: "active"}
	if err := s.Create(ctx, p); err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM projects WHERE id = $1", p.ID) })

	got, err := s.GetBySlug(ctx, slug)
	if err != nil {
		t.Fatalf("GetBySlug: %v", err)
	}
	if got.ID != p.ID {
		t.Errorf("id mismatch: got %v want %v", got.ID, p.ID)
	}

	// Not found
	_, err = s.GetBySlug(ctx, "no-such-slug-xyz")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("expected sql.ErrNoRows, got %v", err)
	}
}

func TestPGProjectStore_List(t *testing.T) {
	db := projectTestDB(t)
	s := NewPGProjectStore(db)
	ctx := context.Background()
	ownerID := seedOwner(t, db)

	suffix := uuid.Must(uuid.NewV7()).String()[:8]
	slugA := "list-a-" + suffix
	slugB := "list-b-" + suffix

	pA := &store.Project{Slug: slugA, OwnerUserID: ownerID, Status: "active"}
	pB := &store.Project{Slug: slugB, OwnerUserID: ownerID, Status: "archived"}
	for _, p := range []*store.Project{pA, pB} {
		if err := s.Create(ctx, p); err != nil {
			t.Fatalf("Create %s: %v", p.Slug, err)
		}
		p := p
		t.Cleanup(func() { db.Exec("DELETE FROM projects WHERE id = $1", p.ID) })
	}

	// Filter by owner — both returned
	all, err := s.List(ctx, store.ListProjectsFilter{OwnerUserID: ownerID})
	if err != nil {
		t.Fatalf("List by owner: %v", err)
	}
	if len(all) < 2 {
		t.Errorf("expected ≥2 projects, got %d", len(all))
	}

	// Filter by owner + status
	active, err := s.List(ctx, store.ListProjectsFilter{OwnerUserID: ownerID, Status: "active"})
	if err != nil {
		t.Fatalf("List active: %v", err)
	}
	for _, p := range active {
		if p.Status != "active" {
			t.Errorf("expected active, got %q", p.Status)
		}
	}
}

func TestPGProjectStore_UpdateStatus(t *testing.T) {
	db := projectTestDB(t)
	s := NewPGProjectStore(db)
	ctx := context.Background()
	ownerID := seedOwner(t, db)

	p := &store.Project{
		Slug:        "upd-status-" + uuid.Must(uuid.NewV7()).String()[:8],
		OwnerUserID: ownerID,
		Status:      "active",
	}
	if err := s.Create(ctx, p); err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM projects WHERE id = $1", p.ID) })

	if err := s.UpdateStatus(ctx, p.ID, "archived"); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	got, _ := s.Get(ctx, p.ID)
	if got.Status != "archived" {
		t.Errorf("expected archived, got %q", got.Status)
	}

	// Not found
	if err := s.UpdateStatus(ctx, uuid.Must(uuid.NewV7()), "archived"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("expected sql.ErrNoRows, got %v", err)
	}
}

func TestPGProjectStore_UpdateMetadata(t *testing.T) {
	db := projectTestDB(t)
	s := NewPGProjectStore(db)
	ctx := context.Background()
	ownerID := seedOwner(t, db)

	p := &store.Project{
		Slug:        "upd-meta-" + uuid.Must(uuid.NewV7()).String()[:8],
		OwnerUserID: ownerID,
		Status:      "active",
	}
	if err := s.Create(ctx, p); err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM projects WHERE id = $1", p.ID) })

	meta := json.RawMessage(`{"key":"value"}`)
	if err := s.UpdateMetadata(ctx, p.ID, meta); err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}
	got, _ := s.Get(ctx, p.ID)
	var m map[string]string
	if err := json.Unmarshal(got.Metadata, &m); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if m["key"] != "value" {
		t.Errorf("metadata key mismatch: got %v", m)
	}

	// Not found
	if err := s.UpdateMetadata(ctx, uuid.Must(uuid.NewV7()), meta); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("expected sql.ErrNoRows, got %v", err)
	}
}
