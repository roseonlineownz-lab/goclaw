//go:build integration

package integration

import (
	"database/sql"
	"testing"

	"github.com/google/uuid"
)

// seedTwoTenants is retained by name for caller compatibility. v4 has no
// tenant concept — this seeds two independent agent rows and returns a
// pair of throwaway scope UUIDs alongside their agent IDs.
func seedTwoTenants(t *testing.T, db *sql.DB) (scopeA, scopeB, agentA, agentB uuid.UUID) {
	t.Helper()
	scopeA, agentA = seedTenantAgent(t, db)
	scopeB, agentB = seedTenantAgent(t, db)
	return
}

// seedTeam creates an active team with v2 settings (required for recovery
// queries). The owner agent becomes the lead; a second member agent is
// seeded and added. Returns the team ID and the second agent's ID. The
// first parameter is retained for caller signature compatibility but is
// no longer used (v4 dropped the legacy tenant scope column on agent_teams).
func seedTeam(t *testing.T, db *sql.DB, _ uuid.UUID, ownerAgentID uuid.UUID) (teamID, memberAgentID uuid.UUID) {
	t.Helper()

	teamID = uuid.New()
	memberAgentID = uuid.New()
	memberKey := "member-" + memberAgentID.String()[:8]

	_, err := db.Exec(
		`INSERT INTO agents (id, agent_key, status, provider, model, owner_id)
		 VALUES ($1, $2, 'active', 'test', 'test-model', 'test-owner')
		 ON CONFLICT DO NOTHING`,
		memberAgentID, memberKey)
	if err != nil {
		t.Fatalf("seed member agent: %v", err)
	}

	_, err = db.Exec(
		`INSERT INTO agent_teams (id, name, lead_agent_id, status, settings, created_by, team_key)
		 VALUES ($1, $2, $3, 'active', '{"version": 2}', 'test', $4)`,
		teamID, "test-team-"+teamID.String()[:8], ownerAgentID, "team-"+teamID.String()[:8])
	if err != nil {
		t.Fatalf("seed team: %v", err)
	}

	for _, m := range []struct {
		agentID uuid.UUID
		role    string
	}{
		{ownerAgentID, "lead"},
		{memberAgentID, "member"},
	} {
		_, err = db.Exec(
			`INSERT INTO agent_team_members (team_id, agent_id, role)
			 VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
			teamID, m.agentID, m.role)
		if err != nil {
			t.Fatalf("seed team member: %v", err)
		}
	}

	t.Cleanup(func() {
		db.Exec("DELETE FROM agent_team_members WHERE team_id = $1", teamID)
		db.Exec("DELETE FROM agent_teams WHERE id = $1", teamID)
		db.Exec("DELETE FROM agents WHERE id = $1", memberAgentID)
	})

	return teamID, memberAgentID
}

// seedSession creates a minimal agent_sessions record. The first uuid
// parameter is unused in v4 (no tenant scope); kept for signature
// compatibility.
func seedSession(t *testing.T, db *sql.DB, _ uuid.UUID, agentID uuid.UUID) string {
	t.Helper()

	sessionKey := "sess-" + uuid.New().String()[:8]
	_, err := db.Exec(
		`INSERT INTO agent_sessions (session_key, agent_id, messages, summary)
		 VALUES ($1, $2, '[]', '')`,
		sessionKey, agentID)
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}

	t.Cleanup(func() {
		db.Exec("DELETE FROM agent_sessions WHERE session_key = $1", sessionKey)
	})

	return sessionKey
}

// seedMCPServer creates a minimal MCP server record. The uuid parameter
// is unused in v4 (no tenant scope); kept for signature compatibility.
func seedMCPServer(t *testing.T, db *sql.DB, _ uuid.UUID) uuid.UUID {
	t.Helper()

	serverID := uuid.New()
	name := "test-mcp-" + serverID.String()[:8]
	_, err := db.Exec(
		`INSERT INTO mcp_servers (id, name, display_name, transport, enabled, created_by)
		 VALUES ($1, $2, $2, 'stdio', true, 'test-user')`,
		serverID, name)
	if err != nil {
		t.Fatalf("seed mcp server: %v", err)
	}

	t.Cleanup(func() {
		db.Exec("DELETE FROM mcp_user_credentials WHERE server_id = $1", serverID)
		db.Exec("DELETE FROM mcp_access_requests WHERE server_id = $1", serverID)
		db.Exec("DELETE FROM mcp_user_grants WHERE server_id = $1", serverID)
		db.Exec("DELETE FROM mcp_agent_grants WHERE server_id = $1", serverID)
		db.Exec("DELETE FROM mcp_servers WHERE id = $1", serverID)
	})

	return serverID
}

// seedSecureCLI creates a minimal secure CLI binary record. The uuid
// parameter is unused in v4 (no tenant scope); kept for signature
// compatibility. Uses a dummy encrypted_env payload.
func seedSecureCLI(t *testing.T, db *sql.DB, _ uuid.UUID) uuid.UUID {
	t.Helper()

	binaryID := uuid.New()
	name := "test-cli-" + binaryID.String()[:8]
	dummyEnv := []byte(`{"TEST_KEY": "test_value"}`)

	_, err := db.Exec(
		`INSERT INTO secure_cli_binaries (id, binary_name, encrypted_env, description, enabled)
		 VALUES ($1, $2, $3, 'test CLI', true)`,
		binaryID, name, dummyEnv)
	if err != nil {
		t.Fatalf("seed secure cli: %v", err)
	}

	t.Cleanup(func() {
		db.Exec("DELETE FROM secure_cli_user_credentials WHERE binary_id = $1", binaryID)
		db.Exec("DELETE FROM secure_cli_agent_grants WHERE binary_id = $1", binaryID)
		db.Exec("DELETE FROM secure_cli_binaries WHERE id = $1", binaryID)
	})

	return binaryID
}
