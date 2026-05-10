//go:build integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/store/pg"
	"github.com/nextlevelbuilder/goclaw/internal/workspace"
)

// projectSwitchTestDeps wraps the four stores SwitchSessionProject needs and
// a tempdir baseDir. Reduces boilerplate across the test cases below.
type projectSwitchTestDeps struct {
	deps    workspace.ProjectSwitchDeps
	baseDir string
	owner   uuid.UUID
}

func newProjectSwitchTestDeps(t *testing.T) projectSwitchTestDeps {
	t.Helper()
	db := testDB(t)
	pg.InitSqlx(db)
	owner := seedUserForShares(t, db)
	return projectSwitchTestDeps{
		deps: workspace.ProjectSwitchDeps{
			Sessions:  pg.NewPGSessionStore(db),
			Projects:  pg.NewPGProjectStore(db),
			Episodics: pg.NewPGEpisodicStore(db),
			BaseDir:   t.TempDir(),
		},
		baseDir: "",
		owner:   owner,
	}
}

func (d projectSwitchTestDeps) seedProject(t *testing.T, slug string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	db := testDB(t)
	_, err := db.Exec(
		`INSERT INTO projects (id, owner_user_id, slug, status)
		 VALUES ($1, $2, $3, 'active')`,
		id, d.owner, slug,
	)
	if err != nil {
		t.Fatalf("seed project %q: %v", slug, err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM projects WHERE id = $1", id) })
	return id
}

func (d projectSwitchTestDeps) seedSession(t *testing.T, agentID uuid.UUID, projectID *uuid.UUID) string {
	t.Helper()
	key := "test-switch-" + uuid.New().String()[:8]
	db := testDB(t)
	if err := insertSessionWithProject(db, key, agentID, projectID); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM agent_sessions WHERE session_key = $1", key) })
	return key
}

// seedSessionFile creates a marker file under the session subdir so the test
// can verify FS relocation moved it. Returns the relative path inside the
// session subdir.
func seedSessionFile(t *testing.T, baseDir, slug, sessionKey, name, content string) string {
	t.Helper()
	dir := filepath.Join(baseDir, "projects", slug, "sessions", workspace.SanitizeSegment(sessionKey))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return filepath.Join(dir, name)
}

func TestSwitchSessionProject_HappyPath(t *testing.T) {
	d := newProjectSwitchTestDeps(t)
	_, agentID := seedTenantAgent(t, testDB(t))

	pid1 := d.seedProject(t, "p-from-"+uuid.New().String()[:6])
	pid2 := d.seedProject(t, "p-to-"+uuid.New().String()[:6])

	sessionKey := d.seedSession(t, agentID, &pid1)

	// Resolve slugs for path-construction in the test.
	slug1, _ := d.deps.Projects.Get(context.Background(), pid1)
	slug2, _ := d.deps.Projects.Get(context.Background(), pid2)

	// Pre-seed a marker file in the OLD session subdir.
	seedSessionFile(t, d.deps.BaseDir, slug1.Slug, sessionKey, "note.md", "hello-from-p1")

	if err := workspace.SwitchSessionProject(context.Background(), d.deps, sessionKey, &pid2); err != nil {
		t.Fatalf("SwitchSessionProject: %v", err)
	}

	// DB: session now bound to pid2.
	sess := d.deps.Sessions.Get(context.Background(), sessionKey)
	if sess == nil || sess.ProjectID == nil || *sess.ProjectID != pid2 {
		t.Errorf("session.ProjectID = %v, want %v", sess.ProjectID, pid2)
	}

	// FS: file moved to new project subdir.
	newPath := filepath.Join(d.deps.BaseDir, "projects", slug2.Slug, "sessions",
		workspace.SanitizeSegment(sessionKey), "note.md")
	got, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatalf("expected file at new path %q: %v", newPath, err)
	}
	if string(got) != "hello-from-p1" {
		t.Errorf("file content lost: got %q", string(got))
	}

	// Old session subdir should not exist anymore (rename-only path).
	oldDir := filepath.Join(d.deps.BaseDir, "projects", slug1.Slug, "sessions",
		workspace.SanitizeSegment(sessionKey))
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Errorf("old session subdir still exists: %v", oldDir)
	}
}

func TestSwitchSessionProject_NoOpSameProject(t *testing.T) {
	d := newProjectSwitchTestDeps(t)
	_, agentID := seedTenantAgent(t, testDB(t))

	pid := d.seedProject(t, "p-noop-"+uuid.New().String()[:6])
	sessionKey := d.seedSession(t, agentID, &pid)

	if err := workspace.SwitchSessionProject(context.Background(), d.deps, sessionKey, &pid); err != nil {
		t.Fatalf("SwitchSessionProject: %v", err)
	}

	sess := d.deps.Sessions.Get(context.Background(), sessionKey)
	if sess == nil || sess.ProjectID == nil || *sess.ProjectID != pid {
		t.Errorf("ProjectID changed unexpectedly: %v", sess.ProjectID)
	}
}

func TestSwitchSessionProject_ClearsBinding(t *testing.T) {
	d := newProjectSwitchTestDeps(t)
	_, agentID := seedTenantAgent(t, testDB(t))

	pid := d.seedProject(t, "p-clear-"+uuid.New().String()[:6])
	sessionKey := d.seedSession(t, agentID, &pid)

	if err := workspace.SwitchSessionProject(context.Background(), d.deps, sessionKey, nil); err != nil {
		t.Fatalf("SwitchSessionProject(nil): %v", err)
	}

	sess := d.deps.Sessions.Get(context.Background(), sessionKey)
	if sess == nil || sess.ProjectID != nil {
		t.Errorf("expected project_id NULL after clear; got %v", sess.ProjectID)
	}
}

func TestSwitchSessionProject_NonexistentProjectFailsFast(t *testing.T) {
	d := newProjectSwitchTestDeps(t)
	_, agentID := seedTenantAgent(t, testDB(t))

	pid := d.seedProject(t, "p-good-"+uuid.New().String()[:6])
	sessionKey := d.seedSession(t, agentID, &pid)

	bogus := uuid.New()
	err := workspace.SwitchSessionProject(context.Background(), d.deps, sessionKey, &bogus)
	if err == nil {
		t.Fatal("expected error when switching to nonexistent project")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error message should mention 'not found': %v", err)
	}

	// DB binding must be unchanged (no partial DB update before failure).
	sess := d.deps.Sessions.Get(context.Background(), sessionKey)
	if sess == nil || sess.ProjectID == nil || *sess.ProjectID != pid {
		t.Errorf("session.ProjectID corrupted by failed switch: got %v, want %v", sess.ProjectID, pid)
	}
}

// TestSwitchSessionProject_RenameIntoExistingDir verifies the B2a strict
// orphan policy: when the target session subdir already exists (e.g. user
// previously switched away from P2 and is now switching back), os.Rename
// fails with ENOTEMPTY. The DB binding must still flip to the new project,
// and the failure must be logged but not surface to the caller.
func TestSwitchSessionProject_RenameIntoExistingDir(t *testing.T) {
	d := newProjectSwitchTestDeps(t)
	_, agentID := seedTenantAgent(t, testDB(t))

	pid1 := d.seedProject(t, "p-rifrom-"+uuid.New().String()[:6])
	pid2 := d.seedProject(t, "p-rito-"+uuid.New().String()[:6])
	slug1, _ := d.deps.Projects.Get(context.Background(), pid1)
	slug2, _ := d.deps.Projects.Get(context.Background(), pid2)

	sessionKey := d.seedSession(t, agentID, &pid1)

	// Pre-seed BOTH old and new dirs (target exists with content).
	seedSessionFile(t, d.deps.BaseDir, slug1.Slug, sessionKey, "old.md", "old-data")
	seedSessionFile(t, d.deps.BaseDir, slug2.Slug, sessionKey, "existing.md", "pre-existing")

	// Rename target dir to be non-empty so os.Rename fails with ENOTEMPTY.
	if err := workspace.SwitchSessionProject(context.Background(), d.deps, sessionKey, &pid2); err != nil {
		t.Fatalf("SwitchSessionProject must not surface FS rename failure: %v", err)
	}

	// DB binding flipped despite FS failure (B2a strict-orphan).
	sess := d.deps.Sessions.Get(context.Background(), sessionKey)
	if sess == nil || sess.ProjectID == nil || *sess.ProjectID != pid2 {
		t.Errorf("DB binding did not flip; got %v, want %v", sess.ProjectID, pid2)
	}

	// Pre-existing file at new dir is preserved.
	preExisting := filepath.Join(d.deps.BaseDir, "projects", slug2.Slug, "sessions",
		workspace.SanitizeSegment(sessionKey), "existing.md")
	if _, err := os.Stat(preExisting); err != nil {
		t.Errorf("pre-existing file lost: %v", err)
	}
}

// TestSwitchSessionProject_EpisodicRetag verifies session-scoped episodic
// rows get re-tagged on switch while non-session-scoped memory tables are
// left alone (per Q1 decision).
func TestSwitchSessionProject_EpisodicRetag(t *testing.T) {
	d := newProjectSwitchTestDeps(t)
	db := testDB(t)
	_, agentID := seedTenantAgent(t, db)
	userID := seedUserForShares(t, db)

	pid1 := d.seedProject(t, "p-epfrom-"+uuid.New().String()[:6])
	pid2 := d.seedProject(t, "p-epto-"+uuid.New().String()[:6])
	sessionKey := d.seedSession(t, agentID, &pid1)

	// Insert an episodic row tagged to the OLD project + matching session_key.
	epID := uuid.Must(uuid.NewV7())
	_, err := db.Exec(
		`INSERT INTO episodic_summaries
			(id, agent_id, user_id, project_id, session_key, summary, source_id, source_type)
		 VALUES ($1, $2, $3, $4, $5, 'a summary', 'src-1', 'session')`,
		epID, agentID, userID, pid1, sessionKey,
	)
	if err != nil {
		t.Fatalf("seed episodic: %v", err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM episodic_summaries WHERE id = $1", epID) })

	if err := workspace.SwitchSessionProject(context.Background(), d.deps, sessionKey, &pid2); err != nil {
		t.Fatalf("SwitchSessionProject: %v", err)
	}

	var got uuid.UUID
	if err := db.QueryRow(
		`SELECT project_id FROM episodic_summaries WHERE id = $1`, epID,
	).Scan(&got); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if got != pid2 {
		t.Errorf("episodic.project_id not retagged: got %v, want %v", got, pid2)
	}
}

// TestSwitchSessionProject_MutexSerialization fires two concurrent switches
// on the same session_key in opposite directions. The mutex must serialise
// them so the final DB state matches one of the two intended endpoints
// (i.e. atomic UPDATE-then-rename pairs do not interleave).
func TestSwitchSessionProject_MutexSerialization(t *testing.T) {
	d := newProjectSwitchTestDeps(t)
	_, agentID := seedTenantAgent(t, testDB(t))

	pid1 := d.seedProject(t, "p-mu1-"+uuid.New().String()[:6])
	pid2 := d.seedProject(t, "p-mu2-"+uuid.New().String()[:6])
	sessionKey := d.seedSession(t, agentID, &pid1)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = workspace.SwitchSessionProject(context.Background(), d.deps, sessionKey, &pid2)
	}()
	go func() {
		defer wg.Done()
		_ = workspace.SwitchSessionProject(context.Background(), d.deps, sessionKey, &pid1)
	}()
	wg.Wait()

	sess := d.deps.Sessions.Get(context.Background(), sessionKey)
	if sess == nil || sess.ProjectID == nil {
		t.Fatal("expected non-nil ProjectID")
	}
	final := *sess.ProjectID
	if final != pid1 && final != pid2 {
		t.Errorf("final project_id = %v, must be one of {pid1=%v, pid2=%v}", final, pid1, pid2)
	}
	_ = store.SessionData{} // keep import
}
