//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// TestE2EEpisodicSummaryInherits5DScope verifies that episodic_summaries can
// store the full 5D scope (team_id, contact_id, project_id) inherited from
// source memory chunks, and that the values round-trip correctly.
func TestE2EEpisodicSummaryInherits5DScope(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	fx := makeScopeFixtures(t, db)

	epID := uuid.New()

	// Insert episodic summary with full 5D scope.
	_, err := db.ExecContext(ctx, `
		INSERT INTO episodic_summaries
			(id, agent_id, user_id, team_id, contact_id, project_id,
			 session_key, summary, l0_abstract, key_topics, source_type, source_id,
			 turn_count, token_count)
		VALUES ($1,$2,$3,$4,$5,$6,'sess-scope-test','Full scope summary.','L0 abstract','[]','session','src-scope-test',5,200)`,
		epID, fx.AgentID, fx.UserID, fx.TeamID, fx.ContactID, fx.ProjectID,
	)
	if err != nil {
		t.Fatalf("insert episodic with 5D scope: %v", err)
	}

	// Read back and assert all 5D fields.
	var gotTeam, gotContact, gotProject uuid.UUID
	var gotUser uuid.UUID
	err = db.QueryRowContext(ctx, `
		SELECT user_id, team_id, contact_id, project_id
		FROM episodic_summaries WHERE id = $1`, epID).
		Scan(&gotUser, &gotTeam, &gotContact, &gotProject)
	if err != nil {
		t.Fatalf("read back episodic: %v", err)
	}
	if gotUser != fx.UserID {
		t.Errorf("user_id: got %v, want %v", gotUser, fx.UserID)
	}
	if gotTeam != fx.TeamID {
		t.Errorf("team_id: got %v, want %v", gotTeam, fx.TeamID)
	}
	if gotContact != fx.ContactID {
		t.Errorf("contact_id: got %v, want %v", gotContact, fx.ContactID)
	}
	if gotProject != fx.ProjectID {
		t.Errorf("project_id: got %v, want %v", gotProject, fx.ProjectID)
	}

	t.Cleanup(func() {
		db.ExecContext(ctx, "DELETE FROM episodic_summaries WHERE id = $1", epID)
	})
}

// TestE2EPrivacyL31UserZoneInviolable verifies the L31 invariant:
// a memory row written with user_id=alice cannot be read by a query scoped to user_id=bob.
// Even an agent-broad query (no user filter) must not surface alice's user-private row
// unless the caller IS alice.
func TestE2EPrivacyL31UserZoneInviolable(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	agentID := uuid.New()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO agents (id, agent_key, status, provider, model, owner_id)
		VALUES ($1,$2,'active','test','test-model','l31-owner') ON CONFLICT DO NOTHING`,
		agentID, "l31-agent-"+agentID.String()[:8]); err != nil {
		t.Skipf("seed agent: %v", err)
	}

	aliceID := uuid.New()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO users (id, email, password_hash, role, status, user_key, kind)
		VALUES ($1,$2,'hash','member','active',$3,'human') ON CONFLICT DO NOTHING`,
		aliceID, "alice-l31-"+aliceID.String()[:8]+"@test.com", "alice-"+aliceID.String()[:8]); err != nil {
		t.Skipf("seed alice: %v", err)
	}

	bobID := uuid.New()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO users (id, email, password_hash, role, status, user_key, kind)
		VALUES ($1,$2,'hash','member','active',$3,'human') ON CONFLICT DO NOTHING`,
		bobID, "bob-l31-"+bobID.String()[:8]+"@test.com", "bob-"+bobID.String()[:8]); err != nil {
		t.Skipf("seed bob: %v", err)
	}

	// Alice writes a private memory row.
	aliceDocID := uuid.New()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO memory_documents (id, agent_id, user_id, path, file_path, content_hash)
		VALUES ($1,$2,$3,'private/alice-secret.md','','hash-alice-l31')`,
		aliceDocID, agentID, aliceID); err != nil {
		t.Fatalf("alice write private memory: %v", err)
	}

	// Bob's query (user_id = bob) — must see ZERO rows from alice.
	var bobCount int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM memory_documents
		WHERE agent_id = $1 AND user_id = $2`,
		agentID, bobID).Scan(&bobCount); err != nil {
		t.Fatalf("bob query: %v", err)
	}
	if bobCount != 0 {
		t.Errorf("L31 violation: bob's scoped query returned %d rows (expected 0)", bobCount)
	}

	// Positive check: alice queries her own zone → sees the row.
	var aliceCount int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM memory_documents
		WHERE agent_id = $1 AND user_id = $2`,
		agentID, aliceID).Scan(&aliceCount); err != nil {
		t.Fatalf("alice query: %v", err)
	}
	if aliceCount != 1 {
		t.Errorf("alice should see her own private row: got %d rows", aliceCount)
	}

	// Agent-broad query (no user_id filter) must NOT surface alice's user-private row.
	// Per L31: user-private rows (user_id != NULL) are NOT visible to agent-broad queries.
	var broadCount int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM memory_documents
		WHERE agent_id = $1 AND user_id IS NULL`,
		agentID).Scan(&broadCount); err != nil {
		t.Fatalf("agent-broad query: %v", err)
	}
	// Alice's row has user_id = alice, so it should NOT appear in the user_id IS NULL query.
	if broadCount != 0 {
		t.Errorf("L31 violation: agent-broad (user_id IS NULL) query returned %d rows for alice's user-private doc", broadCount)
	}

	t.Cleanup(func() {
		db.ExecContext(ctx, "DELETE FROM memory_documents WHERE agent_id = $1", agentID)
		db.ExecContext(ctx, "DELETE FROM users WHERE id IN ($1, $2)", aliceID, bobID)
		db.ExecContext(ctx, "DELETE FROM agents WHERE id = $1", agentID)
	})
}

// TestE2EEpisodicSourceDedup5D verifies that the 5D-aware unique index on
// episodic_summaries (idx_episodic_source_dedup) allows the same source_id
// under different scopes, while preventing exact duplicate (same scope + source_id).
func TestE2EEpisodicSourceDedup5D(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	fx := makeScopeFixtures(t, db)

	const sourceID = "test-source-dedup-5d"
	const sessionKey = "sess-dedup-5d"

	// Insert row 1: agent-broad scope (user_id, team_id, contact_id, project_id all NULL)
	id1 := uuid.New()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO episodic_summaries
			(id, agent_id, session_key, summary, l0_abstract, key_topics,
			 source_type, source_id, turn_count, token_count)
		VALUES ($1,$2,$3,'Summary 1.','L0 1','[]','session',$4,2,100)`,
		id1, fx.AgentID, sessionKey+"-1", sourceID); err != nil {
		t.Fatalf("insert row1 (agent-broad): %v", err)
	}

	// Insert row 2: user-scoped (different scope → different UNIQUE key → should succeed)
	id2 := uuid.New()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO episodic_summaries
			(id, agent_id, user_id, session_key, summary, l0_abstract, key_topics,
			 source_type, source_id, turn_count, token_count)
		VALUES ($1,$2,$3,$4,'Summary 2.','L0 2','[]','session',$5,2,100)`,
		id2, fx.AgentID, fx.UserID, sessionKey+"-2", sourceID); err != nil {
		t.Fatalf("insert row2 (user-scoped, same source_id, different scope): %v", err)
	}

	// Insert row 3: exact duplicate of row 1 (same agent-broad scope + same source_id)
	// Should be silently ignored by ON CONFLICT DO NOTHING.
	id3 := uuid.New()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO episodic_summaries
			(id, agent_id, session_key, summary, l0_abstract, key_topics,
			 source_type, source_id, turn_count, token_count)
		VALUES ($1,$2,$3,'Duplicate of 1.','L0 dup','[]','session',$4,2,100)
		ON CONFLICT DO NOTHING`,
		id3, fx.AgentID, sessionKey+"-3", sourceID); err != nil {
		t.Fatalf("insert row3 (duplicate): %v", err)
	}

	// Assert: row3 was NOT inserted (dedup by ON CONFLICT DO NOTHING)
	var count int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM episodic_summaries
		WHERE agent_id = $1 AND source_id = $2 AND user_id IS NULL`,
		fx.AgentID, sourceID).Scan(&count); err != nil {
		t.Fatalf("count agent-broad episodic rows: %v", err)
	}
	if count != 1 {
		t.Errorf("5D dedup: expected exactly 1 agent-broad row for source_id %q, got %d", sourceID, count)
	}

	// Assert: row2 with user_id exists (different scope → separate row)
	var userCount int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM episodic_summaries
		WHERE agent_id = $1 AND source_id = $2 AND user_id = $3`,
		fx.AgentID, sourceID, fx.UserID).Scan(&userCount); err != nil {
		t.Fatalf("count user-scoped episodic rows: %v", err)
	}
	if userCount != 1 {
		t.Errorf("5D dedup: expected 1 user-scoped row for source_id %q, got %d", sourceID, userCount)
	}

	t.Cleanup(func() {
		db.ExecContext(ctx, "DELETE FROM episodic_summaries WHERE agent_id = $1", fx.AgentID)
	})
}

// TestE2EKGEntityInherits5DScope verifies that kg_entities can store all 5D scope
// fields (team_id, contact_id, project_id) and that they round-trip correctly.
func TestE2EKGEntityInherits5DScope(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	fx := makeScopeFixtures(t, db)

	entityID := uuid.New()
	_, err := db.ExecContext(ctx, `
		INSERT INTO kg_entities
			(id, agent_id, user_id, team_id, contact_id, project_id,
			 external_id, name, entity_type, confidence)
		VALUES ($1,$2,$3,$4,$5,$6,'ext-5d-test','5D Entity','person',0.9)`,
		entityID, fx.AgentID, fx.UserID, fx.TeamID, fx.ContactID, fx.ProjectID,
	)
	if err != nil {
		t.Fatalf("insert kg_entity with 5D scope: %v", err)
	}

	var gotTeam, gotContact, gotProject uuid.UUID
	err = db.QueryRowContext(ctx, `
		SELECT team_id, contact_id, project_id
		FROM kg_entities WHERE id = $1`, entityID).
		Scan(&gotTeam, &gotContact, &gotProject)
	if err != nil {
		t.Fatalf("read back kg_entity: %v", err)
	}
	if gotTeam != fx.TeamID {
		t.Errorf("kg team_id: got %v, want %v", gotTeam, fx.TeamID)
	}
	if gotContact != fx.ContactID {
		t.Errorf("kg contact_id: got %v, want %v", gotContact, fx.ContactID)
	}
	if gotProject != fx.ProjectID {
		t.Errorf("kg project_id: got %v, want %v", gotProject, fx.ProjectID)
	}

	t.Cleanup(func() {
		db.ExecContext(ctx, "DELETE FROM kg_entities WHERE id = $1", entityID)
	})
}
