//go:build sqliteonly && integration

package integration

// SQLite parity — lifecycle scenarios 1-5.
// Helpers and DB setup live in projects_sqlite_parity_helpers_test.go.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/store/sqlitestore"
	"github.com/nextlevelbuilder/goclaw/internal/workspace"
)

// TestSQLiteProjectsParity_CreateFolderExists mirrors the PG lifecycle scenario:
// create project + OnProjectCreate → folder at $GOCLAW_WORKSPACE_ROOT/projects/<slug>.
func TestSQLiteProjectsParity_CreateFolderExists(t *testing.T) {
	db := newProjectsParityDB(t)
	ctx := context.Background()
	wsRoot := t.TempDir()

	ownerID := sqliteE2EUser(t, db)
	p := sqliteE2EProject(t, ctx, db, ownerID)

	if err := workspace.OnProjectCreate(ctx, wsRoot, p.Slug); err != nil {
		t.Fatalf("OnProjectCreate: %v", err)
	}

	expected := filepath.Join(wsRoot, "projects", p.Slug)
	info, err := os.Stat(expected)
	if err != nil {
		t.Fatalf("workspace folder not found: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("expected directory at %s", expected)
	}
}

// TestSQLiteProjectsParity_GrantDirectViewer asserts the SQLite ResolveProjectRole
// UNION ALL query returns viewer and isOwner=false for a direct user grant.
func TestSQLiteProjectsParity_GrantDirectViewer(t *testing.T) {
	db := newProjectsParityDB(t)
	ctx := context.Background()

	ownerID := sqliteE2EUser(t, db)
	viewerID := sqliteE2EUser(t, db)
	p := sqliteE2EProject(t, ctx, db, ownerID)

	gs := sqlitestore.NewSQLiteProjectGrantStore(db)
	g := &store.ProjectGrant{ProjectID: p.ID.String(), UserID: &viewerID, Role: "viewer"}
	if err := gs.Create(ctx, g); err != nil {
		t.Fatalf("Create grant: %v", err)
	}

	role, isOwner, found, err := permissions.ResolveProjectRole(ctx, gs, viewerID, p.ID.String())
	if err != nil {
		t.Fatalf("ResolveProjectRole: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if role != permissions.ProjectRoleViewer {
		t.Errorf("role = %q, want %q", role, permissions.ProjectRoleViewer)
	}
	if isOwner {
		t.Error("isOwner must be false for a direct grant")
	}
}

// TestSQLiteProjectsParity_GrantTeamMaxOverDirect asserts the SQLite UNION ALL
// resolver picks editor (rank 3) over viewer (rank 1) when both apply.
func TestSQLiteProjectsParity_GrantTeamMaxOverDirect(t *testing.T) {
	db := newProjectsParityDB(t)
	ctx := context.Background()

	ownerID := sqliteE2EUser(t, db)
	userID := sqliteE2EUser(t, db)
	teamID := sqliteE2ETeam(t, db, ownerID)
	p := sqliteE2EProject(t, ctx, db, ownerID)

	gs := sqlitestore.NewSQLiteProjectGrantStore(db)

	gUser := &store.ProjectGrant{ProjectID: p.ID.String(), UserID: &userID, Role: "viewer"}
	if err := gs.Create(ctx, gUser); err != nil {
		t.Fatalf("Create user grant: %v", err)
	}

	gTeam := &store.ProjectGrant{ProjectID: p.ID.String(), TeamID: &teamID, Role: "editor"}
	if err := gs.Create(ctx, gTeam); err != nil {
		t.Fatalf("Create team grant: %v", err)
	}

	tums := sqlitestore.NewSQLiteTeamUserMemberStore(db)
	if err := tums.AddMember(ctx, teamID, userID, "member", nil); err != nil {
		t.Fatalf("AddMember: %v", err)
	}

	role, isOwner, found, err := permissions.ResolveProjectRole(ctx, gs, userID, p.ID.String())
	if err != nil {
		t.Fatalf("ResolveProjectRole: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if role != permissions.ProjectRoleEditor {
		t.Errorf("role = %q, want editor (max wins)", role)
	}
	if isOwner {
		t.Error("isOwner must be false — team-grant path")
	}
}

// TestSQLiteProjectsParity_SessionProjectIDStored asserts the SQLite schema
// persists the project_id FK on session insert.
func TestSQLiteProjectsParity_SessionProjectIDStored(t *testing.T) {
	db := newProjectsParityDB(t)
	ctx := context.Background()

	ownerID := sqliteE2EUser(t, db)
	agentID := sqliteE2EAgent(t, db, ownerID)
	p := sqliteE2EProject(t, ctx, db, ownerID)

	projIDStr := p.ID.String()
	sessionKey := sqliteE2ESession(t, db, agentID, &projIDStr)

	var stored *string
	db.QueryRowContext(ctx,
		`SELECT project_id FROM agent_sessions WHERE session_key = ?`, sessionKey,
	).Scan(&stored)
	if stored == nil || *stored != projIDStr {
		t.Errorf("session.project_id = %v, want %v", stored, projIDStr)
	}
}

// TestSQLiteProjectsParity_UpdateProjectSwitchesFK asserts UpdateProject
// replaces the session FK via the SQLite session store.
func TestSQLiteProjectsParity_UpdateProjectSwitchesFK(t *testing.T) {
	db := newProjectsParityDB(t)
	ctx := context.Background()

	ownerID := sqliteE2EUser(t, db)
	agentID := sqliteE2EAgent(t, db, ownerID)
	p1 := sqliteE2EProject(t, ctx, db, ownerID)
	p2 := sqliteE2EProject(t, ctx, db, ownerID)

	p1Str := p1.ID.String()
	sessionKey := sqliteE2ESession(t, db, agentID, &p1Str)

	ss := sqlitestore.NewSQLiteSessionStore(db)
	if err := ss.UpdateProject(ctx, sessionKey, &p2.ID); err != nil {
		t.Fatalf("UpdateProject: %v", err)
	}

	var stored *string
	db.QueryRowContext(ctx,
		`SELECT project_id FROM agent_sessions WHERE session_key = ?`, sessionKey,
	).Scan(&stored)
	p2Str := p2.ID.String()
	if stored == nil || *stored != p2Str {
		t.Errorf("session.project_id after update = %v, want %v", stored, p2Str)
	}
}
