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

// newGrantTestEnv opens an in-memory SQLite DB ready for grant store tests.
func newGrantTestEnv(t *testing.T) (context.Context, *SQLiteProjectGrantStore, *sql.DB) {
	t.Helper()
	db, err := OpenDB(filepath.Join(t.TempDir(), "grants.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := EnsureSchema(db); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	return context.Background(), NewSQLiteProjectGrantStore(db), db
}

// seedGrantUser inserts a minimal users row and returns its UUID string.
func seedGrantUser(t *testing.T, db *sql.DB) string {
	t.Helper()
	id := uuid.Must(uuid.NewV7()).String()
	_, err := db.Exec(
		`INSERT INTO users (id, email, password_hash, role, kind, user_key)
		 VALUES (?, ?, 'x', 'member', 'human', ?)`,
		id, "gu-"+id+"@local", "gu-"+id,
	)
	if err != nil {
		t.Fatalf("seedGrantUser: %v", err)
	}
	return id
}

// seedGrantProject inserts a minimal project and returns its UUID string.
// Slug uses a sanitised subset of the UUID to avoid collisions across test calls.
func seedGrantProject(t *testing.T, db *sql.DB, ownerID string) string {
	t.Helper()
	id := uuid.Must(uuid.NewV7()).String()
	// Build a slug from the UUID by stripping hyphens: prefix + first 20 hex chars.
	clean := "p" + id[:8] + id[9:13] + id[14:18]
	_, err := db.Exec(
		`INSERT INTO projects (id, slug, owner_user_id, status, metadata)
		 VALUES (?, ?, ?, 'active', '{}')`,
		id, clean, ownerID,
	)
	if err != nil {
		t.Fatalf("seedGrantProject: %v", err)
	}
	return id
}

// seedGrantTeam inserts a minimal agent+team and returns the team UUID string.
func seedGrantTeam(t *testing.T, db *sql.DB, ownerID string) string {
	t.Helper()
	return seedSQLiteTeam(t, db, ownerID)
}

func TestSQLiteProjectGrantStore_CreateAndGet(t *testing.T) {
	ctx, s, db := newGrantTestEnv(t)
	ownerID := seedGrantUser(t, db)
	projectID := seedGrantProject(t, db, ownerID)
	userID := seedGrantUser(t, db)

	g := &store.ProjectGrant{ProjectID: projectID, UserID: &userID, Role: "viewer"}
	if err := s.Create(ctx, g); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if g.ID == "" {
		t.Fatal("Create must set ID")
	}

	got, err := s.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ProjectID != projectID {
		t.Errorf("project_id: got %q want %q", got.ProjectID, projectID)
	}
	if got.UserID == nil || *got.UserID != userID {
		t.Errorf("user_id: got %v want %q", got.UserID, userID)
	}
	if got.TeamID != nil {
		t.Errorf("team_id: expected nil, got %v", got.TeamID)
	}
	if got.Role != "viewer" {
		t.Errorf("role: got %q want viewer", got.Role)
	}
}

func TestSQLiteProjectGrantStore_CreateTeamGrant(t *testing.T) {
	ctx, s, db := newGrantTestEnv(t)
	ownerID := seedGrantUser(t, db)
	projectID := seedGrantProject(t, db, ownerID)
	teamID := seedGrantTeam(t, db, ownerID)

	g := &store.ProjectGrant{ProjectID: projectID, TeamID: &teamID, Role: "member"}
	if err := s.Create(ctx, g); err != nil {
		t.Fatalf("Create team grant: %v", err)
	}

	got, err := s.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.TeamID == nil || *got.TeamID != teamID {
		t.Errorf("team_id: got %v want %q", got.TeamID, teamID)
	}
	if got.UserID != nil {
		t.Errorf("user_id: expected nil for team grant, got %v", got.UserID)
	}
}

func TestSQLiteProjectGrantStore_List(t *testing.T) {
	ctx, s, db := newGrantTestEnv(t)
	ownerID := seedGrantUser(t, db)
	projectID := seedGrantProject(t, db, ownerID)
	userA := seedGrantUser(t, db)
	userB := seedGrantUser(t, db)
	teamID := seedGrantTeam(t, db, ownerID)

	for _, g := range []*store.ProjectGrant{
		{ProjectID: projectID, UserID: &userA, Role: "viewer"},
		{ProjectID: projectID, UserID: &userB, Role: "editor"},
		{ProjectID: projectID, TeamID: &teamID, Role: "member"},
	} {
		if err := s.Create(ctx, g); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	grants, err := s.List(ctx, projectID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(grants) < 3 {
		t.Errorf("List: expected ≥3 grants, got %d", len(grants))
	}
}

func TestSQLiteProjectGrantStore_ListForUser(t *testing.T) {
	ctx, s, db := newGrantTestEnv(t)
	ownerID := seedGrantUser(t, db)
	p1 := seedGrantProject(t, db, ownerID)
	p2 := seedGrantProject(t, db, ownerID)
	userID := seedGrantUser(t, db)

	for _, g := range []*store.ProjectGrant{
		{ProjectID: p1, UserID: &userID, Role: "viewer"},
		{ProjectID: p2, UserID: &userID, Role: "editor"},
	} {
		if err := s.Create(ctx, g); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	grants, err := s.ListForUser(ctx, userID)
	if err != nil {
		t.Fatalf("ListForUser: %v", err)
	}
	if len(grants) < 2 {
		t.Errorf("ListForUser: expected ≥2, got %d", len(grants))
	}
	for _, g := range grants {
		if g.UserID == nil || *g.UserID != userID {
			t.Errorf("ListForUser: unexpected user_id %v", g.UserID)
		}
	}
}

func TestSQLiteProjectGrantStore_ListForTeam(t *testing.T) {
	ctx, s, db := newGrantTestEnv(t)
	ownerID := seedGrantUser(t, db)
	projectID := seedGrantProject(t, db, ownerID)
	teamID := seedGrantTeam(t, db, ownerID)

	g := &store.ProjectGrant{ProjectID: projectID, TeamID: &teamID, Role: "member"}
	if err := s.Create(ctx, g); err != nil {
		t.Fatalf("Create: %v", err)
	}

	grants, err := s.ListForTeam(ctx, teamID)
	if err != nil {
		t.Fatalf("ListForTeam: %v", err)
	}
	if len(grants) != 1 {
		t.Fatalf("ListForTeam: expected 1, got %d", len(grants))
	}
	if grants[0].TeamID == nil || *grants[0].TeamID != teamID {
		t.Errorf("ListForTeam: wrong team_id %v", grants[0].TeamID)
	}
}

func TestSQLiteProjectGrantStore_Delete(t *testing.T) {
	ctx, s, db := newGrantTestEnv(t)
	ownerID := seedGrantUser(t, db)
	projectID := seedGrantProject(t, db, ownerID)
	userID := seedGrantUser(t, db)

	g := &store.ProjectGrant{ProjectID: projectID, UserID: &userID, Role: "viewer"}
	if err := s.Create(ctx, g); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := s.Delete(ctx, g.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := s.Get(ctx, g.ID)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("after Delete: expected sql.ErrNoRows, got %v", err)
	}
}

func TestSQLiteProjectGrantStore_XORConstraintRejectsNullNull(t *testing.T) {
	ctx, _, db := newGrantTestEnv(t)
	ownerID := seedGrantUser(t, db)
	projectID := seedGrantProject(t, db, ownerID)

	id := uuid.Must(uuid.NewV7()).String()
	_, err := db.ExecContext(ctx,
		`INSERT INTO project_grants (id, project_id, user_id, team_id, role)
		 VALUES (?, ?, NULL, NULL, 'viewer')`,
		id, projectID,
	)
	if err == nil {
		t.Error("both-NULL insert must be rejected by CHECK constraint")
	}
}

func TestSQLiteProjectGrantStore_XORConstraintRejectsBothSet(t *testing.T) {
	ctx, _, db := newGrantTestEnv(t)
	ownerID := seedGrantUser(t, db)
	projectID := seedGrantProject(t, db, ownerID)
	userID := seedGrantUser(t, db)
	teamID := seedGrantTeam(t, db, ownerID)

	id := uuid.Must(uuid.NewV7()).String()
	_, err := db.ExecContext(ctx,
		`INSERT INTO project_grants (id, project_id, user_id, team_id, role)
		 VALUES (?, ?, ?, ?, 'viewer')`,
		id, projectID, userID, teamID,
	)
	if err == nil {
		t.Error("both-set insert must be rejected by CHECK constraint")
	}
}

func TestSQLiteProjectGrantStore_UniqueRejectsDuplicateUserGrant(t *testing.T) {
	ctx, s, db := newGrantTestEnv(t)
	ownerID := seedGrantUser(t, db)
	projectID := seedGrantProject(t, db, ownerID)
	userID := seedGrantUser(t, db)

	g1 := &store.ProjectGrant{ProjectID: projectID, UserID: &userID, Role: "viewer"}
	if err := s.Create(ctx, g1); err != nil {
		t.Fatalf("first Create: %v", err)
	}

	g2 := &store.ProjectGrant{ProjectID: projectID, UserID: &userID, Role: "editor"}
	if err := s.Create(ctx, g2); err == nil {
		t.Error("duplicate (project_id, user_id) grant must be rejected")
	}
}

func TestSQLiteProjectGrantStore_UniqueRejectsDuplicateTeamGrant(t *testing.T) {
	ctx, s, db := newGrantTestEnv(t)
	ownerID := seedGrantUser(t, db)
	projectID := seedGrantProject(t, db, ownerID)
	teamID := seedGrantTeam(t, db, ownerID)

	g1 := &store.ProjectGrant{ProjectID: projectID, TeamID: &teamID, Role: "member"}
	if err := s.Create(ctx, g1); err != nil {
		t.Fatalf("first Create: %v", err)
	}

	g2 := &store.ProjectGrant{ProjectID: projectID, TeamID: &teamID, Role: "editor"}
	if err := s.Create(ctx, g2); err == nil {
		t.Error("duplicate (project_id, team_id) grant must be rejected")
	}
}

func TestSQLiteProjectGrantStore_RoleCheckRejectsInvalid(t *testing.T) {
	ctx, s, db := newGrantTestEnv(t)
	ownerID := seedGrantUser(t, db)
	projectID := seedGrantProject(t, db, ownerID)

	for _, bad := range []string{"owner", "admin", "superuser", ""} {
		userID := seedGrantUser(t, db)
		g := &store.ProjectGrant{ProjectID: projectID, UserID: &userID, Role: bad}
		if err := s.Create(ctx, g); err == nil {
			t.Errorf("role %q must be rejected by CHECK constraint", bad)
		}
	}
}

func TestSQLiteProjectGrantStore_DeleteCascadeOnProject(t *testing.T) {
	ctx, s, db := newGrantTestEnv(t)
	ownerID := seedGrantUser(t, db)

	// Create project without cleanup so we can delete it manually.
	projID := uuid.Must(uuid.NewV7()).String()
	slug := "casc-" + projID[:8]
	if _, err := db.ExecContext(ctx,
		`INSERT INTO projects (id, slug, owner_user_id, status, metadata)
		 VALUES (?, ?, ?, 'active', '{}')`,
		projID, slug, ownerID); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	userID := seedGrantUser(t, db)
	g := &store.ProjectGrant{ProjectID: projID, UserID: &userID, Role: "viewer"}
	if err := s.Create(ctx, g); err != nil {
		t.Fatalf("Create grant: %v", err)
	}

	if _, err := db.ExecContext(ctx, "DELETE FROM projects WHERE id = ?", projID); err != nil {
		t.Fatalf("delete project: %v", err)
	}

	grants, err := s.List(ctx, projID)
	if err != nil {
		t.Fatalf("List after cascade: %v", err)
	}
	if len(grants) != 0 {
		t.Errorf("CASCADE: expected 0 grants after project delete, got %d", len(grants))
	}
}

func TestSQLiteProjectGrantStore_ResolveProjectRole_Owner(t *testing.T) {
	ctx, s, db := newGrantTestEnv(t)
	ownerID := seedGrantUser(t, db)
	projectID := seedGrantProject(t, db, ownerID)

	rank, isOwner, found, err := s.ResolveProjectRole(ctx, ownerID, projectID)
	if err != nil {
		t.Fatalf("ResolveProjectRole: %v", err)
	}
	if !found {
		t.Fatal("expected found=true for owner")
	}
	if rank != 3 {
		t.Errorf("owner rank: got %d want 3", rank)
	}
	if !isOwner {
		t.Error("isOwner: expected true")
	}
}

func TestSQLiteProjectGrantStore_ResolveProjectRole_TeamGrant(t *testing.T) {
	ctx, s, db := newGrantTestEnv(t)
	ownerID := seedGrantUser(t, db)
	userID := seedGrantUser(t, db)
	projectID := seedGrantProject(t, db, ownerID)
	teamID := seedGrantTeam(t, db, ownerID)

	// Add team grant on project.
	tg := &store.ProjectGrant{ProjectID: projectID, TeamID: &teamID, Role: "member"}
	if err := s.Create(ctx, tg); err != nil {
		t.Fatalf("Create team grant: %v", err)
	}

	// Add user to team.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO team_user_members (team_id, user_id, role) VALUES (?, ?, 'member')`,
		teamID, userID); err != nil {
		t.Fatalf("add team member: %v", err)
	}

	rank, isOwner, found, err := s.ResolveProjectRole(ctx, userID, projectID)
	if err != nil {
		t.Fatalf("ResolveProjectRole: %v", err)
	}
	if !found {
		t.Fatal("expected found=true via team grant")
	}
	if rank != 2 { // member=2
		t.Errorf("team member rank: got %d want 2", rank)
	}
	if isOwner {
		t.Error("isOwner: expected false for team-granted user")
	}
}

func TestSQLiteProjectGrantStore_ResolveProjectRole_NoAccess(t *testing.T) {
	ctx, s, db := newGrantTestEnv(t)
	ownerID := seedGrantUser(t, db)
	userID := seedGrantUser(t, db)
	projectID := seedGrantProject(t, db, ownerID)

	_, _, found, err := s.ResolveProjectRole(ctx, userID, projectID)
	if err != nil {
		t.Fatalf("ResolveProjectRole: %v", err)
	}
	if found {
		t.Error("expected found=false for user with no grant")
	}
}
