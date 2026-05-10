//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

// wantMeta is the test metadata payload written on every insert.
// Round-trip must return equivalent JSON.
var wantMeta = map[string]any{"k": "v", "n": float64(42)}

// metaJSON is the JSON representation of wantMeta to INSERT directly.
const metaJSON = `{"k":"v","n":42}`

// assertMetaRoundTrip reads the metadata column for a given table+pk and
// asserts it deep-equals wantMeta.
func assertMetaRoundTrip(t *testing.T, table, pkCol string, pkVal any) {
	t.Helper()
	db := testDB(t)
	var raw string
	err := db.QueryRowContext(context.Background(),
		`SELECT metadata::text FROM `+table+` WHERE `+pkCol+` = $1`, pkVal,
	).Scan(&raw)
	if err != nil {
		t.Fatalf("%s: scan metadata: %v", table, err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("%s: unmarshal metadata %q: %v", table, raw, err)
	}
	for wk, wv := range wantMeta {
		gv, ok := got[wk]
		if !ok {
			t.Errorf("%s: metadata missing key %q", table, wk)
			continue
		}
		if gv != wv {
			t.Errorf("%s: metadata[%q] = %v, want %v", table, wk, gv, wv)
		}
	}
}

// metaSeedUser inserts a minimal user row and returns its UUID.
func metaSeedUser(t *testing.T) uuid.UUID {
	t.Helper()
	db := testDB(t)
	id := uuid.New()
	key := "u-" + id.String()[:8]
	email := key + "@meta-test.example.com"
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO users (id, email, password_hash, role, status, user_key, kind)
		 VALUES ($1, $2, 'hash', 'member', 'active', $3, 'human')`,
		id, email, key)
	if err != nil {
		t.Fatalf("metaSeedUser: %v", err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM users WHERE id = $1", id) })
	return id
}

// metaSeedAgent inserts a minimal agent row and returns its UUID.
func metaSeedAgent(t *testing.T) uuid.UUID {
	t.Helper()
	db := testDB(t)
	id := uuid.New()
	key := "a-" + id.String()[:8]
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO agents (id, agent_key, status, provider, model, owner_id)
		 VALUES ($1, $2, 'active', 'test', 'test-model', 'test-owner')`,
		id, key)
	if err != nil {
		t.Fatalf("metaSeedAgent: %v", err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM agents WHERE id = $1", id) })
	return id
}

// metaSeedSkill inserts a minimal skills row and returns its UUID.
func metaSeedSkill(t *testing.T) uuid.UUID {
	t.Helper()
	db := testDB(t)
	id := uuid.New()
	slug := "sk-" + id.String()[:8]
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO skills (id, name, slug, owner_id, visibility, file_path, file_size)
		 VALUES ($1, $2, $3, 'owner', 'private', '/tmp/test.md', 0)`,
		id, "skill-"+slug, slug)
	if err != nil {
		t.Fatalf("metaSeedSkill: %v", err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM skills WHERE id = $1", id) })
	return id
}

// TestPGMetadataCRUD verifies that metadata is stored and retrieved correctly
// for each of the 13 entity tables. Each sub-test inserts a row with a known
// metadata JSON object and reads it back, asserting equality.
func TestPGMetadataCRUD(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	// llm_providers
	t.Run("llm_providers", func(t *testing.T) {
		id := uuid.New()
		_, err := db.ExecContext(ctx,
			`INSERT INTO llm_providers (id, name, provider_type, metadata)
			 VALUES ($1, $2, 'openai_compat', $3::jsonb)`,
			id, "prov-"+id.String()[:8], metaJSON)
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
		t.Cleanup(func() { db.Exec("DELETE FROM llm_providers WHERE id = $1", id) })
		assertMetaRoundTrip(t, "llm_providers", "id", id)
	})

	// user_sessions
	t.Run("user_sessions", func(t *testing.T) {
		uid := metaSeedUser(t)
		id := uuid.New()
		fam := uuid.New()
		tok := "tok-" + id.String()
		_, err := db.ExecContext(ctx,
			`INSERT INTO user_sessions (id, user_id, family_id, refresh_token_hash, expires_at, metadata)
			 VALUES ($1, $2, $3, $4, $5, $6::jsonb)`,
			id, uid, fam, tok, time.Now().Add(time.Hour), metaJSON)
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
		t.Cleanup(func() { db.Exec("DELETE FROM user_sessions WHERE id = $1", id) })
		assertMetaRoundTrip(t, "user_sessions", "id", id)
	})

	// agents
	t.Run("agents", func(t *testing.T) {
		id := uuid.New()
		key := "a-" + id.String()[:8]
		_, err := db.ExecContext(ctx,
			`INSERT INTO agents (id, agent_key, status, provider, model, owner_id, metadata)
			 VALUES ($1, $2, 'active', 'test', 'test-model', 'owner', $3::jsonb)`,
			id, key, metaJSON)
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
		t.Cleanup(func() { db.Exec("DELETE FROM agents WHERE id = $1", id) })
		assertMetaRoundTrip(t, "agents", "id", id)
	})

	// agent_shares
	t.Run("agent_shares", func(t *testing.T) {
		agentID := metaSeedAgent(t)
		userID := metaSeedUser(t)
		id := uuid.New()
		_, err := db.ExecContext(ctx,
			`INSERT INTO agent_shares (id, agent_id, shared_with_user_id, role, created_by, metadata)
			 VALUES ($1, $2, $3, 'viewer', $3, $4::jsonb)`,
			id, agentID, userID, metaJSON)
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
		t.Cleanup(func() { db.Exec("DELETE FROM agent_shares WHERE id = $1", id) })
		assertMetaRoundTrip(t, "agent_shares", "id", id)
	})

	// agent_links
	t.Run("agent_links", func(t *testing.T) {
		src := metaSeedAgent(t)
		dst := metaSeedAgent(t)
		id := uuid.New()
		_, err := db.ExecContext(ctx,
			`INSERT INTO agent_links (id, source_agent_id, target_agent_id, direction, created_by, metadata)
			 VALUES ($1, $2, $3, 'outbound', 'test', $4::jsonb)`,
			id, src, dst, metaJSON)
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
		t.Cleanup(func() { db.Exec("DELETE FROM agent_links WHERE id = $1", id) })
		assertMetaRoundTrip(t, "agent_links", "id", id)
	})

	// agent_teams
	t.Run("agent_teams", func(t *testing.T) {
		agentID := metaSeedAgent(t)
		id := uuid.New()
		key := "tm-" + id.String()[:8]
		_, err := db.ExecContext(ctx,
			`INSERT INTO agent_teams (id, name, lead_agent_id, status, settings, created_by, team_key, metadata)
			 VALUES ($1, $2, $3, 'active', '{}', 'test', $4, $5::jsonb)`,
			id, "team-"+key, agentID, key, metaJSON)
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
		t.Cleanup(func() { db.Exec("DELETE FROM agent_teams WHERE id = $1", id) })
		assertMetaRoundTrip(t, "agent_teams", "id", id)
	})

	// memory_documents
	t.Run("memory_documents", func(t *testing.T) {
		agentID := metaSeedAgent(t)
		id := uuid.New()
		_, err := db.ExecContext(ctx,
			`INSERT INTO memory_documents (id, agent_id, path, file_path, content_hash, metadata)
			 VALUES ($1, $2, '/test/doc', '/test/doc', 'abc123', $3::jsonb)`,
			id, agentID, metaJSON)
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
		t.Cleanup(func() { db.Exec("DELETE FROM memory_documents WHERE id = $1", id) })
		assertMetaRoundTrip(t, "memory_documents", "id", id)
	})

	// skills
	t.Run("skills", func(t *testing.T) {
		id := uuid.New()
		slug := "sk-" + id.String()[:8]
		_, err := db.ExecContext(ctx,
			`INSERT INTO skills (id, name, slug, owner_id, visibility, file_path, file_size, metadata)
			 VALUES ($1, $2, $3, 'owner', 'private', '/tmp/test.md', 0, $4::jsonb)`,
			id, "skill-"+slug, slug, metaJSON)
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
		t.Cleanup(func() { db.Exec("DELETE FROM skills WHERE id = $1", id) })
		assertMetaRoundTrip(t, "skills", "id", id)
	})

	// skill_versions
	t.Run("skill_versions", func(t *testing.T) {
		skillID := metaSeedSkill(t)
		id := uuid.New()
		_, err := db.ExecContext(ctx,
			`INSERT INTO skill_versions (id, skill_id, version, file_hash, file_path, file_size, content, metadata)
			 VALUES ($1, $2, 1, 'abc123', '/tmp/v1.md', 0, 'content', $3::jsonb)`,
			id, skillID, metaJSON)
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
		t.Cleanup(func() { db.Exec("DELETE FROM skill_versions WHERE id = $1", id) })
		assertMetaRoundTrip(t, "skill_versions", "id", id)
	})

	// channel_instances
	t.Run("channel_instances", func(t *testing.T) {
		agentID := metaSeedAgent(t)
		id := uuid.New()
		name := "ch-" + id.String()[:8]
		_, err := db.ExecContext(ctx,
			`INSERT INTO channel_instances (id, name, channel_type, agent_id, enabled, metadata)
			 VALUES ($1, $2, 'telegram', $3, true, $4::jsonb)`,
			id, name, agentID, metaJSON)
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
		t.Cleanup(func() { db.Exec("DELETE FROM channel_instances WHERE id = $1", id) })
		assertMetaRoundTrip(t, "channel_instances", "id", id)
	})

	// mcp_servers
	t.Run("mcp_servers", func(t *testing.T) {
		id := uuid.New()
		name := "mcp-" + id.String()[:8]
		_, err := db.ExecContext(ctx,
			`INSERT INTO mcp_servers (id, name, transport, created_by, metadata)
			 VALUES ($1, $2, 'stdio', 'test', $3::jsonb)`,
			id, name, metaJSON)
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
		t.Cleanup(func() { db.Exec("DELETE FROM mcp_servers WHERE id = $1", id) })
		assertMetaRoundTrip(t, "mcp_servers", "id", id)
	})

	// cron_jobs
	t.Run("cron_jobs", func(t *testing.T) {
		id := uuid.New()
		_, err := db.ExecContext(ctx,
			`INSERT INTO cron_jobs (id, name, schedule_kind, payload, metadata)
			 VALUES ($1, $2, 'every', '{}', $3::jsonb)`,
			id, "cron-"+id.String()[:8], metaJSON)
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
		t.Cleanup(func() { db.Exec("DELETE FROM cron_jobs WHERE id = $1", id) })
		assertMetaRoundTrip(t, "cron_jobs", "id", id)
	})

	// system_configs — pk is `key`, not `id`
	t.Run("system_configs", func(t *testing.T) {
		key := "meta-test-" + uuid.New().String()[:8]
		_, err := db.ExecContext(ctx,
			`INSERT INTO system_configs (key, value, metadata)
			 VALUES ($1, 'testval', $2::jsonb)`,
			key, metaJSON)
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
		t.Cleanup(func() { db.Exec("DELETE FROM system_configs WHERE key = $1", key) })
		// system_configs pk is text key
		var raw string
		err = db.QueryRowContext(ctx,
			`SELECT metadata::text FROM system_configs WHERE key = $1`, key,
		).Scan(&raw)
		if err != nil {
			t.Fatalf("system_configs: scan metadata: %v", err)
		}
		var got map[string]any
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatalf("system_configs: unmarshal %q: %v", raw, err)
		}
		for wk, wv := range wantMeta {
			gv, ok := got[wk]
			if !ok {
				t.Errorf("system_configs: metadata missing key %q", wk)
				continue
			}
			if gv != wv {
				t.Errorf("system_configs: metadata[%q] = %v, want %v", wk, gv, wv)
			}
		}
	})
}
