//go:build sqlite || sqliteonly

package permissions_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/store/sqlitestore"
)

// channelPermEnv holds stores for channel default project permission tests.
type channelPermEnv struct {
	ctx      context.Context
	db       *sql.DB
	contacts store.ContactStore
	agents   store.AgentStore
	projects store.ProjectStore
	grants   store.ProjectGrantStore
	instances store.ChannelInstanceStore
}

func newChannelPermEnv(t *testing.T) *channelPermEnv {
	t.Helper()
	db, err := sqlitestore.OpenDB(filepath.Join(t.TempDir(), "chperm.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := sqlitestore.EnsureSchema(db); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	return &channelPermEnv{
		ctx:       context.Background(),
		db:        db,
		contacts:  sqlitestore.NewSQLiteContactStore(db),
		agents:    sqlitestore.NewSQLiteAgentStore(db),
		projects:  sqlitestore.NewSQLiteProjectStore(db),
		grants:    sqlitestore.NewSQLiteProjectGrantStore(db),
		instances: sqlitestore.NewSQLiteChannelInstanceStore(db, "0123456789abcdef0123456789abcdef"),
	}
}

func (e *channelPermEnv) deps() permissions.ChannelDefaultProjectDeps {
	return permissions.ChannelDefaultProjectDeps{
		Contacts:         e.contacts,
		ChannelInstances: e.instances,
		Agents:           e.agents,
		Projects:         e.projects,
		ProjectGrants:    e.grants,
	}
}

// seedUserCh inserts a minimal users row and returns its UUID string.
func (e *channelPermEnv) seedUser(t *testing.T) string {
	t.Helper()
	id := uuid.Must(uuid.NewV7()).String()
	_, err := e.db.ExecContext(e.ctx,
		`INSERT INTO users (id, email, password_hash, role, kind, user_key)
		 VALUES (?, ?, 'x', 'member', 'human', ?)`,
		id, "cu-"+id+"@local", "cu-"+id,
	)
	if err != nil {
		t.Fatalf("seedUser: %v", err)
	}
	return id
}

// seedProject inserts a minimal active project owned by ownerID.
func (e *channelPermEnv) seedProject(t *testing.T, ownerID string) string {
	t.Helper()
	id := uuid.Must(uuid.NewV7()).String()
	clean := "cp" + id[:8] + id[9:13]
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

// seedAgentWithOwner inserts an agent row owned by ownerID and returns the agent UUID.
func (e *channelPermEnv) seedAgentWithOwner(t *testing.T, ownerID string) string {
	t.Helper()
	id := uuid.Must(uuid.NewV7()).String()
	key := "agt-" + id[:8]
	_, err := e.db.ExecContext(e.ctx,
		`INSERT INTO agents (id, agent_key, display_name, frontmatter, owner_id, owner_user_id, provider, model, metadata)
		 VALUES (?, ?, ?, '', ?, ?, 'test', 'test-model', '{}')`,
		id, key, key, ownerID, ownerID,
	)
	if err != nil {
		t.Fatalf("seedAgent: %v", err)
	}
	return id
}

// seedChannelInstance inserts a channel instance bound to agentID.
// Returns the instance name.
func (e *channelPermEnv) seedChannelInstance(t *testing.T, agentID, createdBy string) string {
	t.Helper()
	agentUUID := uuid.Must(uuid.Parse(agentID))
	name := "telegram-" + agentID[:8]
	inst := &store.ChannelInstanceData{
		Name:        name,
		DisplayName: name,
		ChannelType: "telegram",
		AgentID:     agentUUID,
		CreatedBy:   createdBy,
	}
	if err := e.instances.Create(e.ctx, inst); err != nil {
		t.Fatalf("seedChannelInstance: %v", err)
	}
	return name
}

// seedGroupContact inserts a group-type channel contact with an optional channel_instance binding.
func (e *channelPermEnv) seedGroupContact(t *testing.T, instanceName string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	sender := "grp-" + id.String()[:8]
	inst := instanceName
	if inst == "" {
		inst = ""
	}
	_, err := e.db.ExecContext(e.ctx,
		`INSERT INTO channel_contacts (id, channel_type, channel_instance, sender_id, contact_type)
		 VALUES (?, 'telegram', NULLIF(?,?), ?, 'group')`,
		id.String(), inst, "", sender,
	)
	if err != nil {
		t.Fatalf("seedGroupContact: %v", err)
	}
	return id
}

// addViewerGrant grants viewer access on projectID to userID.
func (e *channelPermEnv) addViewerGrant(t *testing.T, projectID, userID string) {
	t.Helper()
	g := &store.ProjectGrant{ProjectID: projectID, UserID: &userID, Role: "viewer"}
	if err := e.grants.Create(e.ctx, g); err != nil {
		t.Fatalf("addViewerGrant: %v", err)
	}
}

// ─── Scenario 1: Admin caller + project access → success ─────────────────────

func TestCanSetChannelDefaultProject_AdminWithAccess(t *testing.T) {
	e := newChannelPermEnv(t)
	ownerID := e.seedUser(t)
	callerID := e.seedUser(t)
	projectID := e.seedProject(t, ownerID)
	contactID := e.seedGroupContact(t, "")
	e.addViewerGrant(t, projectID, callerID)

	pid := uuid.Must(uuid.Parse(projectID))
	ok, err := permissions.CanSetChannelDefaultProject(
		e.ctx, e.deps(),
		permissions.RoleAdmin, callerID,
		contactID, &pid,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("admin with project access must be allowed")
	}
}

// ─── Scenario 2: Agent owner + project access → success ──────────────────────

func TestCanSetChannelDefaultProject_AgentOwnerWithAccess(t *testing.T) {
	e := newChannelPermEnv(t)
	callerID := e.seedUser(t)
	projectOwnerID := e.seedUser(t)
	agentID := e.seedAgentWithOwner(t, callerID)
	instanceName := e.seedChannelInstance(t, agentID, callerID)
	contactID := e.seedGroupContact(t, instanceName)
	projectID := e.seedProject(t, projectOwnerID)
	e.addViewerGrant(t, projectID, callerID)

	pid := uuid.Must(uuid.Parse(projectID))
	ok, err := permissions.CanSetChannelDefaultProject(
		e.ctx, e.deps(),
		permissions.RoleMember, callerID,
		contactID, &pid,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("agent owner with project access must be allowed")
	}
}

// ─── Scenario 3: Project owner → success ─────────────────────────────────────

func TestCanSetChannelDefaultProject_ProjectOwner(t *testing.T) {
	e := newChannelPermEnv(t)
	callerID := e.seedUser(t)
	projectID := e.seedProject(t, callerID) // callerID is the project owner
	contactID := e.seedGroupContact(t, "")

	pid := uuid.Must(uuid.Parse(projectID))
	ok, err := permissions.CanSetChannelDefaultProject(
		e.ctx, e.deps(),
		permissions.RoleMember, callerID,
		contactID, &pid,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("project owner must be allowed without an explicit grant")
	}
}

// TestCanSetChannelDefaultProject_OwnerOnly proves the owner path: no explicit
// project grant is needed — the resolver's owner leg (rank 3) is sufficient.
func TestCanSetChannelDefaultProject_OwnerOnly(t *testing.T) {
	e := newChannelPermEnv(t)
	callerID := e.seedUser(t)
	projectID := e.seedProject(t, callerID) // callerID is the owner, no grant added
	contactID := e.seedGroupContact(t, "")

	pid := uuid.Must(uuid.Parse(projectID))
	ok, err := permissions.CanSetChannelDefaultProject(
		e.ctx, e.deps(),
		permissions.RoleMember, callerID,
		contactID, &pid,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("owner without an explicit grant must still pass CanSetChannelDefaultProject")
	}
}

// ─── Scenario 4: Random user → reject ────────────────────────────────────────

func TestCanSetChannelDefaultProject_RandomUserDenied(t *testing.T) {
	e := newChannelPermEnv(t)
	ownerID := e.seedUser(t)
	callerID := e.seedUser(t) // no special relationship
	projectID := e.seedProject(t, ownerID)
	contactID := e.seedGroupContact(t, "")
	// callerID has no grant on the project

	pid := uuid.Must(uuid.Parse(projectID))
	ok, err := permissions.CanSetChannelDefaultProject(
		e.ctx, e.deps(),
		permissions.RoleMember, callerID,
		contactID, &pid,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("random user without any qualifying role must be denied")
	}
}

// ─── Scenario 5: Admin but no project read access → reject ───────────────────

func TestCanSetChannelDefaultProject_AdminNoProjectAccess(t *testing.T) {
	e := newChannelPermEnv(t)
	ownerID := e.seedUser(t)
	callerID := e.seedUser(t)
	projectID := e.seedProject(t, ownerID)
	contactID := e.seedGroupContact(t, "")
	// callerID is admin but has no grant on projectID and is not the owner

	pid := uuid.Must(uuid.Parse(projectID))
	ok, err := permissions.CanSetChannelDefaultProject(
		e.ctx, e.deps(),
		permissions.RoleAdmin, callerID,
		contactID, &pid,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("admin without project read access must be denied (blocks binding to unreachable project)")
	}
}

// ─── Scenario 6: Clear default (projectID=nil) — admin/agent-owner pass ──────

func TestCanSetChannelDefaultProject_ClearDefault_AdminPasses(t *testing.T) {
	e := newChannelPermEnv(t)
	callerID := e.seedUser(t)
	contactID := e.seedGroupContact(t, "")

	ok, err := permissions.CanSetChannelDefaultProject(
		e.ctx, e.deps(),
		permissions.RoleAdmin, callerID,
		contactID, nil, // clear — no project target
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("admin clearing default must be allowed (no project access check needed)")
	}
}

func TestCanSetChannelDefaultProject_ClearDefault_RandomUserDenied(t *testing.T) {
	e := newChannelPermEnv(t)
	callerID := e.seedUser(t)
	contactID := e.seedGroupContact(t, "")

	ok, err := permissions.CanSetChannelDefaultProject(
		e.ctx, e.deps(),
		permissions.RoleMember, callerID,
		contactID, nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("random member clearing default on unowned contact must be denied")
	}
}
