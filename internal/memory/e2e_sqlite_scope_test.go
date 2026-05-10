//go:build sqlite || sqliteonly

package memory_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/store/sqlitestore"
)

// openSQLiteTestDB creates an in-memory SQLite DB with the full schema applied.
func openSQLiteTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=on")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := sqlitestore.EnsureSchema(db); err != nil {
		db.Close()
		t.Fatalf("ensure schema: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// seedSQLiteAgent inserts a minimal agent row required by FK constraints.
func seedSQLiteAgent(t *testing.T, db *sql.DB) uuid.UUID {
	t.Helper()
	agentID := uuid.Must(uuid.NewV7())
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO agents (id, agent_key, status, provider, model, owner_id)
		VALUES (?,?,?,?,?,?)`,
		agentID.String(), "test-agent-"+agentID.String()[:8], "active", "test", "test-model", "owner"); err != nil {
		t.Skipf("seed agent: %v", err)
	}
	return agentID
}

// newTestEpisodic creates a minimal EpisodicSummary for the given agentID.
func newTestEpisodic(agentID uuid.UUID, userID, sessionKey, sourceID string) *store.EpisodicSummary {
	exp := time.Now().UTC().Add(90 * 24 * time.Hour)
	return &store.EpisodicSummary{
		AgentID:    agentID,
		UserID:     userID,
		SessionKey: sessionKey,
		Summary:    "Test summary for " + sessionKey,
		L0Abstract: "L0 abstract",
		KeyTopics:  []string{"topic1"},
		SourceID:   sourceID,
		SourceType: "session",
		TurnCount:  3,
		TokenCount: 100,
		ExpiresAt:  &exp,
	}
}

// TestSQLiteEpisodicSummary5DScopeRoundTrip verifies that SQLiteEpisodicStore.Create
// stores and retrieves episodic summaries correctly.
func TestSQLiteEpisodicSummary5DScopeRoundTrip(t *testing.T) {
	db := openSQLiteTestDB(t)
	ctx := context.Background()
	agentID := seedSQLiteAgent(t, db)

	epStore := sqlitestore.NewSQLiteEpisodicStore(db)

	ep := newTestEpisodic(agentID, "user-alice-001", "sess-roundtrip", "src-roundtrip-1")
	if err := epStore.Create(ctx, ep); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if ep.ID == (uuid.UUID{}) {
		t.Errorf("expected non-zero ID after Create")
	}

	got, err := epStore.Get(ctx, ep.ID.String())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatalf("Get returned nil for id %s", ep.ID)
	}
	if got.Summary != ep.Summary {
		t.Errorf("Summary: got %q, want %q", got.Summary, ep.Summary)
	}
	if got.SessionKey != ep.SessionKey {
		t.Errorf("SessionKey: got %q, want %q", got.SessionKey, ep.SessionKey)
	}
	if got.UserID != ep.UserID {
		t.Errorf("UserID: got %q, want %q", got.UserID, ep.UserID)
	}
}

// TestSQLiteEpisodicSummaryL31PrivacyIsolation verifies that user-private episodic
// summaries (user_id=alice) are NOT returned in queries for user_id=bob.
func TestSQLiteEpisodicSummaryL31PrivacyIsolation(t *testing.T) {
	db := openSQLiteTestDB(t)
	ctx := context.Background()
	agentID := seedSQLiteAgent(t, db)

	epStore := sqlitestore.NewSQLiteEpisodicStore(db)

	aliceID := "user-alice-l31-0000001"
	bobID := "user-bob-l31-00000001"

	aliceEp := newTestEpisodic(agentID, aliceID, "sess-alice-l31", "src-alice-l31")
	if err := epStore.Create(ctx, aliceEp); err != nil {
		t.Fatalf("alice Create: %v", err)
	}

	// Bob queries his zone — must see ZERO rows.
	bobRows, err := epStore.List(ctx, agentID.String(), bobID, 20, 0)
	if err != nil {
		t.Fatalf("bob List: %v", err)
	}
	if len(bobRows) != 0 {
		t.Errorf("L31 violation: bob saw %d rows that belong to alice", len(bobRows))
	}

	// Alice queries her zone — must see her row.
	aliceRows, err := epStore.List(ctx, agentID.String(), aliceID, 20, 0)
	if err != nil {
		t.Fatalf("alice List: %v", err)
	}
	if len(aliceRows) != 1 {
		t.Errorf("alice should see her own row: got %d rows", len(aliceRows))
	}
}

// TestSQLiteEpisodicDedup5D verifies that the 5D-aware dedup index in SQLite
// prevents exact duplicate (same scope + source_id) while allowing same source_id
// under different scope.
func TestSQLiteEpisodicDedup5D(t *testing.T) {
	db := openSQLiteTestDB(t)
	ctx := context.Background()
	agentID := seedSQLiteAgent(t, db)

	epStore := sqlitestore.NewSQLiteEpisodicStore(db)

	const sourceID = "dedup-source-5d"
	aliceID := "user-alice-dedup-001"
	bobID := "user-bob-dedup-0001"

	// Row 1: alice's scope + source_id.
	ep1 := newTestEpisodic(agentID, aliceID, "sess-dedup-alice", sourceID)
	if err := epStore.Create(ctx, ep1); err != nil {
		t.Fatalf("insert ep1: %v", err)
	}

	// Row 2: bob's scope + same source_id → different scope = different dedup key → must succeed.
	ep2 := newTestEpisodic(agentID, bobID, "sess-dedup-bob", sourceID)
	if err := epStore.Create(ctx, ep2); err != nil {
		t.Fatalf("insert ep2 (different user, same source_id): %v", err)
	}

	// Row 3: exact duplicate of row 1 (same alice scope + same source_id) → ON CONFLICT DO NOTHING.
	ep3 := newTestEpisodic(agentID, aliceID, "sess-dedup-alice-dup", sourceID)
	if err := epStore.Create(ctx, ep3); err != nil {
		t.Fatalf("insert ep3 (duplicate alice): %v", err)
	}

	// Alice should have exactly 1 row (ep3 was silently deduped).
	aliceRows, err := epStore.List(ctx, agentID.String(), aliceID, 20, 0)
	if err != nil {
		t.Fatalf("alice List: %v", err)
	}
	if len(aliceRows) != 1 {
		t.Errorf("expected 1 alice row (dedup), got %d", len(aliceRows))
	}

	// Bob should have exactly 1 row (independent scope).
	bobRows, err := epStore.List(ctx, agentID.String(), bobID, 20, 0)
	if err != nil {
		t.Fatalf("bob List: %v", err)
	}
	if len(bobRows) != 1 {
		t.Errorf("expected 1 bob row, got %d", len(bobRows))
	}
}
