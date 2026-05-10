//go:build integration

package integration

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/store/pg"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

// gateTestBinaryNamePrefix is deliberately NOT a real binary on PATH so the
// "allowed" path can never accidentally exec something real. v4 dropped
// tenant_id from the unique index on secure_cli_binaries, so each parallel
// test must use a unique suffix to avoid colliding on (binary_name).
const gateTestBinaryNamePrefix = "goclaw_test_cli"

// gateTestBinaryName returns a per-test-unique binary name so concurrent
// `t.Parallel()` runs don't violate idx_secure_cli_unique_binary.
func gateTestBinaryName(t *testing.T) string {
	t.Helper()
	return gateTestBinaryNamePrefix + "_" + uuid.New().String()[:8]
}

// gateFixture holds the common seeds for gate enforcement tests.
type gateFixture struct {
	db         *sql.DB
	tool       *tools.ExecTool
	tenantID   uuid.UUID
	agentA     uuid.UUID
	agentB     uuid.UUID
	binaryID   uuid.UUID
	binaryName string // unique per test to avoid colliding on the binary_name unique index
}

// setupGateTest seeds: two agents, a non-global registered binary with a
// per-test-unique name, and a grant only for agentA. Returns a wired ExecTool
// + the IDs. All rows are cleaned up via t.Cleanup.
func setupGateTest(t *testing.T) *gateFixture {
	t.Helper()

	db := testDB(t)
	tenantID, agentA := seedTenantAgent(t, db)
	agentB := seedSecondAgent(t, db, tenantID)
	binaryName := gateTestBinaryName(t)
	binaryID := seedGateBinary(t, db, tenantID, binaryName, false)
	seedGrant(t, db, tenantID, binaryID, agentA)

	secStore := pg.NewPGSecureCLIStore(db, testEncryptionKey)
	tool := tools.NewExecTool(t.TempDir(), false)
	tool.SetSecureCLIStore(secStore)

	return &gateFixture{
		db:         db,
		tool:       tool,
		tenantID:   tenantID,
		agentA:     agentA,
		agentB:     agentB,
		binaryID:   binaryID,
		binaryName: binaryName,
	}
}

// seedSecondAgent inserts an additional agent under an existing tenant. Both
// seedSecondAgent inserts a second agent so the gate (not tenant isolation)
// is what denies ungranted exec. v4 is single-tenant — no tenant_id column.
func seedSecondAgent(t *testing.T, db *sql.DB, _ uuid.UUID) uuid.UUID {
	t.Helper()

	agentID := uuid.New()
	agentKey := "test-" + agentID.String()[:8]

	_, err := db.Exec(
		`INSERT INTO agents (id, agent_key, status, provider, model, owner_id)
		 VALUES ($1, $2, 'active', 'test', 'test-model', 'test-owner')`,
		agentID, agentKey)
	if err != nil {
		t.Fatalf("seed second agent: %v", err)
	}

	t.Cleanup(func() {
		db.Exec("DELETE FROM agents WHERE id = $1", agentID)
	})

	return agentID
}

// seedGateBinary inserts a secure_cli_binaries row with a caller-specified
// binary_name and is_global flag. Matches shape used by seedSecureCLI.
func seedGateBinary(t *testing.T, db *sql.DB, _ uuid.UUID, name string, isGlobal bool) uuid.UUID {
	t.Helper()

	binaryID := uuid.New()
	_, err := db.Exec(
		`INSERT INTO secure_cli_binaries (id, binary_name, encrypted_env, description, enabled, is_global)
		 VALUES ($1, $2, $3, 'gate test CLI', true, $4)`,
		binaryID, name, []byte(`{}`), isGlobal)
	if err != nil {
		t.Fatalf("seed gate binary: %v", err)
	}

	t.Cleanup(func() {
		db.Exec("DELETE FROM secure_cli_agent_grants WHERE binary_id = $1", binaryID)
		db.Exec("DELETE FROM secure_cli_user_credentials WHERE binary_id = $1", binaryID)
		db.Exec("DELETE FROM secure_cli_binaries WHERE id = $1", binaryID)
	})

	return binaryID
}

// seedGrant inserts a secure_cli_agent_grants row tying an agent to a binary.
func seedGrant(t *testing.T, db *sql.DB, _, binaryID, agentID uuid.UUID) {
	t.Helper()

	_, err := db.Exec(
		`INSERT INTO secure_cli_agent_grants (binary_id, agent_id, enabled)
		 VALUES ($1, $2, true)`,
		binaryID, agentID)
	if err != nil {
		t.Fatalf("seed grant: %v", err)
	}
}

// gateCtx builds a ctx with tenant + agent set for gate enforcement.
func gateCtx(tenantID, agentID uuid.UUID) context.Context {
	ctx := context.Background()
	return store.WithAgentID(ctx, agentID)
}

// TestSecureCLIGate_DeniesUngranted proves the gate denies ungranted agents
// for a registered, non-global binary (FR2 — the primary fix).
func TestSecureCLIGate_DeniesUngranted(t *testing.T) {
	t.Parallel()

	f := setupGateTest(t)
	ctx := gateCtx(f.tenantID, f.agentB)

	result := f.tool.Execute(ctx, map[string]any{
		"command": f.binaryName + " --help",
	})

	if !result.IsError {
		t.Fatalf("expected IsError=true for ungranted exec, got: %+v", result)
	}
	if !strings.Contains(result.ForLLM, "requires a secure CLI grant") {
		t.Fatalf("expected deny message, got: %s", result.ForLLM)
	}
}

// TestSecureCLIGate_AllowsGrantedAgent proves the gate permits a granted
// agent past the deny branch (FR3). The downstream credentialed exec may
// still fail because the binary is not on PATH — we assert only that the
// deny message is NOT returned.
func TestSecureCLIGate_AllowsGrantedAgent(t *testing.T) {
	t.Parallel()

	f := setupGateTest(t)
	ctx := gateCtx(f.tenantID, f.agentA)

	result := f.tool.Execute(ctx, map[string]any{
		"command": f.binaryName + " --help",
	})

	if strings.Contains(result.ForLLM, "requires a secure CLI grant") {
		t.Fatalf("granted agent unexpectedly denied: %s", result.ForLLM)
	}
}

// TestSecureCLIGate_UnregisteredBinaryUnchanged proves the gate is a no-op
// for binaries not in the registry (FR4). `echo` must run normally.
func TestSecureCLIGate_UnregisteredBinaryUnchanged(t *testing.T) {
	t.Parallel()

	db := testDB(t)
	tenantID, agentID := seedTenantAgent(t, db)

	secStore := pg.NewPGSecureCLIStore(db, testEncryptionKey)
	tool := tools.NewExecTool(t.TempDir(), false)
	tool.SetSecureCLIStore(secStore)

	ctx := gateCtx(tenantID, agentID)
	result := tool.Execute(ctx, map[string]any{
		"command": "echo hello",
	})

	if result.IsError {
		t.Fatalf("expected no error for unregistered binary, got: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "hello") {
		t.Fatalf("expected output to contain 'hello', got: %s", result.ForLLM)
	}
}

// TestSecureCLIGate_IsGlobalBinaryNotDenied — global binary regression guard.
// A global binary (is_global=true) needs no grant; the gate must NOT deny.
func TestSecureCLIGate_IsGlobalBinaryNotDenied(t *testing.T) {
	t.Parallel()

	db := testDB(t)
	tenantID, agentID := seedTenantAgent(t, db)
	globalName := "goclaw_global_test_" + uuid.New().String()[:8]
	seedGateBinary(t, db, tenantID, globalName, true)

	secStore := pg.NewPGSecureCLIStore(db, testEncryptionKey)
	tool := tools.NewExecTool(t.TempDir(), false)
	tool.SetSecureCLIStore(secStore)

	ctx := gateCtx(tenantID, agentID)
	result := tool.Execute(ctx, map[string]any{
		"command": globalName + " --help",
	})

	if strings.Contains(result.ForLLM, "requires a secure CLI grant") {
		t.Fatalf("global binary unexpectedly denied by gate: %s", result.ForLLM)
	}
}

// TestSecureCLIGate_ShellWrapperBypassDenied — shell-wrapper bypass guard.
// Wrapping the registered binary in `sh -c '...'` must still hit the deny path.
func TestSecureCLIGate_ShellWrapperBypassDenied(t *testing.T) {
	t.Parallel()

	f := setupGateTest(t)
	ctx := gateCtx(f.tenantID, f.agentB)

	result := f.tool.Execute(ctx, map[string]any{
		"command": "sh -c '" + f.binaryName + " --help'",
	})

	if !result.IsError {
		t.Fatalf("expected IsError=true for wrapped exec, got: %+v", result)
	}
	if !strings.Contains(result.ForLLM, "requires a secure CLI grant") {
		t.Fatalf("expected deny message for sh -c wrap, got: %s", result.ForLLM)
	}
}
