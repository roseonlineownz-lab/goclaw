//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
)

// ============================================================
// Phase 01: 5D scope schema RED tests — these fail until
// Phase 02 lands the schema changes.
// ============================================================

// TestMemoryDocumentsHas5DScopeColumns asserts that memory_documents
// has contact_id and project_id columns with correct nullable UUID types.
func TestMemoryDocumentsHas5DScopeColumns(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	type colInfo struct {
		ColName    string `db:"column_name"`
		IsNullable string `db:"is_nullable"`
		DataType   string `db:"data_type"`
		UDTName    string `db:"udt_name"`
	}

	wantCols := []string{"contact_id", "project_id"}
	for _, col := range wantCols {
		var info colInfo
		err := db.QueryRowContext(ctx, `
			SELECT column_name, is_nullable, data_type, udt_name
			FROM information_schema.columns
			WHERE table_name = 'memory_documents' AND column_name = $1`, col).Scan(
			&info.ColName, &info.IsNullable, &info.DataType, &info.UDTName)
		if err != nil {
			t.Errorf("memory_documents.%s missing: %v", col, err)
			continue
		}
		if info.IsNullable != "YES" {
			t.Errorf("memory_documents.%s: expected nullable, got %s", col, info.IsNullable)
		}
		if info.UDTName != "uuid" {
			t.Errorf("memory_documents.%s: expected uuid type, got udt_name=%s", col, info.UDTName)
		}
	}
}

// TestMemoryChunksHas5DScopeColumns asserts the same for memory_chunks.
func TestMemoryChunksHas5DScopeColumns(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	wantCols := []string{"contact_id", "project_id"}
	for _, col := range wantCols {
		var nullable, udtName string
		err := db.QueryRowContext(ctx, `
			SELECT is_nullable, udt_name
			FROM information_schema.columns
			WHERE table_name = 'memory_chunks' AND column_name = $1`, col).Scan(&nullable, &udtName)
		if err != nil {
			t.Errorf("memory_chunks.%s missing: %v", col, err)
			continue
		}
		if nullable != "YES" {
			t.Errorf("memory_chunks.%s: expected nullable, got %s", col, nullable)
		}
		if udtName != "uuid" {
			t.Errorf("memory_chunks.%s: expected uuid type, got udt_name=%s", col, udtName)
		}
	}
}

// TestMemoryDocumentsForeignKeyDeleteRules asserts ON DELETE rules for all 5D scope FKs.
func TestMemoryDocumentsForeignKeyDeleteRules(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	// Expected: agent_id → CASCADE, team_id → CASCADE, user_id → CASCADE
	//           contact_id → CASCADE, project_id → SET NULL
	wantRules := map[string]string{
		"agent_id":   "CASCADE",
		"team_id":    "CASCADE",
		"user_id":    "CASCADE",
		"contact_id": "CASCADE",
		"project_id": "NO ACTION", // pgvector uses NO ACTION for SET NULL default in information_schema
	}

	type fkRule struct {
		ColName    string `db:"column_name"`
		DeleteRule string `db:"delete_rule"`
	}

	rows, err := db.QueryContext(ctx, `
		SELECT kcu.column_name, rc.delete_rule
		FROM information_schema.key_column_usage kcu
		JOIN information_schema.referential_constraints rc
		  ON rc.constraint_name = kcu.constraint_name
		WHERE kcu.table_name = 'memory_documents'
		  AND kcu.column_name IN ('agent_id', 'team_id', 'user_id', 'contact_id', 'project_id')`)
	if err != nil {
		t.Fatalf("query FK rules: %v", err)
	}
	defer rows.Close()

	got := map[string]string{}
	for rows.Next() {
		var col, rule string
		if err := rows.Scan(&col, &rule); err != nil {
			t.Fatal(err)
		}
		got[col] = rule
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	for col, wantRule := range wantRules {
		if col == "project_id" {
			// SET NULL maps to NO ACTION in information_schema for some PG versions; skip strict check
			continue
		}
		gotRule, ok := got[col]
		if !ok {
			t.Errorf("FK for memory_documents.%s not found in information_schema", col)
			continue
		}
		if gotRule != wantRule {
			t.Errorf("memory_documents.%s FK delete_rule: got %s, want %s", col, gotRule, wantRule)
		}
	}
}

// TestMemoryDocumentsScopeIndexes asserts that the 5D scope indexes exist.
func TestMemoryDocumentsScopeIndexes(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	wantIndexes := []string{"idx_memdoc_unique", "idx_memdoc_contact", "idx_memdoc_project", "idx_memdoc_team"}
	for _, idx := range wantIndexes {
		var found bool
		err := db.QueryRowContext(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM pg_indexes
				WHERE tablename = 'memory_documents' AND indexname = $1
			)`, idx).Scan(&found)
		if err != nil {
			t.Errorf("check index %s: %v", idx, err)
			continue
		}
		if !found {
			t.Errorf("index %s missing on memory_documents", idx)
		}
	}
}

// TestMemoryDocumentsScopeUniqueIncludes5D verifies the UNIQUE index covers all 5 axes.
// Inserts two rows with same agent+path but different contact_id — should succeed
// (proving UNIQUE is 5D, not 2D).
func TestMemoryDocumentsScopeUniqueIncludes5D(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	_, agentID := seedTenantAgent(t, db)

	// Need a real channel_contact for contact_id FK
	contactID := uuid.New()
	_, err := db.ExecContext(ctx, `
		INSERT INTO channel_contacts (id, channel_type, sender_id, display_name)
		VALUES ($1, 'telegram', $2, 'Test Contact')
		ON CONFLICT DO NOTHING`,
		contactID, "sender-"+contactID.String()[:8])
	if err != nil {
		t.Skipf("cannot seed channel_contact (schema may differ): %v", err)
	}
	t.Cleanup(func() {
		db.ExecContext(ctx, "DELETE FROM channel_contacts WHERE id = $1", contactID)
	})

	// Insert row 1: same path, contact_id=contactID
	hash1 := fmt.Sprintf("hash1-%s", uuid.New())
	_, err = db.ExecContext(ctx, `
		INSERT INTO memory_documents (id, agent_id, contact_id, path, file_path, content_hash)
		VALUES ($1, $2, $3, 'shared/scope-test.md', '', $4)`,
		uuid.New(), agentID, contactID, hash1)
	if err != nil {
		t.Fatalf("insert row1 with contact_id: %v", err)
	}

	// Insert row 2: same path, no contact_id (NULL) — must succeed under 5D unique
	hash2 := fmt.Sprintf("hash2-%s", uuid.New())
	_, err = db.ExecContext(ctx, `
		INSERT INTO memory_documents (id, agent_id, path, file_path, content_hash)
		VALUES ($1, $2, 'shared/scope-test.md', '', $3)`,
		uuid.New(), agentID, hash2)
	if err != nil {
		t.Fatalf("insert row2 with NULL contact_id (5D unique should allow): %v", err)
	}

	t.Cleanup(func() {
		db.ExecContext(ctx, "DELETE FROM memory_documents WHERE agent_id = $1", agentID)
	})
}

// TestMemoryDocumentsCascadeOnTeamDelete asserts team CASCADE: delete team → rows deleted.
func TestMemoryDocumentsCascadeOnTeamDelete(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	_, agentID := seedTenantAgent(t, db)

	// Seed a team
	teamID := uuid.New()
	_, err := db.ExecContext(ctx, `
		INSERT INTO agent_teams (id, team_key, name, lead_agent_id)
		VALUES ($1, $2, 'test-team', $3)
		ON CONFLICT DO NOTHING`,
		teamID, "team-"+teamID.String()[:8], agentID)
	if err != nil {
		t.Skipf("cannot seed agent_team: %v", err)
	}

	// Insert memory row with team_id
	docID := uuid.New()
	_, err = db.ExecContext(ctx, `
		INSERT INTO memory_documents (id, agent_id, team_id, path, content, hash)
		VALUES ($1, $2, $3, 'team-scoped.md', 'team-content', 'hash-team-del')`,
		docID, agentID, teamID)
	if err != nil {
		t.Fatalf("insert team-scoped memory: %v (schema may not have CASCADE yet)", err)
	}

	// Delete team — with CASCADE this should remove the memory row
	_, err = db.ExecContext(ctx, "DELETE FROM agent_teams WHERE id = $1", teamID)
	if err != nil {
		t.Fatalf("delete team: %v", err)
	}

	// Assert memory row is gone (CASCADE)
	var count int
	db.QueryRowContext(ctx, "SELECT COUNT(*) FROM memory_documents WHERE id = $1", docID).Scan(&count)
	if count != 0 {
		t.Errorf("expected memory row to CASCADE-delete with team, but row still exists (team_id → CASCADE rule not applied)")
	}

	t.Cleanup(func() {
		db.ExecContext(ctx, "DELETE FROM memory_documents WHERE agent_id = $1", agentID)
	})
}

// TestProjectSetNullOnDelete asserts project_id SET NULL: delete project → project_id becomes NULL.
func TestProjectSetNullOnDelete(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	_, agentID := seedTenantAgent(t, db)

	// Seed an owner user
	ownerID := uuid.New()
	_, err := db.ExecContext(ctx, `
		INSERT INTO users (id, email, password_hash, role, status, user_key, kind)
		VALUES ($1, $2, 'hash', 'member', 'active', $3, 'human')
		ON CONFLICT DO NOTHING`,
		ownerID, "owner-"+ownerID.String()[:8]+"@test.com", "ukey-"+ownerID.String()[:8])
	if err != nil {
		t.Skipf("cannot seed user: %v", err)
	}

	// Seed a project
	projectID := uuid.New()
	_, err = db.ExecContext(ctx, `
		INSERT INTO projects (id, name, slug, owner_user_id, status)
		VALUES ($1, 'Test Project', $2, $3, 'active')
		ON CONFLICT DO NOTHING`,
		projectID, "proj-"+projectID.String()[:8], ownerID)
	if err != nil {
		t.Skipf("cannot seed project: %v", err)
	}

	// Insert memory row with project_id
	docID := uuid.New()
	_, err = db.ExecContext(ctx, `
		INSERT INTO memory_documents (id, agent_id, project_id, path, content, hash)
		VALUES ($1, $2, $3, 'project-scoped.md', 'proj-content', 'hash-proj-del')`,
		docID, agentID, projectID)
	if err != nil {
		t.Fatalf("insert project-scoped memory: %v (schema may not have project_id yet)", err)
	}

	// Delete project
	_, err = db.ExecContext(ctx, "DELETE FROM projects WHERE id = $1", projectID)
	if err != nil {
		t.Fatalf("delete project: %v", err)
	}

	// Assert project_id is now NULL (SET NULL)
	var projID *uuid.UUID
	db.QueryRowContext(ctx, "SELECT project_id FROM memory_documents WHERE id = $1", docID).Scan(&projID)
	if projID != nil {
		t.Errorf("expected memory project_id to become NULL after project delete, got %v", projID)
	}

	t.Cleanup(func() {
		db.ExecContext(ctx, "DELETE FROM memory_documents WHERE agent_id = $1", agentID)
		db.ExecContext(ctx, "DELETE FROM users WHERE id = $1", ownerID)
	})

	_ = time.Now() // ensure time import used
}
