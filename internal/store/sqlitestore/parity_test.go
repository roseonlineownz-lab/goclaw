//go:build sqlite || sqliteonly

package sqlitestore

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// tableColumns returns the set of column names for a given table via PRAGMA.
func tableColumns(t *testing.T, db *sql.DB, table string) map[string]bool {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", table, err)
	}
	defer rows.Close()

	cols := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull int
		var dflt *string
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan column row for %s: %v", table, err)
		}
		cols[name] = true
	}
	return cols
}

// TestSchemaParityFreshDB verifies that a fresh DB created from schema.sql has
// all identity and metadata columns introduced by the v4 foundation rebuild:
//   - users: user_key, kind, channel_type, metadata
//   - agent_teams: team_key, metadata
//   - 13 entity tables: metadata column on each
func TestSchemaParityFreshDB(t *testing.T) {
	db := openTestDB(t)
	if err := EnsureSchema(db); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}

	// users: identity slug + kind + channel columns
	usersWant := []string{"user_key", "kind", "channel_type", "metadata"}
	usersCols := tableColumns(t, db, "users")
	for _, col := range usersWant {
		if !usersCols[col] {
			t.Errorf("users: missing column %q", col)
		}
	}

	// agent_teams: team slug + metadata
	teamWant := []string{"team_key", "metadata"}
	teamCols := tableColumns(t, db, "agent_teams")
	for _, col := range teamWant {
		if !teamCols[col] {
			t.Errorf("agent_teams: missing column %q", col)
		}
	}

	// 13 entity tables that must carry metadata
	entityTables := []string{
		"agents",
		"agent_teams",
		"agent_shares",
		"agent_links",
		"memory_documents",
		"skills",
		"skill_versions",
		"channel_instances",
		"mcp_servers",
		"cron_jobs",
		"llm_providers",
		"system_configs",
		"user_sessions",
	}
	for _, tbl := range entityTables {
		cols := tableColumns(t, db, tbl)
		if !cols["metadata"] {
			t.Errorf("table %q: missing metadata column", tbl)
		}
	}
}

// minimalOldSchema is a stripped-down schema representing a desktop DB created
// before the foundation rebuild landed — it lacks user_key, kind, channel_type,
// team_key, and metadata on most tables. Applied as the "before" state in the
// incremental migration test.
const minimalOldSchema = `
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL PRIMARY KEY);

CREATE TABLE IF NOT EXISTS llm_providers (
    id            TEXT NOT NULL PRIMARY KEY,
    name          VARCHAR(50) NOT NULL UNIQUE,
    display_name  VARCHAR(255),
    provider_type VARCHAR(30) NOT NULL DEFAULT 'openai_compat',
    api_base      TEXT,
    api_key       TEXT,
    enabled       INTEGER NOT NULL DEFAULT 1,
    settings      TEXT NOT NULL DEFAULT '{}',
    created_at    TEXT,
    updated_at    TEXT
);

CREATE TABLE IF NOT EXISTS users (
    id            TEXT NOT NULL PRIMARY KEY,
    email         VARCHAR(255) NOT NULL UNIQUE,
    display_name  VARCHAR(255),
    password_hash TEXT NOT NULL,
    role          VARCHAR(20) NOT NULL DEFAULT 'member',
    status        VARCHAR(20) NOT NULL DEFAULT 'active',
    deleted_at    TEXT,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS user_sessions (
    id                 TEXT NOT NULL PRIMARY KEY,
    user_id            TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    family_id          TEXT NOT NULL,
    refresh_token_hash TEXT NOT NULL UNIQUE,
    expires_at         TEXT NOT NULL,
    revoked_at         TEXT,
    created_at         TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS agents (
    id          TEXT NOT NULL PRIMARY KEY,
    agent_key   VARCHAR(100) NOT NULL,
    owner_id    VARCHAR(255) NOT NULL,
    provider    VARCHAR(50) NOT NULL DEFAULT 'openrouter',
    model       VARCHAR(200) NOT NULL,
    status      VARCHAR(20) DEFAULT 'active',
    created_at  TEXT,
    updated_at  TEXT
);

CREATE TABLE IF NOT EXISTS agent_shares (
    id         TEXT NOT NULL PRIMARY KEY,
    agent_id   TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       VARCHAR(20) NOT NULL DEFAULT 'user',
    granted_by VARCHAR(255) NOT NULL,
    created_at TEXT
);

CREATE TABLE IF NOT EXISTS agent_links (
    id              TEXT NOT NULL PRIMARY KEY,
    source_agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    target_agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    direction       VARCHAR(20) NOT NULL DEFAULT 'outbound',
    status          VARCHAR(20) NOT NULL DEFAULT 'active',
    created_by      VARCHAR(255) NOT NULL,
    created_at      TEXT
);

CREATE TABLE IF NOT EXISTS agent_teams (
    id            TEXT NOT NULL PRIMARY KEY,
    name          VARCHAR(255) NOT NULL,
    lead_agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    status        VARCHAR(20) NOT NULL DEFAULT 'active',
    settings      TEXT NOT NULL DEFAULT '{}',
    created_by    VARCHAR(255) NOT NULL,
    created_at    TEXT
);

CREATE TABLE IF NOT EXISTS memory_documents (
    id         TEXT NOT NULL PRIMARY KEY,
    agent_id   TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    user_id    TEXT,
    path       VARCHAR(500) NOT NULL,
    content    TEXT NOT NULL DEFAULT '',
    hash       VARCHAR(64) NOT NULL,
    created_at TEXT
);

CREATE TABLE IF NOT EXISTS skills (
    id         TEXT NOT NULL PRIMARY KEY,
    name       VARCHAR(100) NOT NULL,
    slug       VARCHAR(100) NOT NULL UNIQUE,
    created_at TEXT
);

CREATE TABLE IF NOT EXISTS skill_versions (
    id         TEXT NOT NULL PRIMARY KEY,
    skill_id   TEXT NOT NULL REFERENCES skills(id) ON DELETE CASCADE,
    version    VARCHAR(20) NOT NULL,
    created_at TEXT
);

CREATE TABLE IF NOT EXISTS channel_instances (
    id           TEXT NOT NULL PRIMARY KEY,
    channel_type VARCHAR(50) NOT NULL,
    enabled      INTEGER NOT NULL DEFAULT 1,
    created_at   TEXT
);

CREATE TABLE IF NOT EXISTS mcp_servers (
    id         TEXT NOT NULL PRIMARY KEY,
    name       VARCHAR(100) NOT NULL,
    url        TEXT NOT NULL,
    created_at TEXT
);

CREATE TABLE IF NOT EXISTS cron_jobs (
    id             TEXT NOT NULL PRIMARY KEY,
    agent_id       TEXT NOT NULL,
    schedule_kind  VARCHAR(10) NOT NULL,
    schedule_value TEXT NOT NULL,
    enabled        INTEGER NOT NULL DEFAULT 1,
    created_at     TEXT
);

CREATE TABLE IF NOT EXISTS system_configs (
    key        VARCHAR(100) NOT NULL PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

-- Tables that existed at v1 (production) but are stubbed minimally here
-- because subsequent migrations (v8–v15) ALTER them.
CREATE TABLE IF NOT EXISTS agent_sessions (
    id           TEXT NOT NULL PRIMARY KEY,
    agent_id     TEXT NOT NULL,
    session_key  VARCHAR(255) NOT NULL,
    created_at   TEXT
);

CREATE TABLE IF NOT EXISTS channel_contacts (
    id           TEXT NOT NULL PRIMARY KEY,
    channel_type VARCHAR(50) NOT NULL,
    chat_id      VARCHAR(255) NOT NULL,
    user_id      TEXT,
    created_at   TEXT
);

CREATE TABLE IF NOT EXISTS agent_config_permissions (
    id          TEXT NOT NULL PRIMARY KEY,
    agent_id    TEXT NOT NULL,
    config_type VARCHAR(50) NOT NULL,
    created_at  TEXT
);

CREATE TABLE IF NOT EXISTS traces (
    id         TEXT NOT NULL PRIMARY KEY,
    agent_id   TEXT,
    user_id    TEXT,
    created_at TEXT
);

CREATE TABLE IF NOT EXISTS spans (
    id         TEXT NOT NULL PRIMARY KEY,
    trace_id   TEXT NOT NULL,
    created_at TEXT
);

CREATE TABLE IF NOT EXISTS subagent_tasks (
    id         TEXT NOT NULL PRIMARY KEY,
    agent_id   TEXT,
    status     VARCHAR(20),
    created_at TEXT
);

CREATE TABLE IF NOT EXISTS memory_chunks (
    id         TEXT NOT NULL PRIMARY KEY,
    document_id TEXT,
    user_id    TEXT,
    created_at TEXT
);

CREATE TABLE IF NOT EXISTS embedding_cache (
    id         TEXT NOT NULL PRIMARY KEY,
    hash       VARCHAR(64) NOT NULL,
    created_at TEXT
);

CREATE TABLE IF NOT EXISTS episodic_summaries (
    id         TEXT NOT NULL PRIMARY KEY,
    agent_id   TEXT,
    user_id    TEXT,
    created_at TEXT
);

CREATE TABLE IF NOT EXISTS kg_entities (
    id         TEXT NOT NULL PRIMARY KEY,
    agent_id   TEXT,
    user_id    TEXT,
    created_at TEXT
);

CREATE TABLE IF NOT EXISTS vault_documents (
    id         TEXT NOT NULL PRIMARY KEY,
    title      VARCHAR(255),
    created_at TEXT
);

CREATE TABLE IF NOT EXISTS team_tasks (
    id         TEXT NOT NULL PRIMARY KEY,
    team_id    TEXT,
    created_at TEXT
);
`

// TestMigrationFromPriorVersion verifies that an existing desktop DB (built
// before the foundation rebuild) is upgraded cleanly by the incremental
// migration. After the migration runs, all expected columns must be present.
func TestMigrationFromPriorVersion(t *testing.T) {
	// Open a plain :memory: SQLite without EnsureSchema so we can simulate
	// a legacy DB at SchemaVersion 1 (pre-foundation-rebuild columns).
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open :memory: db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// Bootstrap the old schema + seed version = 1 (the version this migration
	// starts from — the v4 greenfield initial version before slug/metadata columns).
	if _, err := db.Exec(minimalOldSchema); err != nil {
		t.Fatalf("apply minimal old schema: %v", err)
	}
	if _, err := db.Exec("INSERT INTO schema_version (version) VALUES (1)"); err != nil {
		t.Fatalf("seed schema_version=1: %v", err)
	}

	// Run migrations from version 1 → current SchemaVersion.
	// This exercises the incremental path of EnsureSchema.
	if err := EnsureSchema(db); err != nil {
		t.Fatalf("EnsureSchema on legacy DB: %v", err)
	}

	// Assert all foundation columns are now present.
	usersCols := tableColumns(t, db, "users")
	for _, col := range []string{"user_key", "kind", "channel_type", "metadata"} {
		if !usersCols[col] {
			t.Errorf("after migration: users missing %q", col)
		}
	}

	teamCols := tableColumns(t, db, "agent_teams")
	for _, col := range []string{"team_key", "metadata"} {
		if !teamCols[col] {
			t.Errorf("after migration: agent_teams missing %q", col)
		}
	}

	entityTables := []string{
		"agents", "agent_shares", "agent_links",
		"memory_documents", "skills", "skill_versions",
		"channel_instances", "mcp_servers", "cron_jobs",
		"llm_providers", "system_configs", "user_sessions",
	}
	for _, tbl := range entityTables {
		cols := tableColumns(t, db, tbl)
		if !cols["metadata"] {
			t.Errorf("after migration: table %q missing metadata", tbl)
		}
	}

	// Verify schema_version was bumped to current SchemaVersion.
	var version int
	if err := db.QueryRow("SELECT version FROM schema_version LIMIT 1").Scan(&version); err != nil {
		t.Fatalf("read schema_version after migration: %v", err)
	}
	if version != SchemaVersion {
		t.Errorf("schema_version = %d, want %d", version, SchemaVersion)
	}
}
