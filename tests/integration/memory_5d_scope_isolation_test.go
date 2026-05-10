//go:build integration

package integration

// memory_5d_scope_isolation_test.go — regression guard for cross-team/cross-project
// memory leaks in EpisodicStore.Search.
//
// Before the fix, Search only filtered by agent_id+user_id. A query with
// team_id=B would return rows tagged team_id=A because the scope filter was
// absent from the SQL WHERE clause. These tests assert that the scope filter
// is both present and effective.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/store/pg"
)

// insertEpisodicWithTeam inserts an episodic_summaries row tagged with the
// given teamID. Returns the row ID.
func insertEpisodicWithTeam(t *testing.T, agentID uuid.UUID, userID string, teamID *uuid.UUID, summaryText string) string {
	t.Helper()
	db := testDB(t)
	ctx := context.Background()

	rowID := uuid.Must(uuid.NewV7())
	now := time.Now().UTC()
	expiresAt := now.Add(90 * 24 * time.Hour)
	sourceID := fmt.Sprintf("src-%s", rowID)

	_, err := db.ExecContext(ctx, `
		INSERT INTO episodic_summaries
			(id, agent_id, user_id, team_id,
			 session_key, summary, key_topics,
			 turn_count, token_count, l0_abstract,
			 source_id, source_type, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, '[]', 1, 100, $7, $8, 'session', $9, $10)
		ON CONFLICT DO NOTHING`,
		rowID, agentID, userID, teamID,
		"session-"+rowID.String()[:8], summaryText, summaryText,
		sourceID, now, expiresAt,
	)
	if err != nil {
		t.Fatalf("insertEpisodicWithTeam: %v", err)
	}
	t.Cleanup(func() {
		db.ExecContext(context.Background(), "DELETE FROM episodic_summaries WHERE id = $1", rowID)
	})
	return rowID.String()
}

// TestEpisodicSearch_TeamScopeFilter verifies that Search with team_id=A does
// not return rows tagged team_id=B (cross-team isolation).
func TestEpisodicSearch_TeamScopeFilter(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	_, agentID := seedTenantAgent(t, db)
	userID := seedUserForShares(t, db).String()

	teamA, _ := seedTeam(t, db, uuid.Nil, agentID)
	teamB, _ := seedTeam(t, db, uuid.Nil, agentID)

	// Insert row tagged to team A.
	rowAID := insertEpisodicWithTeam(t, agentID, userID, &teamA, "memory about project alpha")
	// Insert row tagged to team B.
	_ = insertEpisodicWithTeam(t, agentID, userID, &teamB, "memory about project beta")

	// Query with scope restricted to team A.
	epStore := pg.NewPGEpisodicStore(db)
	scope := &store.EpisodicScope{TeamID: &teamA}
	results, err := epStore.Search(ctx, "project", agentID.String(), userID,
		store.EpisodicSearchOptions{
			MaxResults: 10,
			Scope:      scope,
		})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	// All returned results must be the team-A row (rowAID), not the team-B row.
	for _, r := range results {
		if r.EpisodicID == rowAID {
			continue
		}
		// Any other row returned is a cross-team leak.
		t.Errorf("Search with team_id=A returned row %q which belongs to a different team (cross-team leak)", r.EpisodicID)
	}
}

// insertMemoryChunkWithTeam inserts a memory_documents + memory_chunks row tagged
// with the given teamID. user_id is stored as NULL (global chunk) to avoid FK
// constraints against the users table. Returns the chunk ID and the document path.
// The FTS tsv column is GENERATED ALWAYS — only raw text columns need to be inserted.
func insertMemoryChunkWithTeam(t *testing.T, agentID uuid.UUID, teamID *uuid.UUID, text string) (chunkID, docPath string) {
	t.Helper()
	db := testDB(t)
	ctx := context.Background()

	docID := uuid.Must(uuid.NewV7())
	chunkUUID := uuid.Must(uuid.NewV7())
	docPath = "memory/chunk-test-" + chunkUUID.String()[:8] + ".md"

	// Insert document first (FK required by memory_chunks). user_id=NULL so no users FK.
	_, err := db.ExecContext(ctx, `
		INSERT INTO memory_documents (id, agent_id, team_id, path, file_path, content_hash)
		VALUES ($1, $2, $3, $4, '', 'testhash')
		ON CONFLICT DO NOTHING`,
		docID, agentID, teamID, docPath,
	)
	if err != nil {
		t.Fatalf("insertMemoryChunkWithTeam: insert document: %v", err)
	}

	// Insert chunk. user_id=NULL (global), team_id scoped by teamID argument.
	_, err = db.ExecContext(ctx, `
		INSERT INTO memory_chunks (id, agent_id, document_id, team_id, path, start_line, end_line, hash, text)
		VALUES ($1, $2, $3, $4, $5, 0, 1, 'testhash', $6)
		ON CONFLICT DO NOTHING`,
		chunkUUID, agentID, docID, teamID, docPath, text,
	)
	if err != nil {
		t.Fatalf("insertMemoryChunkWithTeam: insert chunk: %v", err)
	}

	t.Cleanup(func() {
		db.ExecContext(context.Background(), "DELETE FROM memory_chunks WHERE id = $1", chunkUUID)
		db.ExecContext(context.Background(), "DELETE FROM memory_documents WHERE id = $1", docID)
	})
	return chunkUUID.String(), docPath
}

// TestMemoryChunks_TeamIsolation verifies that Search over memory_chunks with
// team_id=A does not return rows tagged team_id=B (cross-team isolation).
func TestMemoryChunks_TeamIsolation(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	_, agentID := seedTenantAgent(t, db)

	teamA, _ := seedTeam(t, db, uuid.Nil, agentID)
	teamB, _ := seedTeam(t, db, uuid.Nil, agentID)

	// Insert global chunks (user_id=NULL) tagged to team A and team B.
	insertMemoryChunkWithTeam(t, agentID, &teamA, "secret data for team alpha")
	insertMemoryChunkWithTeam(t, agentID, &teamB, "secret data for team beta")

	memStore := pg.NewPGMemoryStore(db, pg.DefaultPGMemoryConfig())

	// Search with scope restricted to team A. Empty userID = global (user_id IS NULL) path.
	results, err := memStore.Search(ctx, "secret data", agentID.String(), "",
		store.MemorySearchOptions{
			MaxResults: 10,
			Scope:      &store.MemoryScope{TeamID: &teamA},
		})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	// No result should contain "beta" (team B content).
	for _, r := range results {
		if snippetContains(r.Snippet, "beta") {
			t.Errorf("Search with team_id=A returned team-B chunk (cross-team leak): snippet=%q", r.Snippet)
		}
	}
}

// TestMemoryChunks_CrossTeamSearchReturnsNoRows verifies that searching with
// team_id=B when only team_id=A chunks exist returns zero results.
func TestMemoryChunks_CrossTeamSearchReturnsNoRows(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	_, agentID := seedTenantAgent(t, db)

	teamA, _ := seedTeam(t, db, uuid.Nil, agentID)
	teamB, _ := seedTeam(t, db, uuid.Nil, agentID) // no chunks exist for this team

	insertMemoryChunkWithTeam(t, agentID, &teamA, "exclusive alpha content")

	memStore := pg.NewPGMemoryStore(db, pg.DefaultPGMemoryConfig())

	// Query with team B scope — must return zero rows.
	results, err := memStore.Search(ctx, "exclusive alpha", agentID.String(), "",
		store.MemorySearchOptions{
			MaxResults: 10,
			Scope:      &store.MemoryScope{TeamID: &teamB},
		})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) > 0 {
		t.Errorf("cross-team chunk query returned %d result(s), want 0 — scope filter not applied", len(results))
	}
}

// snippetContains is a simple substring check for test assertions.
func snippetContains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestEpisodicSearch_CrossTeamQueryReturnsNoRows verifies that querying with
// team_id=B when only team_id=A rows exist returns zero results.
func TestEpisodicSearch_CrossTeamQueryReturnsNoRows(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	_, agentID := seedTenantAgent(t, db)
	userID := seedUserForShares(t, db).String()

	teamA, _ := seedTeam(t, db, uuid.Nil, agentID)
	teamB, _ := seedTeam(t, db, uuid.Nil, agentID) // no rows exist for this team

	_ = insertEpisodicWithTeam(t, agentID, userID, &teamA, "secret team alpha data")

	epStore := pg.NewPGEpisodicStore(db)
	// Query with team_id=B — must return NO rows.
	scope := &store.EpisodicScope{TeamID: &teamB}
	results, err := epStore.Search(ctx, "secret", agentID.String(), userID,
		store.EpisodicSearchOptions{
			MaxResults: 10,
			Scope:      scope,
		})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) > 0 {
		t.Errorf("cross-team query returned %d result(s), want 0 — scope filter not applied", len(results))
	}
}
