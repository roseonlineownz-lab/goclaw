package pg

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// grantTestDB reuses the same PG test setup as project store tests.
func grantTestDB(t *testing.T) *sql.DB {
	t.Helper()
	return projectTestDB(t)
}

// seedGrantOwner inserts a user for FK references in grant tests.
func seedGrantOwner(t *testing.T, db *sql.DB) string {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	suffix := id.String()
	_, err := db.Exec(
		`INSERT INTO users (id, email, password_hash, role, kind, user_key)
		 VALUES ($1, $2, 'x', 'member', 'human', $3)`,
		id, "grant-owner-"+suffix+"@local", "grant-owner-"+suffix,
	)
	if err != nil {
		t.Fatalf("seedGrantOwner: %v", err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM users WHERE id = $1", id) })
	return id.String()
}

// seedGrantProject inserts a minimal project owned by ownerID.
// Slug uses a sanitised subset of the UUID to ensure uniqueness across runs on a shared DB.
func seedGrantProject(t *testing.T, db *sql.DB, ownerID string) string {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	idStr := id.String()
	// Strip hyphens, take first 20 hex chars, prefix 'p' to satisfy [a-z0-9] slug pattern.
	clean := "p" + idStr[:8] + idStr[9:13] + idStr[14:18]
	_, err := db.Exec(
		`INSERT INTO projects (id, slug, owner_user_id, status)
		 VALUES ($1, $2, $3, 'active')`,
		id, clean, ownerID,
	)
	if err != nil {
		t.Fatalf("seedGrantProject: %v", err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM projects WHERE id = $1", id) })
	return id.String()
}

// seedGrantTeam inserts a minimal agent + team.
func seedGrantTeam(t *testing.T, db *sql.DB, ownerID string) string {
	t.Helper()
	return seedTeamForMember(t, db, uuid.MustParse(ownerID)).String()
}

func TestPGProjectGrantStore_CreateAndGet(t *testing.T) {
	db := grantTestDB(t)
	s := NewPGProjectGrantStore(db)
	ctx := context.Background()

	ownerID := seedGrantOwner(t, db)
	projectID := seedGrantProject(t, db, ownerID)
	userID := seedGrantOwner(t, db)

	g := &store.ProjectGrant{
		ProjectID: projectID,
		UserID:    &userID,
		Role:      "viewer",
	}
	if err := s.Create(ctx, g); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if g.ID == "" {
		t.Fatal("Create must set ID")
	}
	t.Cleanup(func() { db.Exec("DELETE FROM project_grants WHERE id = $1", g.ID) })

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

func TestPGProjectGrantStore_CreateTeamGrant(t *testing.T) {
	db := grantTestDB(t)
	s := NewPGProjectGrantStore(db)
	ctx := context.Background()

	ownerID := seedGrantOwner(t, db)
	projectID := seedGrantProject(t, db, ownerID)
	teamID := seedGrantTeam(t, db, ownerID)

	g := &store.ProjectGrant{
		ProjectID: projectID,
		TeamID:    &teamID,
		Role:      "member",
	}
	if err := s.Create(ctx, g); err != nil {
		t.Fatalf("Create team grant: %v", err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM project_grants WHERE id = $1", g.ID) })

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

func TestPGProjectGrantStore_List(t *testing.T) {
	db := grantTestDB(t)
	s := NewPGProjectGrantStore(db)
	ctx := context.Background()

	ownerID := seedGrantOwner(t, db)
	projectID := seedGrantProject(t, db, ownerID)
	userA := seedGrantOwner(t, db)
	userB := seedGrantOwner(t, db)
	teamID := seedGrantTeam(t, db, ownerID)

	for _, g := range []*store.ProjectGrant{
		{ProjectID: projectID, UserID: &userA, Role: "viewer"},
		{ProjectID: projectID, UserID: &userB, Role: "editor"},
		{ProjectID: projectID, TeamID: &teamID, Role: "member"},
	} {
		if err := s.Create(ctx, g); err != nil {
			t.Fatalf("Create: %v", err)
		}
		gID := g.ID
		t.Cleanup(func() { db.Exec("DELETE FROM project_grants WHERE id = $1", gID) })
	}

	grants, err := s.List(ctx, projectID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(grants) < 3 {
		t.Errorf("List: expected ≥3 grants, got %d", len(grants))
	}
}

func TestPGProjectGrantStore_ListForUser(t *testing.T) {
	db := grantTestDB(t)
	s := NewPGProjectGrantStore(db)
	ctx := context.Background()

	ownerID := seedGrantOwner(t, db)
	p1 := seedGrantProject(t, db, ownerID)
	p2 := seedGrantProject(t, db, ownerID)
	userID := seedGrantOwner(t, db)

	g1 := &store.ProjectGrant{ProjectID: p1, UserID: &userID, Role: "viewer"}
	g2 := &store.ProjectGrant{ProjectID: p2, UserID: &userID, Role: "editor"}
	for _, g := range []*store.ProjectGrant{g1, g2} {
		if err := s.Create(ctx, g); err != nil {
			t.Fatalf("Create: %v", err)
		}
		gID := g.ID
		t.Cleanup(func() { db.Exec("DELETE FROM project_grants WHERE id = $1", gID) })
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

func TestPGProjectGrantStore_ListForTeam(t *testing.T) {
	db := grantTestDB(t)
	s := NewPGProjectGrantStore(db)
	ctx := context.Background()

	ownerID := seedGrantOwner(t, db)
	projectID := seedGrantProject(t, db, ownerID)
	teamID := seedGrantTeam(t, db, ownerID)

	g := &store.ProjectGrant{ProjectID: projectID, TeamID: &teamID, Role: "member"}
	if err := s.Create(ctx, g); err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM project_grants WHERE id = $1", g.ID) })

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

func TestPGProjectGrantStore_Delete(t *testing.T) {
	db := grantTestDB(t)
	s := NewPGProjectGrantStore(db)
	ctx := context.Background()

	ownerID := seedGrantOwner(t, db)
	projectID := seedGrantProject(t, db, ownerID)
	userID := seedGrantOwner(t, db)

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

func TestPGProjectGrantStore_XORConstraintRejectsNullNull(t *testing.T) {
	db := grantTestDB(t)
	ctx := context.Background()

	ownerID := seedGrantOwner(t, db)
	projectID := seedGrantProject(t, db, ownerID)

	// Both NULL — must be rejected by CHECK constraint.
	_, err := db.ExecContext(ctx,
		`INSERT INTO project_grants (project_id, user_id, team_id, role)
		 VALUES ($1, NULL, NULL, 'viewer')`, projectID)
	if err == nil {
		t.Error("both-NULL insert must be rejected by CHECK constraint")
	}
}

func TestPGProjectGrantStore_XORConstraintRejectsBothSet(t *testing.T) {
	db := grantTestDB(t)
	ctx := context.Background()

	ownerID := seedGrantOwner(t, db)
	projectID := seedGrantProject(t, db, ownerID)
	userID := seedGrantOwner(t, db)
	teamID := seedGrantTeam(t, db, ownerID)

	// Both set — must be rejected.
	_, err := db.ExecContext(ctx,
		`INSERT INTO project_grants (project_id, user_id, team_id, role)
		 VALUES ($1, $2, $3, 'viewer')`, projectID, userID, teamID)
	if err == nil {
		t.Error("both-set insert must be rejected by CHECK constraint")
	}
}

func TestPGProjectGrantStore_UniqueRejectsDuplicateUserGrant(t *testing.T) {
	db := grantTestDB(t)
	s := NewPGProjectGrantStore(db)
	ctx := context.Background()

	ownerID := seedGrantOwner(t, db)
	projectID := seedGrantProject(t, db, ownerID)
	userID := seedGrantOwner(t, db)

	g1 := &store.ProjectGrant{ProjectID: projectID, UserID: &userID, Role: "viewer"}
	if err := s.Create(ctx, g1); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM project_grants WHERE id = $1", g1.ID) })

	g2 := &store.ProjectGrant{ProjectID: projectID, UserID: &userID, Role: "editor"}
	if err := s.Create(ctx, g2); err == nil {
		db.Exec("DELETE FROM project_grants WHERE id = $1", g2.ID)
		t.Error("duplicate (project_id, user_id) grant must be rejected")
	}
}

func TestPGProjectGrantStore_UniqueRejectsDuplicateTeamGrant(t *testing.T) {
	db := grantTestDB(t)
	s := NewPGProjectGrantStore(db)
	ctx := context.Background()

	ownerID := seedGrantOwner(t, db)
	projectID := seedGrantProject(t, db, ownerID)
	teamID := seedGrantTeam(t, db, ownerID)

	g1 := &store.ProjectGrant{ProjectID: projectID, TeamID: &teamID, Role: "member"}
	if err := s.Create(ctx, g1); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM project_grants WHERE id = $1", g1.ID) })

	g2 := &store.ProjectGrant{ProjectID: projectID, TeamID: &teamID, Role: "editor"}
	if err := s.Create(ctx, g2); err == nil {
		db.Exec("DELETE FROM project_grants WHERE id = $1", g2.ID)
		t.Error("duplicate (project_id, team_id) grant must be rejected")
	}
}

func TestPGProjectGrantStore_RoleCheckRejectsInvalid(t *testing.T) {
	db := grantTestDB(t)
	s := NewPGProjectGrantStore(db)
	ctx := context.Background()

	ownerID := seedGrantOwner(t, db)
	projectID := seedGrantProject(t, db, ownerID)

	for _, bad := range []string{"owner", "admin", "superuser", ""} {
		userID := seedGrantOwner(t, db)
		g := &store.ProjectGrant{ProjectID: projectID, UserID: &userID, Role: bad}
		if err := s.Create(ctx, g); err == nil {
			db.Exec("DELETE FROM project_grants WHERE id = $1", g.ID)
			t.Errorf("role %q must be rejected by CHECK constraint", bad)
		}
	}
}

func TestPGProjectGrantStore_DeleteCascadeOnProject(t *testing.T) {
	db := grantTestDB(t)
	s := NewPGProjectGrantStore(db)
	ctx := context.Background()

	ownerID := seedGrantOwner(t, db)

	// Create project without auto-cleanup so we can delete it manually.
	projID := uuid.Must(uuid.NewV7())
	slug := "casc-" + projID.String()[:8]
	if _, err := db.Exec(
		`INSERT INTO projects (id, slug, owner_user_id, status) VALUES ($1, $2, $3, 'active')`,
		projID, slug, ownerID); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	userID := seedGrantOwner(t, db)
	projIDStr := projID.String()
	g := &store.ProjectGrant{ProjectID: projIDStr, UserID: &userID, Role: "viewer"}
	if err := s.Create(ctx, g); err != nil {
		t.Fatalf("Create grant: %v", err)
	}

	if _, err := db.Exec("DELETE FROM projects WHERE id = $1", projID); err != nil {
		t.Fatalf("delete project: %v", err)
	}

	grants, err := s.List(ctx, projIDStr)
	if err != nil {
		t.Fatalf("List after cascade: %v", err)
	}
	if len(grants) != 0 {
		t.Errorf("CASCADE: expected 0 grants after project delete, got %d", len(grants))
	}
}

func TestPGProjectGrantStore_ResolveProjectRole_Owner(t *testing.T) {
	db := grantTestDB(t)
	s := NewPGProjectGrantStore(db)
	ctx := context.Background()

	ownerID := seedGrantOwner(t, db)
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

func TestPGProjectGrantStore_ResolveProjectRole_TeamGrant(t *testing.T) {
	db := grantTestDB(t)
	s := NewPGProjectGrantStore(db)
	ctx := context.Background()

	ownerID := seedGrantOwner(t, db)
	userID := seedGrantOwner(t, db)
	projectID := seedGrantProject(t, db, ownerID)
	teamID := seedGrantTeam(t, db, ownerID)

	// Add team grant.
	teamIDStr := teamID
	tg := &store.ProjectGrant{ProjectID: projectID, TeamID: &teamIDStr, Role: "member"}
	if err := s.Create(ctx, tg); err != nil {
		t.Fatalf("Create team grant: %v", err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM project_grants WHERE id = $1", tg.ID) })

	// Add user to team via team_user_members.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO team_user_members (team_id, user_id, role) VALUES ($1, $2, 'member')`,
		teamID, userID); err != nil {
		t.Fatalf("add team member: %v", err)
	}
	t.Cleanup(func() {
		db.Exec("DELETE FROM team_user_members WHERE team_id = $1 AND user_id = $2", teamID, userID)
	})

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
