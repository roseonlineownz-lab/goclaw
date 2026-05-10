//go:build sqlite || sqliteonly

package permissions_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/store/sqlitestore"
)

// resolverEnv holds all stores and helpers for a single resolver test.
type resolverEnv struct {
	ctx     context.Context
	db      *sql.DB
	grants  store.ProjectGrantStore
	members store.TeamUserMemberStore
}

func newResolverEnv(t *testing.T) *resolverEnv {
	t.Helper()
	db, err := sqlitestore.OpenDB(filepath.Join(t.TempDir(), "resolver.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := sqlitestore.EnsureSchema(db); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	return &resolverEnv{
		ctx:     context.Background(),
		db:      db,
		grants:  sqlitestore.NewSQLiteProjectGrantStore(db),
		members: sqlitestore.NewSQLiteTeamUserMemberStore(db),
	}
}

// seedUser inserts a minimal users row and returns its UUID string.
func (e *resolverEnv) seedUser(t *testing.T) string {
	t.Helper()
	id := uuid.Must(uuid.NewV7()).String()
	_, err := e.db.ExecContext(e.ctx,
		`INSERT INTO users (id, email, password_hash, role, kind, user_key)
		 VALUES (?, ?, 'x', 'member', 'human', ?)`,
		id, "u-"+id+"@local", "u-"+id,
	)
	if err != nil {
		t.Fatalf("seedUser: %v", err)
	}
	return id
}

// seedProject inserts a minimal project owned by ownerID and returns its UUID string.
// The slug uses a sanitised form of the full UUID to avoid collisions across tests.
func (e *resolverEnv) seedProject(t *testing.T, ownerID string) string {
	t.Helper()
	id := uuid.Must(uuid.NewV7()).String()
	// Strip hyphens and take first 30 chars so the slug stays within 100 chars
	// and matches the [a-z0-9][a-z0-9-]+ pattern without leading/trailing hyphens.
	clean := "p" + id[:8] + id[9:13] + id[14:18]
	_, err := e.db.ExecContext(e.ctx,
		`INSERT INTO projects (id, slug, owner_user_id, status, metadata)
		 VALUES (?, ?, ?, 'active', '{}')`,
		id, clean, ownerID,
	)
	if err != nil {
		t.Fatalf("seedProject: %v", err)
	}
	return id
}

// seedArchivedProject inserts a project with status='archived'.
func (e *resolverEnv) seedArchivedProject(t *testing.T, ownerID string) string {
	t.Helper()
	id := uuid.Must(uuid.NewV7()).String()
	clean := "a" + id[:8] + id[9:13] + id[14:18]
	_, err := e.db.ExecContext(e.ctx,
		`INSERT INTO projects (id, slug, owner_user_id, status, metadata)
		 VALUES (?, ?, ?, 'archived', '{}')`,
		id, clean, ownerID,
	)
	if err != nil {
		t.Fatalf("seedArchivedProject: %v", err)
	}
	return id
}

// seedTeam inserts a minimal agent + team and returns the team UUID string.
func (e *resolverEnv) seedTeam(t *testing.T, ownerID string) string {
	t.Helper()
	agentID := uuid.Must(uuid.NewV7()).String()
	asuf := agentID[:8]
	_, err := e.db.ExecContext(e.ctx,
		`INSERT INTO agents (id, agent_key, owner_id, owner_user_id, provider, model, metadata)
		 VALUES (?, ?, ?, ?, 'openai', 'gpt-4o', '{}')`,
		agentID, "agt-"+asuf, ownerID, ownerID,
	)
	if err != nil {
		t.Fatalf("seedTeam agent: %v", err)
	}
	teamID := uuid.Must(uuid.NewV7()).String()
	tsuf := teamID[:8]
	_, err = e.db.ExecContext(e.ctx,
		`INSERT INTO agent_teams (id, name, lead_agent_id, created_by, team_key, metadata)
		 VALUES (?, ?, ?, ?, ?, '{}')`,
		teamID, "team-"+tsuf, agentID, ownerID, "team-"+tsuf,
	)
	if err != nil {
		t.Fatalf("seedTeam team: %v", err)
	}
	return teamID
}

// addUserGrant creates a direct user grant on a project.
func (e *resolverEnv) addUserGrant(t *testing.T, projectID, userID, role string) {
	t.Helper()
	g := &store.ProjectGrant{ProjectID: projectID, UserID: &userID, Role: role}
	if err := e.grants.Create(e.ctx, g); err != nil {
		t.Fatalf("addUserGrant: %v", err)
	}
}

// addTeamGrant creates a team grant on a project.
func (e *resolverEnv) addTeamGrant(t *testing.T, projectID, teamID, role string) {
	t.Helper()
	g := &store.ProjectGrant{ProjectID: projectID, TeamID: &teamID, Role: role}
	if err := e.grants.Create(e.ctx, g); err != nil {
		t.Fatalf("addTeamGrant: %v", err)
	}
}

// addTeamMember adds userID as a member of teamID.
func (e *resolverEnv) addTeamMember(t *testing.T, teamID, userID string) {
	t.Helper()
	if err := e.members.AddMember(e.ctx, teamID, userID, "member", nil); err != nil {
		t.Fatalf("addTeamMember: %v", err)
	}
}

// --- Resolver scenario tests ---

// TestResolveProjectRole_OwnerHasEditor asserts the project creator receives
// editor-level access with isOwner=true even without an explicit grant row.
func TestResolveProjectRole_OwnerHasEditor(t *testing.T) {
	e := newResolverEnv(t)
	ownerID := e.seedUser(t)
	projectID := e.seedProject(t, ownerID)

	role, isOwner, found, err := permissions.ResolveProjectRole(e.ctx, e.grants, ownerID, projectID)
	if err != nil {
		t.Fatalf("ResolveProjectRole: %v", err)
	}
	if !found {
		t.Fatal("expected found=true for project owner")
	}
	if role != permissions.ProjectRoleEditor {
		t.Errorf("owner role: got %q want %q", role, permissions.ProjectRoleEditor)
	}
	if !isOwner {
		t.Error("isOwner: expected true for project creator")
	}
}

// TestResolveProjectRole_DirectViewerGrant asserts a user with an explicit
// viewer grant gets role=viewer and isOwner=false.
func TestResolveProjectRole_DirectViewerGrant(t *testing.T) {
	e := newResolverEnv(t)
	ownerID := e.seedUser(t)
	userID := e.seedUser(t)
	projectID := e.seedProject(t, ownerID)
	e.addUserGrant(t, projectID, userID, "viewer")

	role, isOwner, found, err := permissions.ResolveProjectRole(e.ctx, e.grants, userID, projectID)
	if err != nil {
		t.Fatalf("ResolveProjectRole: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if role != permissions.ProjectRoleViewer {
		t.Errorf("role: got %q want viewer", role)
	}
	if isOwner {
		t.Error("isOwner: expected false for non-owner")
	}
}

// TestResolveProjectRole_TeamGrantImplicit asserts that a user inherits the
// team's grant when they are a member of the team (via team_user_members).
func TestResolveProjectRole_TeamGrantImplicit(t *testing.T) {
	e := newResolverEnv(t)
	ownerID := e.seedUser(t)
	userID := e.seedUser(t)
	projectID := e.seedProject(t, ownerID)
	teamID := e.seedTeam(t, ownerID)

	e.addTeamGrant(t, projectID, teamID, "member")
	e.addTeamMember(t, teamID, userID)

	role, isOwner, found, err := permissions.ResolveProjectRole(e.ctx, e.grants, userID, projectID)
	if err != nil {
		t.Fatalf("ResolveProjectRole: %v", err)
	}
	if !found {
		t.Fatal("expected found=true via team grant")
	}
	if role != permissions.ProjectRoleMember {
		t.Errorf("role: got %q want member", role)
	}
	if isOwner {
		t.Error("isOwner: expected false for team-granted user")
	}
}

// TestResolveProjectRole_MaxRoleWinsDirectVsTeam asserts that when a user has
// both a direct viewer grant and a team editor grant, the higher role wins.
func TestResolveProjectRole_MaxRoleWinsDirectVsTeam(t *testing.T) {
	e := newResolverEnv(t)
	ownerID := e.seedUser(t)
	userID := e.seedUser(t)
	projectID := e.seedProject(t, ownerID)
	teamID := e.seedTeam(t, ownerID)

	e.addUserGrant(t, projectID, userID, "viewer") // lower
	e.addTeamGrant(t, projectID, teamID, "editor") // higher
	e.addTeamMember(t, teamID, userID)

	role, _, found, err := permissions.ResolveProjectRole(e.ctx, e.grants, userID, projectID)
	if err != nil {
		t.Fatalf("ResolveProjectRole: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if role != permissions.ProjectRoleEditor {
		t.Errorf("max role: got %q want editor", role)
	}
}

// TestResolveProjectRole_NoGrant asserts that a user with no grant on a project
// gets found=false.
func TestResolveProjectRole_NoGrant(t *testing.T) {
	e := newResolverEnv(t)
	ownerID := e.seedUser(t)
	userID := e.seedUser(t)
	projectID := e.seedProject(t, ownerID)

	_, _, found, err := permissions.ResolveProjectRole(e.ctx, e.grants, userID, projectID)
	if err != nil {
		t.Fatalf("ResolveProjectRole: %v", err)
	}
	if found {
		t.Error("expected found=false for user with no grant")
	}
}

// TestResolveProjectRole_ArchivedProjectStillReturnsRole asserts the resolver
// returns the effective role even for archived projects — status gating is the
// caller's responsibility.
func TestResolveProjectRole_ArchivedProjectStillReturnsRole(t *testing.T) {
	e := newResolverEnv(t)
	ownerID := e.seedUser(t)
	projectID := e.seedArchivedProject(t, ownerID)

	role, isOwner, found, err := permissions.ResolveProjectRole(e.ctx, e.grants, ownerID, projectID)
	if err != nil {
		t.Fatalf("ResolveProjectRole: %v", err)
	}
	if !found {
		t.Fatal("expected found=true: owner of archived project still has access")
	}
	if role != permissions.ProjectRoleEditor {
		t.Errorf("role: got %q want editor", role)
	}
	if !isOwner {
		t.Error("isOwner: expected true")
	}
}

// TestResolveProjectRole_CrossProjectIsolation asserts a grant on project P1
// does not grant access to project P2.
func TestResolveProjectRole_CrossProjectIsolation(t *testing.T) {
	e := newResolverEnv(t)
	ownerID := e.seedUser(t)
	userID := e.seedUser(t)
	p1 := e.seedProject(t, ownerID)
	p2 := e.seedProject(t, ownerID)

	e.addUserGrant(t, p1, userID, "editor") // grant only on p1

	_, _, found, err := permissions.ResolveProjectRole(e.ctx, e.grants, userID, p2)
	if err != nil {
		t.Fatalf("ResolveProjectRole p2: %v", err)
	}
	if found {
		t.Error("cross-project: grant on p1 must not grant access to p2")
	}
}

// TestCanAccessProject_ViewerBelowEditorThreshold asserts CanAccessProject returns
// false when the user's role is below the required minimum.
func TestCanAccessProject_ViewerBelowEditorThreshold(t *testing.T) {
	e := newResolverEnv(t)
	ownerID := e.seedUser(t)
	userID := e.seedUser(t)
	projectID := e.seedProject(t, ownerID)
	e.addUserGrant(t, projectID, userID, "viewer")

	ok, err := permissions.CanAccessProject(e.ctx, e.grants, userID, projectID, permissions.ProjectRoleEditor)
	if err != nil {
		t.Fatalf("CanAccessProject: %v", err)
	}
	if ok {
		t.Error("viewer must not satisfy minRole=editor")
	}
}

// TestCanAccessProject_EditorAboveViewerThreshold asserts CanAccessProject returns
// true when the user's role meets or exceeds the minimum.
func TestCanAccessProject_EditorAboveViewerThreshold(t *testing.T) {
	e := newResolverEnv(t)
	ownerID := e.seedUser(t)
	userID := e.seedUser(t)
	projectID := e.seedProject(t, ownerID)
	e.addUserGrant(t, projectID, userID, "editor")

	ok, err := permissions.CanAccessProject(e.ctx, e.grants, userID, projectID, permissions.ProjectRoleViewer)
	if err != nil {
		t.Fatalf("CanAccessProject: %v", err)
	}
	if !ok {
		t.Error("editor must satisfy minRole=viewer")
	}
}

// TestResolveProjectRole_TeamMemberNotInTeam asserts that a user who is NOT a
// member of the team does not inherit the team's project grant.
func TestResolveProjectRole_TeamMemberNotInTeam(t *testing.T) {
	e := newResolverEnv(t)
	ownerID := e.seedUser(t)
	userID := e.seedUser(t) // NOT added to team
	projectID := e.seedProject(t, ownerID)
	teamID := e.seedTeam(t, ownerID)

	e.addTeamGrant(t, projectID, teamID, "editor")
	// userID is not in teamID → no access

	_, _, found, err := permissions.ResolveProjectRole(e.ctx, e.grants, userID, projectID)
	if err != nil {
		t.Fatalf("ResolveProjectRole: %v", err)
	}
	if found {
		t.Error("non-member of team must not inherit team grant")
	}
}

// TestResolveProjectRole_MemberEqualsMinRole asserts that member role satisfies
// minRole=member but not minRole=editor.
func TestResolveProjectRole_MemberEqualsMinRole(t *testing.T) {
	e := newResolverEnv(t)
	ownerID := e.seedUser(t)
	userID := e.seedUser(t)
	projectID := e.seedProject(t, ownerID)
	e.addUserGrant(t, projectID, userID, "member")

	okMember, err := permissions.CanAccessProject(e.ctx, e.grants, userID, projectID, permissions.ProjectRoleMember)
	if err != nil {
		t.Fatalf("CanAccessProject member: %v", err)
	}
	if !okMember {
		t.Error("member must satisfy minRole=member")
	}

	okEditor, err := permissions.CanAccessProject(e.ctx, e.grants, userID, projectID, permissions.ProjectRoleEditor)
	if err != nil {
		t.Fatalf("CanAccessProject editor: %v", err)
	}
	if okEditor {
		t.Error("member must not satisfy minRole=editor")
	}
}

// TestResolveProjectRole_NoAccessReturnsErrNoRowsNever asserts the resolver
// never returns sql.ErrNoRows — it converts that to found=false.
func TestResolveProjectRole_NoAccessReturnsErrNoRowsNever(t *testing.T) {
	e := newResolverEnv(t)
	ownerID := e.seedUser(t)
	userID := e.seedUser(t)
	projectID := e.seedProject(t, ownerID)

	_, _, found, err := permissions.ResolveProjectRole(e.ctx, e.grants, userID, projectID)
	if err != nil {
		// Specifically must not be sql.ErrNoRows leaking to callers.
		if errors.Is(err, sql.ErrNoRows) {
			t.Error("resolver must not surface sql.ErrNoRows — convert to found=false")
		}
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Error("expected found=false")
	}
}
