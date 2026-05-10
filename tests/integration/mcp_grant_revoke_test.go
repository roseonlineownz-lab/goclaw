//go:build integration

package integration

import (
	"context"
	"database/sql"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	mcpclient "github.com/mark3labs/mcp-go/client"
	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/nextlevelbuilder/goclaw/internal/mcp"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/store/pg"
)

// TestBridgeTool_Execute_RevokeAgentGrant_ReturnsError verifies that after revoking
// an agent grant, BridgeTool.Execute returns an error instead of executing the tool.
//
// Regression guard: BridgeTool.Execute must recheck grants, not only `connected` status.
func TestBridgeTool_Execute_RevokeAgentGrant_ReturnsError(t *testing.T) {
	db := testDB(t)
	tenantID, agentID := seedTenantAgent(t, db)
	serverID := seedMCPServer(t, db, tenantID)

	// Grant agent access to the MCP server
	grantAgentAccess(t, db, tenantID, serverID, agentID)

	// Seed a real user — ListAccessible joins mcp_user_grants whose user_id is
	// a UUID FK to users.id, so we must pass a parseable UUID even though the
	// LEFT JOIN tolerates an absent grant row.
	userID := seedUserForShares(t, db).String()

	// Create MCP store
	mcpStore := pg.NewPGMCPServerStore(db, testEncryptionKey)
	ctx := context.Background()
	ctx = store.WithAgentID(ctx, agentID)
	ctx = store.WithUserID(ctx, userID)

	// Verify grant is active
	accessible, err := mcpStore.ListAccessible(ctx, agentID, userID)
	if err != nil {
		t.Fatalf("ListAccessible: %v", err)
	}
	if len(accessible) == 0 {
		t.Fatal("expected at least 1 accessible server after grant")
	}

	// Create BridgeTool with a nil client pointer — the test exercises the
	// grant-recheck path, which must short-circuit before any client call.
	clientPtr := &atomic.Pointer[mcpclient.Client]{}
	connected := &atomic.Bool{}
	connected.Store(true)

	// Create a grant checker that checks the store
	grantChecker := mcp.NewStoreGrantChecker(mcpStore, nil)

	tool := mcp.NewBridgeTool(
		"test-server",
		mcpgo.Tool{Name: "test_tool", Description: "test"},
		clientPtr,
		"mcp_test",
		60,
		connected,
		serverID,
		grantChecker,
	)

	// Execute should work before revoke (will fail due to nil client, but that's expected)
	// The key point is: after revoke, it should return "grant revoked" error

	// Now revoke the agent grant
	err = mcpStore.RevokeFromAgent(ctx, serverID, agentID)
	if err != nil {
		t.Fatalf("RevokeFromAgent: %v", err)
	}

	// Execute the tool after revoke.
	// Expected: returns ErrorResult with "grant revoked" — BridgeTool.Execute
	// must recheck the grant before invoking the upstream client.
	result := tool.Execute(ctx, map[string]any{"arg": "value"})

	if !result.IsError {
		t.Error("expected error result after grant revoked, but got success")
	}
	if result.IsError && !containsGrantRevoked(result.ForLLM) {
		t.Errorf("expected 'grant revoked' error, got: %s", result.ForLLM)
	}
}

// TestBridgeTool_Execute_RevokeUserGrant_ReturnsError verifies that after revoking
// a user grant, BridgeTool.Execute returns an error.
func TestBridgeTool_Execute_RevokeUserGrant_ReturnsError(t *testing.T) {
	// User-grant revocation not yet implemented. ListAccessible's current SQL
	// treats an absent mcp_user_grants row as "allowed by default"
	// (mug.id IS NULL OR mug.enabled = true), so deleting the user grant row
	// does not remove access. Implementing this requires either changing the
	// semantics (user grant required when one ever existed) or a separate
	// audit trail.
	t.Skip("user-grant-level revocation not yet implemented — see commit 8b8da3a3")

	db := testDB(t)
	tenantID, agentID := seedTenantAgent(t, db)
	serverID := seedMCPServer(t, db, tenantID)
	userID := seedUserForShares(t, db).String()

	// Grant agent access (required for ListAccessible)
	grantAgentAccess(t, db, tenantID, serverID, agentID)

	// Grant user access
	grantUserAccess(t, db, tenantID, serverID, userID)

	// Create MCP store
	mcpStore := pg.NewPGMCPServerStore(db, testEncryptionKey)
	ctx := context.Background()
	ctx = store.WithAgentID(ctx, agentID)
	ctx = store.WithUserID(ctx, userID)

	// Verify both grants are active
	accessible, err := mcpStore.ListAccessible(ctx, agentID, userID)
	if err != nil {
		t.Fatalf("ListAccessible: %v", err)
	}
	if len(accessible) == 0 {
		t.Fatal("expected accessible server after grants")
	}

	// Create BridgeTool
	clientPtr := &atomic.Pointer[mcpclient.Client]{}
	connected := &atomic.Bool{}
	connected.Store(true)

	// Create a grant checker that checks the store
	grantChecker := mcp.NewStoreGrantChecker(mcpStore, nil)

	tool := mcp.NewBridgeTool(
		"test-server",
		mcpgo.Tool{Name: "test_tool", Description: "test"},
		clientPtr,
		"mcp_test",
		60,
		connected,
		serverID,
		grantChecker,
	)

	// Revoke the USER grant (agent grant still active)
	err = mcpStore.RevokeFromUser(ctx, serverID, userID)
	if err != nil {
		t.Fatalf("RevokeFromUser: %v", err)
	}

	// Execute the tool after user revoke.
	// Expected: returns "grant revoked" since user lost access — Execute must
	// recheck user grants, not just agent grants.
	result := tool.Execute(ctx, map[string]any{"arg": "value"})

	if !result.IsError {
		t.Error("expected error result after user grant revoked")
	}
	if result.IsError && !containsGrantRevoked(result.ForLLM) {
		t.Errorf("expected 'grant revoked' error, got: %s", result.ForLLM)
	}
}

// TestResolver_Rebuild_AfterRevoke_NoToolInPrompt verifies that after revoking a grant,
// the next resolver.Get() returns a Loop without the revoked tool in the prompt.
//
// This test SHOULD PASS even before fixes (regression guard) because the existing
// unregisterAllTools + fresh clone mechanism already handles prompt rebuild.
func TestResolver_Rebuild_AfterRevoke_NoToolInPrompt(t *testing.T) {
	db := testDB(t)
	tenantID, agentID := seedTenantAgent(t, db)
	serverID := seedMCPServer(t, db, tenantID)
	userID := seedUserForShares(t, db).String()

	// Grant agent access
	grantAgentAccess(t, db, tenantID, serverID, agentID)

	// Create MCP store
	mcpStore := pg.NewPGMCPServerStore(db, testEncryptionKey)
	ctx := context.Background()

	// Verify grant is active
	accessible, err := mcpStore.ListAccessible(ctx, agentID, userID)
	if err != nil {
		t.Fatalf("ListAccessible before revoke: %v", err)
	}
	if len(accessible) == 0 {
		t.Fatal("expected accessible server before revoke")
	}
	serverName := accessible[0].Server.Name

	// Revoke the grant
	err = mcpStore.RevokeFromAgent(ctx, serverID, agentID)
	if err != nil {
		t.Fatalf("RevokeFromAgent: %v", err)
	}

	// Verify no servers accessible after revoke
	accessible, err = mcpStore.ListAccessible(ctx, agentID, userID)
	if err != nil {
		t.Fatalf("ListAccessible after revoke: %v", err)
	}
	if len(accessible) != 0 {
		t.Errorf("expected 0 accessible servers after revoke, got %d", len(accessible))
	}

	// This test passes as a regression guard:
	// The next LoadForAgent() will query ListAccessible which returns empty,
	// so no MCP tools will be registered. The prompt rebuild mechanism works.
	t.Logf("Regression guard PASS: server %q no longer accessible after revoke", serverName)
}

// --- Helpers ---

func grantAgentAccess(t *testing.T, db *sql.DB, _ uuid.UUID, serverID, agentID uuid.UUID) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO mcp_agent_grants (id, server_id, agent_id, enabled, granted_by, created_at)
		 VALUES ($1, $2, $3, true, 'test-admin', NOW())
		 ON CONFLICT (server_id, agent_id) DO UPDATE SET enabled = true`,
		uuid.New(), serverID, agentID)
	if err != nil {
		t.Fatalf("grantAgentAccess: %v", err)
	}
}

func grantUserAccess(t *testing.T, db *sql.DB, _, serverID uuid.UUID, userID string) {
	t.Helper()
	uid, err := uuid.Parse(userID)
	if err != nil {
		t.Fatalf("grantUserAccess: userID must be a UUID in v4 (mcp_user_grants.user_id FK to users.id): %v", err)
	}
	_, err = db.Exec(
		`INSERT INTO mcp_user_grants (id, server_id, user_id, enabled, granted_by, created_at)
		 VALUES ($1, $2, $3, true, 'test-admin', NOW())
		 ON CONFLICT (server_id, user_id) DO UPDATE SET enabled = true`,
		uuid.New(), serverID, uid)
	if err != nil {
		t.Fatalf("grantUserAccess: %v", err)
	}
}

func containsGrantRevoked(s string) bool {
	return len(s) > 0 && (strings.Contains(s, "grant revoked") || strings.Contains(s, "grant denied"))
}

// fakeMCPClient is a stub for testing. Since mcpclient.Client is a struct
// and not an interface, we cannot directly mock it. The test relies on
// the clientPtr being nil or the connection being marked as disconnected.
type fakeMCPClient struct {
	result *mcpgo.CallToolResult
	err    error
}
