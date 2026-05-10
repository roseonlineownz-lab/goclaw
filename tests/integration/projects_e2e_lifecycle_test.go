//go:build integration

package integration

// E2E lifecycle scenarios 1-5: project create, direct grant, team-max grant,
// session project_id store, and session project_id switch.
// Seed helpers live in projects_e2e_helpers_test.go.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/store/pg"
	"github.com/nextlevelbuilder/goclaw/internal/workspace"
)

// TestProjectsE2E_CreateFolderExists asserts that creating a project via the
// store and calling OnProjectCreate produces a folder at
// $GOCLAW_WORKSPACE_ROOT/projects/<slug>/.
func TestProjectsE2E_CreateFolderExists(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	wsRoot := t.TempDir()

	owner := e2eUser(t, db)
	p := e2eCreateProject(t, ctx, db, owner)

	if err := workspace.OnProjectCreate(ctx, wsRoot, p.Slug); err != nil {
		t.Fatalf("OnProjectCreate: %v", err)
	}

	expected := filepath.Join(wsRoot, "projects", p.Slug)
	info, err := os.Stat(expected)
	if err != nil {
		t.Fatalf("workspace folder not found at %s: %v", expected, err)
	}
	if !info.IsDir() {
		t.Errorf("expected directory at %s", expected)
	}
}

// TestProjectsE2E_GrantDirectViewer asserts a direct user grant resolves to
// viewer and isOwner=false.
func TestProjectsE2E_GrantDirectViewer(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	owner := e2eUser(t, db)
	viewer := e2eUser(t, db)
	p := e2eCreateProject(t, ctx, db, owner)

	gs := pg.NewPGProjectGrantStore(db)
	viewerStr := viewer.String()
	g := &store.ProjectGrant{ProjectID: p.ID.String(), UserID: &viewerStr, Role: "viewer"}
	if err := gs.Create(ctx, g); err != nil {
		t.Fatalf("Create grant: %v", err)
	}

	role, isOwner, found, err := permissions.ResolveProjectRole(ctx, gs, viewer.String(), p.ID.String())
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
		t.Error("isOwner must be false for a direct grant (not the project owner)")
	}
}

// TestProjectsE2E_GrantTeamMaxOverDirectUser asserts that when a user holds a
// direct viewer grant AND belongs to a team with an editor grant, the effective
// role resolves to editor (max-rank wins).
func TestProjectsE2E_GrantTeamMaxOverDirectUser(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	owner := e2eUser(t, db)
	user := e2eUser(t, db)
	teamID := e2eTeam(t, db, owner)
	p := e2eCreateProject(t, ctx, db, owner)

	gs := pg.NewPGProjectGrantStore(db)

	userStr := user.String()
	gUser := &store.ProjectGrant{ProjectID: p.ID.String(), UserID: &userStr, Role: "viewer"}
	if err := gs.Create(ctx, gUser); err != nil {
		t.Fatalf("Create user grant: %v", err)
	}

	teamStr := teamID.String()
	gTeam := &store.ProjectGrant{ProjectID: p.ID.String(), TeamID: &teamStr, Role: "editor"}
	if err := gs.Create(ctx, gTeam); err != nil {
		t.Fatalf("Create team grant: %v", err)
	}

	tums := pg.NewPGTeamUserMemberStore(db)
	if err := tums.AddMember(ctx, teamID.String(), user.String(), "member", nil); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	t.Cleanup(func() {
		db.Exec("DELETE FROM team_user_members WHERE team_id = $1 AND user_id = $2", teamID, user)
	})

	role, isOwner, found, err := permissions.ResolveProjectRole(ctx, gs, user.String(), p.ID.String())
	if err != nil {
		t.Fatalf("ResolveProjectRole: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if role != permissions.ProjectRoleEditor {
		t.Errorf("role = %q, want %q (max of viewer+editor)", role, permissions.ProjectRoleEditor)
	}
	if isOwner {
		t.Error("isOwner must be false — team-grant path does not make user an owner")
	}
}

// TestProjectsE2E_SessionProjectIDStored asserts a session created with a
// non-nil project_id persists the FK.
func TestProjectsE2E_SessionProjectIDStored(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	owner := e2eUser(t, db)
	_, agentID := seedTenantAgent(t, db)
	p := e2eCreateProject(t, ctx, db, owner)

	sessionKey := "e2e:spid:" + uuid.New().String()[:8]
	if err := insertSessionWithProject(db, sessionKey, agentID, &p.ID); err != nil {
		t.Fatalf("insertSessionWithProject: %v", err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM agent_sessions WHERE session_key = $1", sessionKey) })

	var stored *uuid.UUID
	db.QueryRowContext(ctx,
		`SELECT project_id FROM agent_sessions WHERE session_key = $1`, sessionKey,
	).Scan(&stored)
	if stored == nil || *stored != p.ID {
		t.Errorf("session.project_id = %v, want %v", stored, p.ID)
	}
}

// TestProjectsE2E_UpdateProjectSwitchesFK asserts UpdateProject replaces the
// session's project_id with the new value.
func TestProjectsE2E_UpdateProjectSwitchesFK(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	owner := e2eUser(t, db)
	_, agentID := seedTenantAgent(t, db)
	p1 := e2eCreateProject(t, ctx, db, owner)
	p2 := e2eCreateProject(t, ctx, db, owner)

	sessionKey := "e2e:upd:" + uuid.New().String()[:8]
	if err := insertSessionWithProject(db, sessionKey, agentID, &p1.ID); err != nil {
		t.Fatalf("insertSessionWithProject: %v", err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM agent_sessions WHERE session_key = $1", sessionKey) })

	ss := pg.NewPGSessionStore(db)
	if err := ss.UpdateProject(ctx, sessionKey, &p2.ID); err != nil {
		t.Fatalf("UpdateProject: %v", err)
	}

	var stored *uuid.UUID
	db.QueryRowContext(ctx,
		`SELECT project_id FROM agent_sessions WHERE session_key = $1`, sessionKey,
	).Scan(&stored)
	if stored == nil || *stored != p2.ID {
		t.Errorf("session.project_id after update = %v, want %v", stored, p2.ID)
	}
}
