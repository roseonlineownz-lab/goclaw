//go:build integration

package integration

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/store/pg"
)

// adminCtx returns a context with admin role so agentOwnerFilter bypasses
// owner_user_id scoping (the seeded test agent has a string owner, not a
// UUID, so non-admin queries hit ErrNotFound).
func adminCtx() context.Context {
	return store.WithRole(context.Background(), "admin")
}

// seedAgentForImmutableKeyTest creates an agent row with display_name set
// (scanAgentRow rejects NULL into string). Returns the agent UUID.
func seedAgentForImmutableKeyTest(t *testing.T, db *sql.DB) uuid.UUID {
	t.Helper()
	id := uuid.New()
	key := "imm-" + id.String()[:8]
	_, err := db.Exec(
		`INSERT INTO agents (id, agent_key, display_name, status, provider, model, owner_id)
		 VALUES ($1, $2, $3, 'active', 'test', 'test-model', 'test-owner')`,
		id, key, "Original Display "+key)
	if err != nil {
		t.Fatalf("seedAgentForImmutableKeyTest: %v", err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM agents WHERE id = $1", id) })
	return id
}

// TestAgentStore_KeyImmutable verifies the agent_key strip in
// PGAgentStore.Update. agent_key couples to the FS workspace path and the
// router cache; renames would orphan workspace dirs and break cache
// invalidation. Mirrors team_key immutability (already covered by test
// fixtures elsewhere) and project slug immutability (covered separately).
func TestAgentStore_KeyImmutable(t *testing.T) {
	db := testDB(t)
	agentID := seedAgentForImmutableKeyTest(t, db)

	as := pg.NewPGAgentStore(db)

	// Capture original key.
	originalAgent, err := as.GetByIDUnscoped(adminCtx(), agentID)
	if err != nil {
		t.Fatalf("GetByIDUnscoped: %v", err)
	}
	originalKey := originalAgent.AgentKey

	// Attempt rename via Update map. Must be silently ignored.
	err = as.Update(adminCtx(), agentID, map[string]any{
		"agent_key": "renamed-key",
	})
	if err != nil {
		t.Fatalf("Update with agent_key only: %v", err)
	}

	// Re-read; key must be unchanged.
	after, err := as.GetByIDUnscoped(adminCtx(), agentID)
	if err != nil {
		t.Fatalf("GetByIDUnscoped after: %v", err)
	}
	if after.AgentKey != originalKey {
		t.Errorf("agent_key mutated: got %q, want %q", after.AgentKey, originalKey)
	}
}

// TestAgentStore_KeyStrippedAlongsideOtherUpdates verifies that even when
// agent_key is included alongside legitimate fields, the key is stripped
// while the other fields apply. Critical: caller must not be able to bury
// a key rename inside a larger PATCH.
func TestAgentStore_KeyStrippedAlongsideOtherUpdates(t *testing.T) {
	db := testDB(t)
	agentID := seedAgentForImmutableKeyTest(t, db)

	as := pg.NewPGAgentStore(db)
	original, _ := as.GetByIDUnscoped(adminCtx(), agentID)

	err := as.Update(adminCtx(), agentID, map[string]any{
		"agent_key":    "should-be-stripped",
		"display_name": "New Display Name",
	})
	if err != nil {
		t.Fatalf("Update mixed: %v", err)
	}

	after, _ := as.GetByIDUnscoped(adminCtx(), agentID)
	if after.AgentKey != original.AgentKey {
		t.Errorf("agent_key leaked through: got %q, want %q", after.AgentKey, original.AgentKey)
	}
	if after.DisplayName != "New Display Name" {
		t.Errorf("legitimate update dropped: display_name=%q", after.DisplayName)
	}
}

// TestProjectStore_SlugImmutableThroughAPI verifies that the ProjectStore
// API surface does not expose any path to mutate slug. UpdateStatus and
// UpdateMetadata are the only writers; neither touches slug. This test
// exercises both, then confirms slug is unchanged.
func TestProjectStore_SlugImmutableThroughAPI(t *testing.T) {
	db := testDB(t)
	ownerID := seedUserForShares(t, db)

	// Seed a project directly.
	id := uuid.New()
	slug := "immut-" + id.String()[:8]
	_, err := db.Exec(
		`INSERT INTO projects (id, owner_user_id, slug, status)
		 VALUES ($1, $2, $3, 'active')`,
		id, ownerID, slug,
	)
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM projects WHERE id = $1", id) })

	ps := pg.NewPGProjectStore(db)

	// Exercise both writers — slug must not change.
	if err := ps.UpdateStatus(adminCtx(), id, "archived"); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	if err := ps.UpdateMetadata(adminCtx(), id, []byte(`{"foo":"bar"}`)); err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}

	got, err := ps.Get(adminCtx(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Slug != slug {
		t.Errorf("slug mutated through API surface: got %q, want %q", got.Slug, slug)
	}
}
