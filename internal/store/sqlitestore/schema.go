//go:build sqlite || sqliteonly

package sqlitestore

import (
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
)

//go:embed schema.sql
var schemaSQL string

// SchemaVersion is the current SQLite schema version for v4.
// Bump this and add an entry to migrations when schema changes are needed.
// v1 → v2: adds user_key/kind/channel_type to users, team_key to agent_teams,
// and metadata to all 13 entity tables (foundation rebuild).
// v2 → v3: adds password_reset_tokens table for self-serve password reset.
// v3 → v4: splits agents.workspace_sharing JSONB into share_workspace + share_memory BOOL.
// v4 → v5: rebuilds agent_shares with target mutex (user XOR team), role enum, FK created_by, updated_at.
// v5 → v6: adds projects table (top-level entity, owner FK to users, immutable slug, status check).
// v6 → v7: adds team_user_members join table (user↔team membership with role + added_by audit trail).
// v7 → v8: adds project_grants table (project-level access control for users and teams).
// v8 → v9: adds project_id FK (SET NULL) to agent_sessions for per-session project binding.
// v9 → v10: adds default_project_id FK (SET NULL) to channel_contacts for group-chat project default.
// v10 → v11: widens idx_projects_status to cover all status values (was active-only partial index).
// Note: projects.owner_user_id ON DELETE RESTRICT was added to schema.sql for fresh DBs but does NOT
// have an incremental migration. SQLite cannot alter FK actions on existing tables without a
// full table-rebuild. The practical behavior is identical: SQLite's default NO ACTION (for FKs with
// PRAGMA foreign_keys=ON) already prevents deletion of owner users that have projects, same as RESTRICT.
// v11 → v12: adds team_id + project_id nullable FKs to mcp_servers for 3-state scope (global/team/project).
// v12 → v13: splits monolithic file_writer gate into write_file/edit_file/delete_file;
// adds deny_globs column with baseline default protecting secrets/dotfiles;
// adds CHECK constraint on config_type for fail-closed validation.
// v13 → v14: adds contact_id to traces + spans.
// v14 → v15: adds project_id FK to subagent_tasks.
// v15 → v16: memory rebuild — 5D scope (contact_id, project_id) on memory_documents,
// memory_chunks, episodic_summaries, kg_entities; FS-backed columns (file_path,
// content_hash, version) on memory_documents; halfvec BLOB + embedding_norm on
// memory_chunks, embedding_cache, kg_entities, vault_documents, skills, team_tasks.
// ON DELETE rules tightened: team_id/user_id CASCADE (was SET NULL) on memory tables.
const SchemaVersion = 16

// migrations maps version → ordered slice of SQL statements to apply when
// upgrading FROM that version to the next.
// schema.sql always represents the LATEST full schema (for fresh DBs).
// Existing DBs are patched incrementally via these steps.
//
// Add future migrations as: migrations[N] = []string{...} and bump SchemaVersion.
var migrations = map[int][]string{
	// Upgrade v1 → v2: foundation identity + metadata columns.
	// Adds stable slug identifiers (user_key, team_key), identity kind columns,
	// and a generic metadata JSONB-equivalent column to all main entity tables.
	// The shape constraint CHECK (kind/channel_type coherence) cannot be added
	// to an existing column via SQLite ALTER TABLE; new rows on upgraded DBs
	// are validated at the application layer. Fresh DBs get the full constraint
	// via schema.sql.
	1: {
		// --- users: identity slug + kind ---
		`ALTER TABLE users ADD COLUMN user_key    VARCHAR(100) NOT NULL DEFAULT ''`,
		`ALTER TABLE users ADD COLUMN kind        VARCHAR(20)  NOT NULL DEFAULT 'human' CHECK (kind IN ('human','channel'))`,
		`ALTER TABLE users ADD COLUMN channel_type VARCHAR(20) NULL`,
		`ALTER TABLE users ADD COLUMN metadata    TEXT         NOT NULL DEFAULT '{}'`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_users_user_key ON users(user_key)`,
		// --- agent_teams: team slug ---
		`ALTER TABLE agent_teams ADD COLUMN team_key VARCHAR(100) NOT NULL DEFAULT ''`,
		`ALTER TABLE agent_teams ADD COLUMN metadata TEXT        NOT NULL DEFAULT '{}'`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_teams_team_key ON agent_teams(team_key)`,
		// --- 11 remaining entity tables: metadata column ---
		`ALTER TABLE agents            ADD COLUMN metadata TEXT NOT NULL DEFAULT '{}'`,
		`ALTER TABLE agent_shares      ADD COLUMN metadata TEXT NOT NULL DEFAULT '{}'`,
		`ALTER TABLE agent_links       ADD COLUMN metadata TEXT NOT NULL DEFAULT '{}'`,
		`ALTER TABLE memory_documents  ADD COLUMN metadata TEXT NOT NULL DEFAULT '{}'`,
		`ALTER TABLE skills            ADD COLUMN metadata TEXT NOT NULL DEFAULT '{}'`,
		`ALTER TABLE skill_versions    ADD COLUMN metadata TEXT NOT NULL DEFAULT '{}'`,
		`ALTER TABLE channel_instances ADD COLUMN metadata TEXT NOT NULL DEFAULT '{}'`,
		`ALTER TABLE mcp_servers       ADD COLUMN metadata TEXT NOT NULL DEFAULT '{}'`,
		`ALTER TABLE cron_jobs         ADD COLUMN metadata TEXT NOT NULL DEFAULT '{}'`,
		`ALTER TABLE llm_providers     ADD COLUMN metadata TEXT NOT NULL DEFAULT '{}'`,
		`ALTER TABLE system_configs    ADD COLUMN metadata TEXT NOT NULL DEFAULT '{}'`,
		`ALTER TABLE user_sessions     ADD COLUMN metadata TEXT NOT NULL DEFAULT '{}'`,
		// --- best-effort slug backfill for legacy rows ---
		// Derives user_key from email local-part (lowercase, strip dots/plus).
		// May produce collisions on legacy data; application layer regenerates
		// unique slugs on next gateway start if needed.
		`UPDATE users SET user_key = lower(replace(replace(substr(email, 1, instr(email||'@', '@')-1), '.', ''), '+', '')) WHERE user_key = ''`,
		`UPDATE agent_teams SET team_key = lower(replace(replace(name, ' ', '-'), '_', '-')) WHERE team_key = ''`,
	},
	// Upgrade v2 → v3: password_reset_tokens table for self-serve password reset.
	// Single-use, time-bounded; raw token mailed once, only SHA-256 hex stored.
	2: {
		`CREATE TABLE IF NOT EXISTS password_reset_tokens (
			id         TEXT NOT NULL PRIMARY KEY,
			user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			token_hash TEXT NOT NULL,
			expires_at TEXT NOT NULL,
			used_at    TEXT,
			created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_password_reset_token_hash ON password_reset_tokens(token_hash)`,
		`CREATE INDEX IF NOT EXISTS idx_password_reset_user   ON password_reset_tokens(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_password_reset_active ON password_reset_tokens(token_hash) WHERE used_at IS NULL`,
	},
	// Upgrade v3 → v4: split workspace_sharing JSONB blob into two boolean
	// flags. share_workspace controls per-user file zone collapse; share_memory
	// covers memory + KG + sessions sharing. Default-false preserves
	// privacy-by-default. The legacy column is dropped.
	3: {
		`ALTER TABLE agents ADD COLUMN share_workspace INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE agents ADD COLUMN share_memory    INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE agents DROP COLUMN workspace_sharing`,
	},
	// Upgrade v4 → v5: rebuild agent_shares with target mutex + role enum.
	// Drop legacy table (greenfield, no data preservation) and recreate to
	// match PG shape. Includes FK to agent_teams via shared_with_team_id.
	4: {
		`DROP TABLE IF EXISTS agent_shares`,
		`CREATE TABLE agent_shares (
			id                  TEXT         NOT NULL PRIMARY KEY,
			agent_id            TEXT         NOT NULL REFERENCES agents(id)      ON DELETE CASCADE,
			shared_with_user_id TEXT         NULL     REFERENCES users(id)       ON DELETE CASCADE,
			shared_with_team_id TEXT         NULL     REFERENCES agent_teams(id) ON DELETE CASCADE,
			role                VARCHAR(20)  NOT NULL CHECK (role IN ('viewer','member','editor')),
			metadata            TEXT         NOT NULL DEFAULT '{}',
			created_by          TEXT         NOT NULL REFERENCES users(id)       ON DELETE RESTRICT,
			created_at          TEXT         NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
			updated_at          TEXT         NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
			CONSTRAINT agent_shares_target_mutex CHECK (
				(shared_with_user_id IS NOT NULL AND shared_with_team_id IS NULL) OR
				(shared_with_user_id IS NULL     AND shared_with_team_id IS NOT NULL)
			)
		)`,
		`CREATE INDEX idx_agent_shares_agent ON agent_shares(agent_id)`,
		`CREATE UNIQUE INDEX idx_agent_shares_user ON agent_shares(agent_id, shared_with_user_id) WHERE shared_with_user_id IS NOT NULL`,
		`CREATE UNIQUE INDEX idx_agent_shares_team ON agent_shares(agent_id, shared_with_team_id) WHERE shared_with_team_id IS NOT NULL`,
	},
	// Upgrade v5 → v6: adds projects table for top-level project entities.
	// Slug is immutable post-create (FS path coupling). Archive via status.
	5: {
		`CREATE TABLE IF NOT EXISTS projects (
			id            TEXT         NOT NULL PRIMARY KEY,
			slug          VARCHAR(100) NOT NULL UNIQUE
			                  CHECK (slug GLOB '[a-z0-9]*' AND slug NOT GLOB '*[^a-z0-9-]*' AND
			                         length(slug) >= 3 AND length(slug) <= 100 AND
			                         substr(slug, 1, 1) != '-' AND substr(slug, length(slug), 1) != '-'),
			owner_user_id TEXT         NOT NULL REFERENCES users(id),
			status        VARCHAR(20)  NOT NULL DEFAULT 'active'
			                  CHECK (status IN ('active', 'archived')),
			metadata      TEXT         NOT NULL DEFAULT '{}',
			created_at    TEXT         NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
			updated_at    TEXT         NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		)`,
		`CREATE INDEX IF NOT EXISTS idx_projects_owner  ON projects(owner_user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_projects_status ON projects(status)`,
	},
	// Upgrade v6 → v7: adds team_user_members join table.
	// User↔team membership with role enum and added_by audit trail.
	// Composite PK (team_id, user_id) prevents duplicate membership rows.
	6: {
		`CREATE TABLE IF NOT EXISTS team_user_members (
			team_id    TEXT        NOT NULL REFERENCES agent_teams(id) ON DELETE CASCADE,
			user_id    TEXT        NOT NULL REFERENCES users(id)       ON DELETE CASCADE,
			role       VARCHAR(20) NOT NULL CHECK (role IN ('viewer', 'member', 'admin')),
			added_by   TEXT        REFERENCES users(id) ON DELETE SET NULL,
			created_at TEXT        NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
			PRIMARY KEY (team_id, user_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_team_user_members_user ON team_user_members(user_id)`,
	},
	// Upgrade v7 → v8: adds project_grants table for project-level access control.
	// Exactly one of user_id/team_id must be set (XOR via CHECK constraint).
	// Separate partial unique indexes replicate PG UNIQUE NULLS NOT DISTINCT behaviour.
	7: {
		`CREATE TABLE IF NOT EXISTS project_grants (
			id          TEXT        NOT NULL PRIMARY KEY,
			project_id  TEXT        NOT NULL REFERENCES projects(id)     ON DELETE CASCADE,
			user_id     TEXT                 REFERENCES users(id)        ON DELETE CASCADE,
			team_id     TEXT                 REFERENCES agent_teams(id)  ON DELETE CASCADE,
			role        VARCHAR(20) NOT NULL CHECK (role IN ('viewer', 'member', 'editor')),
			granted_by  TEXT                 REFERENCES users(id)        ON DELETE SET NULL,
			created_at  TEXT        NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
			CHECK (
				(user_id IS NOT NULL AND team_id IS NULL) OR
				(user_id IS NULL     AND team_id IS NOT NULL)
			)
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_project_grants_unique_user
		    ON project_grants(project_id, user_id) WHERE user_id IS NOT NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_project_grants_unique_team
		    ON project_grants(project_id, team_id) WHERE team_id IS NOT NULL`,
		`CREATE INDEX IF NOT EXISTS idx_project_grants_project ON project_grants(project_id)`,
		`CREATE INDEX IF NOT EXISTS idx_project_grants_user ON project_grants(user_id) WHERE user_id IS NOT NULL`,
		`CREATE INDEX IF NOT EXISTS idx_project_grants_team ON project_grants(team_id) WHERE team_id IS NOT NULL`,
	},
	// Upgrade v8 → v9: adds project_id FK to agent_sessions.
	// Nullable (SET NULL on project delete) so legacy sessions without a project
	// continue to work unchanged. Index is partial — only rows with a project set.
	8: {
		`ALTER TABLE agent_sessions ADD COLUMN project_id TEXT NULL REFERENCES projects(id) ON DELETE SET NULL`,
		`CREATE INDEX IF NOT EXISTS idx_agent_sessions_project ON agent_sessions(project_id) WHERE project_id IS NOT NULL`,
	},
	// Upgrade v9 → v10: adds default_project_id FK to channel_contacts.
	// Group contacts can have a default project; FK SET NULL on project delete
	// so existing contacts remain valid without a project binding.
	9: {
		`ALTER TABLE channel_contacts ADD COLUMN default_project_id TEXT NULL REFERENCES projects(id) ON DELETE SET NULL`,
		`CREATE INDEX IF NOT EXISTS idx_channel_contacts_default_project ON channel_contacts(default_project_id) WHERE default_project_id IS NOT NULL`,
	},
	// Upgrade v10 → v11: widen idx_projects_status to cover both active and archived rows.
	// The original partial index (WHERE status = 'active') did not benefit queries
	// filtering on archived status. Drop and recreate as a full index on (status).
	10: {
		`DROP INDEX IF EXISTS idx_projects_status`,
		`CREATE INDEX IF NOT EXISTS idx_projects_status ON projects(status)`,
	},
	// Upgrade v11 → v12: add team_id / project_id scope columns to mcp_servers.
	// Exactly one of {both NULL (global), team_id only, project_id only} is valid.
	// SQLite CHECK constraints added inline to new columns; existing rows get NULL
	// values which are valid (global scope). ON DELETE CASCADE removes scoped
	// servers when their owner team or project is deleted.
	11: {
		`ALTER TABLE mcp_servers ADD COLUMN team_id    TEXT NULL REFERENCES agent_teams(id) ON DELETE CASCADE`,
		`ALTER TABLE mcp_servers ADD COLUMN project_id TEXT NULL REFERENCES projects(id)    ON DELETE CASCADE`,
		`CREATE INDEX IF NOT EXISTS idx_mcp_servers_team    ON mcp_servers(team_id)    WHERE team_id    IS NOT NULL`,
		`CREATE INDEX IF NOT EXISTS idx_mcp_servers_project ON mcp_servers(project_id) WHERE project_id IS NOT NULL`,
	},
	// Upgrade v12 → v13: split file_writer gate into write_file/edit_file/delete_file.
	// Adds deny_globs column with baseline default protecting common secrets/dotfile
	// patterns even when an agent has a broad folder-level write grant.
	// Note: SQLite cannot add a CHECK constraint to an existing column via ALTER TABLE.
	// The constraint lives in schema.sql for fresh DBs; upgraded DBs rely on app-layer
	// validation (RPC handler rejects unknown config_type values).
	// Existing rows with config_type='file_writer' are removed (greenfield — no live data).
	12: {
		`DELETE FROM agent_config_permissions WHERE config_type = 'file_writer'`,
		`ALTER TABLE agent_config_permissions ADD COLUMN deny_globs TEXT NOT NULL DEFAULT '[".env*","secrets/**",".git/**","*.key","*.pem"]'`,
	},
	// Upgrade v13 → v14: add contact_id FK to traces and spans tables.
	// Nullable; references channel_contacts(id) ON DELETE SET NULL so existing
	// traces/spans without a contact binding are unaffected. Enables per-contact
	// tracing queries for channel-originated agent invocations.
	13: {
		`ALTER TABLE traces ADD COLUMN contact_id TEXT REFERENCES channel_contacts(id) ON DELETE SET NULL`,
		`CREATE INDEX IF NOT EXISTS idx_traces_contact ON traces(contact_id) WHERE contact_id IS NOT NULL`,
		`ALTER TABLE spans  ADD COLUMN contact_id TEXT REFERENCES channel_contacts(id) ON DELETE SET NULL`,
		`CREATE INDEX IF NOT EXISTS idx_spans_contact  ON spans(contact_id)  WHERE contact_id IS NOT NULL`,
	},
	// Upgrade v14 → v15: add project_id FK to subagent_tasks.
	// Nullable; references projects(id) ON DELETE SET NULL. Sub-agent dispatch
	// inherits parent agent's project binding so subagent tasks stay scoped to
	// the same project as the parent run. Historical tasks without a project keep
	// NULL and are unaffected.
	14: {
		`ALTER TABLE subagent_tasks ADD COLUMN project_id TEXT REFERENCES projects(id) ON DELETE SET NULL`,
		`CREATE INDEX IF NOT EXISTS idx_subagent_tasks_project ON subagent_tasks(project_id) WHERE project_id IS NOT NULL`,
	},
	// Upgrade v15 → v16: memory 5D scope rebuild.
	// Adds contact_id + project_id FKs to memory_documents, memory_chunks,
	// episodic_summaries, kg_entities. Adds FS-backed columns (file_path,
	// content_hash, version) to memory_documents. Adds embedding BLOB +
	// embedding_norm to memory_chunks, embedding_cache, kg_entities,
	// vault_documents, skills, team_tasks. Rebuilds UNIQUE indexes to 5D.
	// Note: ON DELETE rule changes (SET NULL → CASCADE for team_id/user_id
	// on memory tables) cannot be applied via ALTER in SQLite; fresh DBs use
	// schema.sql which has the correct rules. Upgraded DBs retain old SET NULL
	// behavior for existing FKs — acceptable for Lite edition (single user,
	// no team sharing in production at this migration point).
	15: {
		// memory_documents: 5D scope + FS-backed columns; drop content (FS-backed now)
		`ALTER TABLE memory_documents ADD COLUMN contact_id   TEXT REFERENCES channel_contacts(id) ON DELETE SET NULL`,
		`ALTER TABLE memory_documents ADD COLUMN project_id   TEXT REFERENCES projects(id)         ON DELETE SET NULL`,
		`ALTER TABLE memory_documents ADD COLUMN file_path    VARCHAR(500) NOT NULL DEFAULT ''`,
		`ALTER TABLE memory_documents ADD COLUMN content_hash VARCHAR(64)  NOT NULL DEFAULT ''`,
		`ALTER TABLE memory_documents ADD COLUMN version      INT          NOT NULL DEFAULT 1`,
		`ALTER TABLE memory_documents DROP COLUMN content`,
		`CREATE INDEX IF NOT EXISTS idx_memdoc_contact ON memory_documents(contact_id) WHERE contact_id IS NOT NULL`,
		`CREATE INDEX IF NOT EXISTS idx_memdoc_project ON memory_documents(project_id) WHERE project_id IS NOT NULL`,
		`CREATE INDEX IF NOT EXISTS idx_memdoc_user    ON memory_documents(user_id)    WHERE user_id    IS NOT NULL`,
		// memory_chunks: 5D scope + halfvec BLOB
		`ALTER TABLE memory_chunks ADD COLUMN contact_id     TEXT REFERENCES channel_contacts(id) ON DELETE SET NULL`,
		`ALTER TABLE memory_chunks ADD COLUMN project_id     TEXT REFERENCES projects(id)         ON DELETE SET NULL`,
		`ALTER TABLE memory_chunks ADD COLUMN embedding      BLOB`,
		`ALTER TABLE memory_chunks ADD COLUMN embedding_norm REAL`,
		`CREATE INDEX IF NOT EXISTS idx_memchunk_contact ON memory_chunks(contact_id) WHERE contact_id IS NOT NULL`,
		`CREATE INDEX IF NOT EXISTS idx_memchunk_project ON memory_chunks(project_id) WHERE project_id IS NOT NULL`,
		// embedding_cache: halfvec BLOB
		`ALTER TABLE embedding_cache ADD COLUMN embedding      BLOB`,
		`ALTER TABLE embedding_cache ADD COLUMN embedding_norm REAL`,
		// episodic_summaries: 5D scope
		`ALTER TABLE episodic_summaries ADD COLUMN team_id    TEXT REFERENCES agent_teams(id)      ON DELETE SET NULL`,
		`ALTER TABLE episodic_summaries ADD COLUMN contact_id TEXT REFERENCES channel_contacts(id) ON DELETE SET NULL`,
		`ALTER TABLE episodic_summaries ADD COLUMN project_id TEXT REFERENCES projects(id)         ON DELETE SET NULL`,
		`CREATE INDEX IF NOT EXISTS idx_episodic_team    ON episodic_summaries(team_id)    WHERE team_id    IS NOT NULL`,
		`CREATE INDEX IF NOT EXISTS idx_episodic_contact ON episodic_summaries(contact_id) WHERE contact_id IS NOT NULL`,
		`CREATE INDEX IF NOT EXISTS idx_episodic_project ON episodic_summaries(project_id) WHERE project_id IS NOT NULL`,
		// kg_entities: 5D scope + halfvec BLOB
		`ALTER TABLE kg_entities ADD COLUMN contact_id     TEXT REFERENCES channel_contacts(id) ON DELETE SET NULL`,
		`ALTER TABLE kg_entities ADD COLUMN project_id     TEXT REFERENCES projects(id)         ON DELETE SET NULL`,
		`ALTER TABLE kg_entities ADD COLUMN embedding      BLOB`,
		`ALTER TABLE kg_entities ADD COLUMN embedding_norm REAL`,
		`CREATE INDEX IF NOT EXISTS idx_kg_contact ON kg_entities(contact_id) WHERE contact_id IS NOT NULL`,
		`CREATE INDEX IF NOT EXISTS idx_kg_project ON kg_entities(project_id) WHERE project_id IS NOT NULL`,
		// vault_documents: halfvec BLOB
		`ALTER TABLE vault_documents ADD COLUMN embedding      BLOB`,
		`ALTER TABLE vault_documents ADD COLUMN embedding_norm REAL`,
		// skills: halfvec BLOB
		`ALTER TABLE skills ADD COLUMN embedding      BLOB`,
		`ALTER TABLE skills ADD COLUMN embedding_norm REAL`,
		// team_tasks: halfvec BLOB
		`ALTER TABLE team_tasks ADD COLUMN embedding      BLOB`,
		`ALTER TABLE team_tasks ADD COLUMN embedding_norm REAL`,
	},
}

// dropColumnRE matches `ALTER TABLE <name> DROP COLUMN <col>` (with or without
// surrounding whitespace / trailing semicolon). Used to make DROP COLUMN
// idempotent: if the legacy column was never created on a given DB lineage,
// skip the drop instead of failing the migration. SQLite (modernc) does not
// accept `DROP COLUMN IF EXISTS`, so we PRAGMA-check column presence instead.
var dropColumnRE = regexp.MustCompile(`(?i)^\s*ALTER\s+TABLE\s+([A-Za-z_][A-Za-z0-9_]*)\s+DROP\s+COLUMN\s+([A-Za-z_][A-Za-z0-9_]*)\s*;?\s*$`)

func skipDropColumnIfMissing(tx *sql.Tx, stmt string) bool {
	m := dropColumnRE.FindStringSubmatch(stmt)
	if len(m) != 3 {
		return false
	}
	table, column := m[1], m[2]
	rows, err := tx.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false
		}
		if strings.EqualFold(name, column) {
			return false // column exists → run the DROP
		}
	}
	return true // column absent → skip
}

// EnsureSchema creates tables if they don't exist and applies incremental migrations.
//
// Flow:
//  1. Fresh DB (no schema_version row) → apply full schema.sql + set version = SchemaVersion
//  2. Existing DB with version < SchemaVersion → apply patches sequentially
//  3. Existing DB with version == SchemaVersion → no-op
func EnsureSchema(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (
		version INTEGER NOT NULL PRIMARY KEY
	)`); err != nil {
		return fmt.Errorf("create schema_version: %w", err)
	}

	var current int
	err := db.QueryRow("SELECT version FROM schema_version LIMIT 1").Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		// Fresh database — apply full schema.
		slog.Info("sqlite: applying initial schema", "version", SchemaVersion)
		tx, txErr := db.Begin()
		if txErr != nil {
			return fmt.Errorf("begin schema tx: %w", txErr)
		}
		if _, err := tx.Exec(schemaSQL); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply schema: %w", err)
		}
		if _, err := tx.Exec("INSERT INTO schema_version (version) VALUES (?)", SchemaVersion); err != nil {
			tx.Rollback()
			return fmt.Errorf("set schema version: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit schema tx: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}

	// Apply incremental migrations for existing DBs.
	if current < SchemaVersion {
		slog.Info("sqlite: migrating schema", "from", current, "to", SchemaVersion)
		for v := current; v < SchemaVersion; v++ {
			stmts, ok := migrations[v]
			if !ok {
				return fmt.Errorf("sqlite: missing migration for version %d → %d", v, v+1)
			}
			tx, txErr := db.Begin()
			if txErr != nil {
				return fmt.Errorf("begin migration tx v%d: %w", v, txErr)
			}
			for _, stmt := range stmts {
				if skipDropColumnIfMissing(tx, stmt) {
					slog.Info("sqlite: skip drop-column (already absent)", "version", v, "stmt", stmt[:min(60, len(stmt))])
					continue
				}
				if _, err := tx.Exec(stmt); err != nil {
					tx.Rollback()
					return fmt.Errorf("apply migration v%d stmt %q: %w", v, stmt[:min(40, len(stmt))], err)
				}
			}
			if _, err := tx.Exec(
				"UPDATE schema_version SET version = ? WHERE version = ?", v+1, v,
			); err != nil {
				tx.Rollback()
				return fmt.Errorf("update schema version v%d: %w", v, err)
			}
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("commit migration v%d: %w", v, err)
			}
			slog.Info("sqlite: applied migration", "version", v+1)
		}
	}

	return nil
}
