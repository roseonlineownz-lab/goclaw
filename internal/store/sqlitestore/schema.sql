-- GoClaw SQLite v4 Schema
-- Single-tenant, user-centric model. No tenant_id columns anywhere.
-- Fresh rewrite matching PG v4 migration 000001_initial.up.sql logical model.
--
-- Translation rules applied (SQLite type dialect):
--   UUID          → TEXT NOT NULL (36-char UUID v7 string, generated in Go)
--   TIMESTAMPTZ   → TEXT (ISO-8601/RFC3339, strftime default)
--   JSONB         → TEXT (JSON string, DEFAULT '{}' or '[]')
--   BYTEA/BLOB    → BLOB
--   BOOLEAN       → INTEGER (0/1 — SQLite has no native boolean)
--   vector(N)     → OMITTED (no sqlite-vec extension in standard lite build)
--   tsvector GENERATED → OMITTED (no FTS5 generated columns; FTS done at app layer)
--   CREATE EXTENSION / CREATE OR REPLACE FUNCTION → OMITTED (PG-only)
--   DEFAULT uuid_generate_v7() → OMITTED (Go layer generates UUID v7 before INSERT)
--   DEFAULT NOW() → DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
--   ON DELETE CASCADE / SET NULL → preserved
--   CHECK constraints → preserved (SQLite supports CHECK inline)
--   UNIQUE constraints → preserved
--   COALESCE in UNIQUE → CREATE UNIQUE INDEX (SQLite supports expressions in indexes)
--
-- vault_versions intentionally absent — versioning not needed in lite edition.

PRAGMA foreign_keys = ON;

-- ============================================================
-- LLM Providers (defined first — agent_heartbeats refs provider_id)
-- ============================================================

CREATE TABLE IF NOT EXISTS llm_providers (
    id            TEXT NOT NULL PRIMARY KEY,
    name          VARCHAR(50)  NOT NULL UNIQUE,
    display_name  VARCHAR(255),
    provider_type VARCHAR(30)  NOT NULL DEFAULT 'openai_compat',
    api_base      TEXT,
    api_key       TEXT,
    enabled       INTEGER      NOT NULL DEFAULT 1,
    settings      TEXT         NOT NULL DEFAULT '{}',
    metadata      TEXT         NOT NULL DEFAULT '{}',
    created_at    TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at    TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- ============================================================
-- Section 1: Core — users, user_sessions, agents, shares,
--             context files, profiles, overrides
-- ============================================================

CREATE TABLE IF NOT EXISTS users (
    id            TEXT         NOT NULL PRIMARY KEY,
    email         VARCHAR(255) NOT NULL UNIQUE,
    display_name  VARCHAR(255),
    password_hash TEXT         NOT NULL,
    role          VARCHAR(20)  NOT NULL DEFAULT 'member',
    status        VARCHAR(20)  NOT NULL DEFAULT 'active',
    deleted_at    TEXT,
    metadata      TEXT         NOT NULL DEFAULT '{}',
    -- Stable workspace folder identifier derived from email local-part.
    -- Generated once at insert time; immutable thereafter.
    user_key      VARCHAR(100) NOT NULL UNIQUE,
    -- Identity kind: 'human' (default) or 'channel' (merged channel contact).
    kind          VARCHAR(20)  NOT NULL DEFAULT 'human',
    -- Channel platform when kind='channel'. NULL when kind='human'.
    channel_type  VARCHAR(20)  NULL,
    created_at    TEXT         NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at    TEXT         NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    CONSTRAINT users_role_check         CHECK (role IN ('root', 'admin', 'member', 'viewer')),
    CONSTRAINT users_kind_check         CHECK (kind IN ('human', 'channel')),
    CONSTRAINT users_channel_type_shape CHECK (
        (kind = 'human'   AND channel_type IS NULL) OR
        (kind = 'channel' AND channel_type IS NOT NULL)
    )
);

-- Partial UNIQUE: at most one root user at any time.
CREATE UNIQUE INDEX IF NOT EXISTS users_only_one_root ON users(role) WHERE role = 'root';
CREATE INDEX IF NOT EXISTS idx_users_email  ON users(email);
CREATE INDEX IF NOT EXISTS idx_users_status ON users(status) WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS user_sessions (
    id                 TEXT NOT NULL PRIMARY KEY,
    user_id            TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    family_id          TEXT NOT NULL,
    refresh_token_hash TEXT NOT NULL UNIQUE,
    expires_at         TEXT NOT NULL,
    revoked_at         TEXT,
    metadata           TEXT NOT NULL DEFAULT '{}',
    created_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_user_sessions_user    ON user_sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_user_sessions_expires ON user_sessions(expires_at) WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS user_sessions_family_idx  ON user_sessions(family_id);

-- Mirror of password_reset_tokens (PG migration). Single-use time-bounded
-- tokens; raw value mailed once, only SHA-256 hex stored. used_at NULL = active.
CREATE TABLE IF NOT EXISTS password_reset_tokens (
    id         TEXT NOT NULL PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    used_at    TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_password_reset_token_hash ON password_reset_tokens(token_hash);
CREATE INDEX        IF NOT EXISTS idx_password_reset_user        ON password_reset_tokens(user_id);
CREATE INDEX        IF NOT EXISTS idx_password_reset_active      ON password_reset_tokens(token_hash) WHERE used_at IS NULL;

-- Mirror of PG projects table. Slug is immutable post-create (FS path coupling).
-- Archive via status change; no hard delete in rc1.
-- UUID stored as TEXT (generated in Go). JSONB stored as TEXT.
CREATE TABLE IF NOT EXISTS projects (
    id            TEXT         NOT NULL PRIMARY KEY,
    slug          VARCHAR(100) NOT NULL UNIQUE
                      CHECK (slug GLOB '[a-z0-9]*' AND slug NOT GLOB '*[^a-z0-9-]*' AND
                             length(slug) >= 3 AND length(slug) <= 100 AND
                             substr(slug, 1, 1) != '-' AND substr(slug, length(slug), 1) != '-'),
    owner_user_id TEXT         NOT NULL REFERENCES users(id) ON DELETE RESTRICT, -- transfer or archive project before user deletion
    status        VARCHAR(20)  NOT NULL DEFAULT 'active'
                      CHECK (status IN ('active', 'archived')),
    metadata      TEXT         NOT NULL DEFAULT '{}',
    created_at    TEXT         NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at    TEXT         NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_projects_owner  ON projects(owner_user_id);
CREATE INDEX IF NOT EXISTS idx_projects_status ON projects(status);

-- Project grants — controls user/team access to a project.
-- Exactly one of user_id/team_id must be set (XOR via CHECK).
-- SQLite UNIQUE treats NULLs as distinct by default, but the XOR CHECK guarantees
-- one non-NULL per row, so duplicate detection works on the non-NULL column.
CREATE TABLE IF NOT EXISTS project_grants (
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
);

-- PG UNIQUE NULLS NOT DISTINCT (project_id, user_id, team_id) is split into two partial indexes here
-- because SQLite has no NULLS-NOT-DISTINCT primitive. The XOR CHECK guarantees columns are disjoint.
CREATE UNIQUE INDEX IF NOT EXISTS idx_project_grants_unique_user
    ON project_grants(project_id, user_id) WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_project_grants_unique_team
    ON project_grants(project_id, team_id) WHERE team_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_project_grants_project ON project_grants(project_id);
CREATE INDEX IF NOT EXISTS idx_project_grants_user    ON project_grants(user_id)  WHERE user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_project_grants_team    ON project_grants(team_id)  WHERE team_id IS NOT NULL;

-- Note: tsv (tsvector) column omitted — no FTS5 generated column in SQLite.
CREATE TABLE IF NOT EXISTS agents (
    id                    TEXT         NOT NULL PRIMARY KEY,
    agent_key             VARCHAR(100) NOT NULL,
    display_name          VARCHAR(255),
    owner_id              VARCHAR(255) NOT NULL,
    owner_user_id         TEXT         REFERENCES users(id) ON DELETE SET NULL,
    provider              VARCHAR(50)  NOT NULL DEFAULT 'openrouter',
    model                 VARCHAR(200) NOT NULL,
    context_window        INT          NOT NULL DEFAULT 200000,
    max_tool_iterations   INT          NOT NULL DEFAULT 20,
    workspace             TEXT         NOT NULL DEFAULT '.',
    restrict_to_workspace INTEGER      NOT NULL DEFAULT 1,
    tools_config          TEXT         NOT NULL DEFAULT '{}',
    sandbox_config        TEXT,
    subagents_config      TEXT,
    memory_config         TEXT,
    compaction_config     TEXT,
    context_pruning       TEXT,
    other_config          TEXT         NOT NULL DEFAULT '{}',
    emoji                 TEXT         NOT NULL DEFAULT '',
    agent_description     TEXT         NOT NULL DEFAULT '',
    thinking_level        TEXT         NOT NULL DEFAULT '',
    max_tokens            INT          NOT NULL DEFAULT 0,
    self_evolve           INTEGER      NOT NULL DEFAULT 0,
    skill_evolve          INTEGER      NOT NULL DEFAULT 0,
    skill_nudge_interval  INT          NOT NULL DEFAULT 0,
    reasoning_config      TEXT         NOT NULL DEFAULT '{}',
    share_workspace       INTEGER      NOT NULL DEFAULT 0,
    share_memory          INTEGER      NOT NULL DEFAULT 0,
    chatgpt_oauth_routing TEXT         NOT NULL DEFAULT '{}',
    shell_deny_groups     TEXT         NOT NULL DEFAULT '{}',
    kg_dedup_config       TEXT         NOT NULL DEFAULT '{}',
    is_default            INTEGER      NOT NULL DEFAULT 0,
    status                VARCHAR(20)  DEFAULT 'active',
    frontmatter           TEXT,
    budget_monthly_cents  INTEGER,
    metadata              TEXT         NOT NULL DEFAULT '{}',
    created_at            TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at            TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    deleted_at            TEXT
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_agents_agent_key_active ON agents(agent_key) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_agents_owner      ON agents(owner_id)      WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_agents_owner_user ON agents(owner_user_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_agents_status     ON agents(status)        WHERE deleted_at IS NULL;

-- Mirror of PG agent_shares. agent_teams FK is added later in this file
-- because the agent_teams table is declared after agent_shares (forward-ref
-- pattern already used by other tables here). SQLite does not enforce CHECK
-- constraints for forward FKs at parse time, so the inline FK reference works.
CREATE TABLE IF NOT EXISTS agent_shares (
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
);

CREATE INDEX IF NOT EXISTS idx_agent_shares_agent ON agent_shares(agent_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_shares_user
    ON agent_shares(agent_id, shared_with_user_id)
    WHERE shared_with_user_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_shares_team
    ON agent_shares(agent_id, shared_with_team_id)
    WHERE shared_with_team_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS agent_context_files (
    id         TEXT         NOT NULL PRIMARY KEY,
    agent_id   TEXT         NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    file_name  VARCHAR(255) NOT NULL,
    content    TEXT         NOT NULL DEFAULT '',
    created_at TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE(agent_id, file_name)
);

CREATE TABLE IF NOT EXISTS user_context_files (
    id         TEXT         NOT NULL PRIMARY KEY,
    agent_id   TEXT         NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    user_id    TEXT         NOT NULL REFERENCES users(id)  ON DELETE CASCADE,
    file_name  VARCHAR(255) NOT NULL,
    content    TEXT         NOT NULL DEFAULT '',
    created_at TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE(agent_id, user_id, file_name)
);

CREATE INDEX IF NOT EXISTS idx_user_context_files_user ON user_context_files(user_id);

CREATE TABLE IF NOT EXISTS user_agent_profiles (
    agent_id      TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    user_id       TEXT NOT NULL REFERENCES users(id)  ON DELETE CASCADE,
    workspace     TEXT,
    metadata      TEXT DEFAULT '{}',
    first_seen_at TEXT DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    last_seen_at  TEXT DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    PRIMARY KEY (agent_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_user_agent_profiles_user ON user_agent_profiles(user_id);

CREATE TABLE IF NOT EXISTS user_agent_overrides (
    id         TEXT        NOT NULL PRIMARY KEY,
    agent_id   TEXT        NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    user_id    TEXT        NOT NULL REFERENCES users(id)  ON DELETE CASCADE,
    provider   VARCHAR(50),
    model      VARCHAR(200),
    settings   TEXT        NOT NULL DEFAULT '{}',
    created_at TEXT        DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT        DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE(agent_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_user_agent_overrides_user ON user_agent_overrides(user_id);

-- ============================================================
-- Section 2: API Keys & Agent Links
-- ============================================================

CREATE TABLE IF NOT EXISTS api_keys (
    id            TEXT         NOT NULL PRIMARY KEY,
    name          VARCHAR(100) NOT NULL,
    prefix        VARCHAR(8)   NOT NULL,
    key_hash      VARCHAR(64)  NOT NULL UNIQUE,
    scopes        TEXT         NOT NULL DEFAULT '[]',
    expires_at    TEXT,
    last_used_at  TEXT,
    revoked       INTEGER      NOT NULL DEFAULT 0,
    created_by    VARCHAR(255),
    owner_user_id TEXT         NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at    TEXT         NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at    TEXT         NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_api_keys_key_hash ON api_keys(key_hash) WHERE NOT revoked;
CREATE INDEX IF NOT EXISTS idx_api_keys_prefix   ON api_keys(prefix);
CREATE INDEX IF NOT EXISTS idx_api_keys_owner    ON api_keys(owner_user_id);

-- team_id FK to agent_teams added after agent_teams is defined (forward reference).
-- SQLite enforces FKs at DML time (INSERT/UPDATE), not DDL time, so the column
-- declaration without REFERENCES is safe; application layer maintains integrity.
CREATE TABLE IF NOT EXISTS agent_links (
    id              TEXT         NOT NULL PRIMARY KEY,
    source_agent_id TEXT         NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    target_agent_id TEXT         NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    direction       VARCHAR(20)  NOT NULL DEFAULT 'outbound',
    description     TEXT,
    max_concurrent  INT          NOT NULL DEFAULT 3,
    settings        TEXT         NOT NULL DEFAULT '{}',
    status          VARCHAR(20)  NOT NULL DEFAULT 'active',
    created_by      VARCHAR(255) NOT NULL,
    team_id         TEXT,
    metadata        TEXT         NOT NULL DEFAULT '{}',
    created_at      TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at      TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE(source_agent_id, target_agent_id),
    CHECK (source_agent_id != target_agent_id)
);

CREATE INDEX IF NOT EXISTS idx_agent_links_source ON agent_links(source_agent_id) WHERE status = 'active';
CREATE INDEX IF NOT EXISTS idx_agent_links_target ON agent_links(target_agent_id) WHERE status = 'active';

-- ============================================================
-- Section 3: Teams & Collaboration
-- ============================================================

CREATE TABLE IF NOT EXISTS agent_teams (
    id            TEXT         NOT NULL PRIMARY KEY,
    name          VARCHAR(255) NOT NULL,
    lead_agent_id TEXT         NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    description   TEXT,
    status        VARCHAR(20)  NOT NULL DEFAULT 'active',
    settings      TEXT         NOT NULL DEFAULT '{}',
    created_by    VARCHAR(255) NOT NULL,
    owner_user_id TEXT         REFERENCES users(id) ON DELETE SET NULL,
    -- Stable workspace folder identifier derived from team name.
    -- Generated once at insert time; immutable thereafter.
    team_key      VARCHAR(100) NOT NULL UNIQUE,
    metadata      TEXT         NOT NULL DEFAULT '{}',
    created_at    TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at    TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_agent_teams_owner ON agent_teams(owner_user_id);

CREATE TABLE IF NOT EXISTS agent_team_members (
    team_id   TEXT        NOT NULL REFERENCES agent_teams(id) ON DELETE CASCADE,
    agent_id  TEXT        NOT NULL REFERENCES agents(id)      ON DELETE CASCADE,
    role      VARCHAR(20) NOT NULL DEFAULT 'member',
    joined_at TEXT        DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    PRIMARY KEY (team_id, agent_id)
);

CREATE TABLE IF NOT EXISTS team_user_grants (
    id         TEXT         NOT NULL PRIMARY KEY,
    team_id    TEXT         NOT NULL REFERENCES agent_teams(id) ON DELETE CASCADE,
    user_id    TEXT         NOT NULL REFERENCES users(id)       ON DELETE CASCADE,
    role       VARCHAR(50)  NOT NULL DEFAULT 'viewer',
    granted_by VARCHAR(255),
    created_at TEXT         NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE(team_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_team_user_grants_user ON team_user_grants(user_id);
CREATE INDEX IF NOT EXISTS idx_team_user_grants_team ON team_user_grants(team_id);

-- User↔team membership, distinct from agent↔team in agent_team_members.
-- Roles (viewer/member/admin) control what a user can do as a team member.
-- added_by preserved for audit; SET NULL on granter delete keeps the row.
CREATE TABLE IF NOT EXISTS team_user_members (
    team_id    TEXT        NOT NULL REFERENCES agent_teams(id) ON DELETE CASCADE,
    user_id    TEXT        NOT NULL REFERENCES users(id)       ON DELETE CASCADE,
    role       VARCHAR(20) NOT NULL CHECK (role IN ('viewer', 'member', 'admin')),
    added_by   TEXT        REFERENCES users(id) ON DELETE SET NULL,
    created_at TEXT        NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    PRIMARY KEY (team_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_team_user_members_user ON team_user_members(user_id);

-- Note: tsv (tsvector) and embedding (vector) columns omitted.
CREATE TABLE IF NOT EXISTS team_tasks (
    id                  TEXT         NOT NULL PRIMARY KEY,
    team_id             TEXT         NOT NULL REFERENCES agent_teams(id) ON DELETE CASCADE,
    subject             VARCHAR(500) NOT NULL,
    description         TEXT,
    status              VARCHAR(20)  NOT NULL DEFAULT 'pending',
    owner_agent_id      TEXT         REFERENCES agents(id) ON DELETE SET NULL,
    blocked_by          TEXT         NOT NULL DEFAULT '[]',
    priority            INT          NOT NULL DEFAULT 0,
    result              TEXT,
    metadata            TEXT         NOT NULL DEFAULT '{}',
    user_id             TEXT         REFERENCES users(id) ON DELETE SET NULL,
    channel             VARCHAR(50),
    task_type           VARCHAR(30)  NOT NULL DEFAULT 'general',
    task_number         INT          NOT NULL DEFAULT 0,
    identifier          VARCHAR(20),
    created_by_agent_id TEXT         REFERENCES agents(id) ON DELETE SET NULL,
    assignee_user_id    TEXT         REFERENCES users(id) ON DELETE SET NULL,
    parent_id           TEXT         REFERENCES team_tasks(id) ON DELETE SET NULL,
    chat_id             VARCHAR(255) DEFAULT '',
    locked_at           TEXT,
    lock_expires_at     TEXT,
    progress_percent    INT          DEFAULT 0 CHECK (progress_percent BETWEEN 0 AND 100),
    progress_step       TEXT,
    followup_at         TEXT,
    followup_count      INT          NOT NULL DEFAULT 0,
    followup_max        INT          NOT NULL DEFAULT 0,
    followup_message    TEXT,
    followup_channel    VARCHAR(60),
    followup_chat_id    VARCHAR(255),
    confidence_score    REAL,
    comment_count       INT          NOT NULL DEFAULT 0,
    attachment_count    INT          NOT NULL DEFAULT 0,
    custom_scope        TEXT,
    embedding           BLOB,
    embedding_norm      REAL,
    created_at          TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at          TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_team_tasks_team       ON team_tasks(team_id);
CREATE INDEX IF NOT EXISTS idx_team_tasks_status     ON team_tasks(team_id, status);
CREATE INDEX IF NOT EXISTS idx_team_tasks_user_scope ON team_tasks(team_id, user_id) WHERE user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_tt_parent             ON team_tasks(parent_id)        WHERE parent_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_tt_scope              ON team_tasks(team_id, channel, chat_id);
CREATE INDEX IF NOT EXISTS idx_tt_type               ON team_tasks(team_id, task_type);
CREATE INDEX IF NOT EXISTS idx_tt_lock               ON team_tasks(lock_expires_at)  WHERE lock_expires_at IS NOT NULL AND status = 'in_progress';
CREATE UNIQUE INDEX IF NOT EXISTS idx_tt_identifier  ON team_tasks(team_id, identifier) WHERE identifier IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_tt_followup           ON team_tasks(followup_at)      WHERE followup_at IS NOT NULL AND status = 'in_progress';
CREATE INDEX IF NOT EXISTS idx_tt_owner_status       ON team_tasks(team_id, owner_agent_id, status);

CREATE TABLE IF NOT EXISTS team_task_comments (
    id               TEXT         NOT NULL PRIMARY KEY,
    task_id          TEXT         NOT NULL REFERENCES team_tasks(id) ON DELETE CASCADE,
    agent_id         TEXT         REFERENCES agents(id)              ON DELETE SET NULL,
    user_id          TEXT         REFERENCES users(id)               ON DELETE SET NULL,
    content          TEXT         NOT NULL,
    metadata         TEXT         DEFAULT '{}',
    comment_type     VARCHAR(20)  NOT NULL DEFAULT 'note',
    confidence_score REAL,
    custom_scope     TEXT,
    created_at       TEXT         NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_ttc_task ON team_task_comments(task_id);

CREATE TABLE IF NOT EXISTS team_task_events (
    id           TEXT         NOT NULL PRIMARY KEY,
    task_id      TEXT         NOT NULL REFERENCES team_tasks(id) ON DELETE CASCADE,
    event_type   VARCHAR(30)  NOT NULL,
    actor_type   VARCHAR(10)  NOT NULL,
    actor_id     VARCHAR(255) NOT NULL,
    data         TEXT,
    custom_scope TEXT,
    created_at   TEXT         NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_tte_task ON team_task_events(task_id);

CREATE TABLE IF NOT EXISTS team_task_attachments (
    id                   TEXT         NOT NULL PRIMARY KEY,
    task_id              TEXT         NOT NULL REFERENCES team_tasks(id)  ON DELETE CASCADE,
    team_id              TEXT         NOT NULL REFERENCES agent_teams(id) ON DELETE CASCADE,
    chat_id              VARCHAR(255) NOT NULL DEFAULT '',
    path                 TEXT         NOT NULL,
    base_name            TEXT         NOT NULL DEFAULT '',
    file_size            INTEGER      NOT NULL DEFAULT 0,
    mime_type            VARCHAR(100) DEFAULT '',
    created_by_agent_id  TEXT         REFERENCES agents(id),
    created_by_sender_id VARCHAR(255) DEFAULT '',
    metadata             TEXT         NOT NULL DEFAULT '{}',
    custom_scope         TEXT,
    created_at           TEXT         NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE(task_id, path)
);

CREATE INDEX IF NOT EXISTS idx_tta_task     ON team_task_attachments(task_id);
CREATE INDEX IF NOT EXISTS idx_tta_team     ON team_task_attachments(team_id);
CREATE INDEX IF NOT EXISTS idx_tta_basename ON team_task_attachments(team_id, base_name);

-- ============================================================
-- Section 4: Agent Sessions (renamed from v3 "sessions")
-- ============================================================

CREATE TABLE IF NOT EXISTS agent_sessions (
    id                            TEXT         NOT NULL PRIMARY KEY,
    session_key                   VARCHAR(500) NOT NULL,
    agent_id                      TEXT         REFERENCES agents(id)      ON DELETE CASCADE,
    user_id                       TEXT         REFERENCES users(id)       ON DELETE SET NULL,
    messages                      TEXT         NOT NULL DEFAULT '[]',
    summary                       TEXT,
    model                         VARCHAR(200),
    provider                      VARCHAR(50),
    channel                       VARCHAR(50),
    input_tokens                  INTEGER      NOT NULL DEFAULT 0,
    output_tokens                 INTEGER      NOT NULL DEFAULT 0,
    compaction_count              INT          NOT NULL DEFAULT 0,
    memory_flush_compaction_count INT          NOT NULL DEFAULT 0,
    memory_flush_at               INTEGER      DEFAULT 0,
    label                         VARCHAR(500),
    spawned_by                    VARCHAR(200),
    spawn_depth                   INT          NOT NULL DEFAULT 0,
    metadata                      TEXT         DEFAULT '{}',
    team_id                       TEXT         REFERENCES agent_teams(id) ON DELETE SET NULL,
    project_id                    TEXT         REFERENCES projects(id)    ON DELETE SET NULL,
    created_at                    TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at                    TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_sessions_session_key ON agent_sessions(session_key);
CREATE INDEX IF NOT EXISTS idx_agent_sessions_agent      ON agent_sessions(agent_id);
CREATE INDEX IF NOT EXISTS idx_agent_sessions_user       ON agent_sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_agent_sessions_updated    ON agent_sessions(updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_agent_sessions_user_agent ON agent_sessions(user_id, agent_id);
CREATE INDEX IF NOT EXISTS idx_agent_sessions_team       ON agent_sessions(team_id)    WHERE team_id    IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_agent_sessions_project    ON agent_sessions(project_id) WHERE project_id IS NOT NULL;

-- ============================================================
-- Section 5: Memory
-- ============================================================

-- Note: halfvec embedding stored as BLOB (6144 bytes, float16 little-endian).
CREATE TABLE IF NOT EXISTS memory_documents (
    id           TEXT         NOT NULL PRIMARY KEY,
    agent_id     TEXT         NOT NULL REFERENCES agents(id)           ON DELETE CASCADE,
    team_id      TEXT         REFERENCES agent_teams(id)               ON DELETE CASCADE,
    user_id      TEXT         REFERENCES users(id)                     ON DELETE CASCADE,
    contact_id   TEXT         REFERENCES channel_contacts(id)          ON DELETE CASCADE,
    project_id   TEXT         REFERENCES projects(id)                  ON DELETE SET NULL,
    path         VARCHAR(500) NOT NULL,
    file_path    VARCHAR(500) NOT NULL DEFAULT '',
    content_hash VARCHAR(64)  NOT NULL DEFAULT '',
    version      INT          NOT NULL DEFAULT 1,
    metadata     TEXT         NOT NULL DEFAULT '{}',
    created_at   TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at   TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_memdoc_unique ON memory_documents(
    agent_id,
    COALESCE(team_id,    ''),
    COALESCE(user_id,    ''),
    COALESCE(contact_id, ''),
    COALESCE(project_id, ''),
    path
);
CREATE INDEX IF NOT EXISTS idx_memdoc_agent_user ON memory_documents(agent_id, user_id);
CREATE INDEX IF NOT EXISTS idx_memdoc_team       ON memory_documents(team_id)    WHERE team_id    IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_memdoc_user       ON memory_documents(user_id)    WHERE user_id    IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_memdoc_contact    ON memory_documents(contact_id) WHERE contact_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_memdoc_project    ON memory_documents(project_id) WHERE project_id IS NOT NULL;

-- Note: embedding stored as BLOB (halfvec(3072) = 6144 bytes float16); tsv omitted.
CREATE TABLE IF NOT EXISTS memory_chunks (
    id             TEXT         NOT NULL PRIMARY KEY,
    agent_id       TEXT         NOT NULL REFERENCES agents(id)           ON DELETE CASCADE,
    document_id    TEXT         REFERENCES memory_documents(id)          ON DELETE CASCADE,
    team_id        TEXT         REFERENCES agent_teams(id)               ON DELETE CASCADE,
    user_id        TEXT         REFERENCES users(id)                     ON DELETE CASCADE,
    contact_id     TEXT         REFERENCES channel_contacts(id)          ON DELETE CASCADE,
    project_id     TEXT         REFERENCES projects(id)                  ON DELETE SET NULL,
    path           TEXT         NOT NULL,
    start_line     INT          NOT NULL DEFAULT 0,
    end_line       INT          NOT NULL DEFAULT 0,
    hash           VARCHAR(64)  NOT NULL,
    text           TEXT         NOT NULL,
    embedding      BLOB,
    embedding_norm REAL,
    created_at     TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at     TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_mem_agent_user   ON memory_chunks(agent_id, user_id);
CREATE INDEX IF NOT EXISTS idx_mem_global       ON memory_chunks(agent_id)    WHERE user_id    IS NULL;
CREATE INDEX IF NOT EXISTS idx_mem_document     ON memory_chunks(document_id);
CREATE INDEX IF NOT EXISTS idx_memchunk_team    ON memory_chunks(team_id)     WHERE team_id    IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_memchunk_contact ON memory_chunks(contact_id)  WHERE contact_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_memchunk_project ON memory_chunks(project_id)  WHERE project_id IS NOT NULL;

-- Note: embedding stored as BLOB (halfvec(3072)).
CREATE TABLE IF NOT EXISTS embedding_cache (
    hash           VARCHAR(64)  NOT NULL,
    provider       VARCHAR(50)  NOT NULL,
    model          VARCHAR(200) NOT NULL,
    dims           INT          NOT NULL DEFAULT 0,
    embedding      BLOB,
    embedding_norm REAL,
    created_at     TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at     TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    PRIMARY KEY (hash, provider, model)
);

CREATE TABLE IF NOT EXISTS episodic_summaries (
    id               TEXT         NOT NULL PRIMARY KEY,
    agent_id         TEXT         NOT NULL REFERENCES agents(id)          ON DELETE CASCADE,
    user_id          TEXT         REFERENCES users(id)                    ON DELETE CASCADE,
    team_id          TEXT         REFERENCES agent_teams(id)              ON DELETE CASCADE,
    contact_id       TEXT         REFERENCES channel_contacts(id)         ON DELETE CASCADE,
    project_id       TEXT         REFERENCES projects(id)                 ON DELETE SET NULL,
    session_key      TEXT         NOT NULL,
    summary          TEXT         NOT NULL,
    l0_abstract      TEXT         NOT NULL DEFAULT '',
    key_topics       TEXT         NOT NULL DEFAULT '[]',
    source_type      TEXT         NOT NULL DEFAULT 'session',
    source_id        TEXT,
    turn_count       INTEGER      NOT NULL DEFAULT 0,
    token_count      INTEGER      NOT NULL DEFAULT 0,
    created_at       TEXT         NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    expires_at       TEXT,
    promoted_at      TEXT,
    recall_count     INTEGER      NOT NULL DEFAULT 0,
    recall_score     REAL         NOT NULL DEFAULT 0,
    last_recalled_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_episodic_agent_user ON episodic_summaries(agent_id, user_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_episodic_source_dedup ON episodic_summaries(
    agent_id,
    COALESCE(user_id,    ''),
    COALESCE(team_id,    ''),
    COALESCE(contact_id, ''),
    COALESCE(project_id, ''),
    source_id
) WHERE source_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_episodic_team    ON episodic_summaries(team_id)    WHERE team_id    IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_episodic_contact ON episodic_summaries(contact_id) WHERE contact_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_episodic_project ON episodic_summaries(project_id) WHERE project_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_episodic_unpromoted ON episodic_summaries(agent_id, user_id, created_at)
    WHERE promoted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_episodic_recall_unpromoted ON episodic_summaries(agent_id, user_id, recall_score DESC)
    WHERE promoted_at IS NULL;

-- ============================================================
-- Section 6: Knowledge Graph
-- ============================================================

-- Note: embedding stored as BLOB (halfvec(3072) = 6144 bytes float16).
CREATE TABLE IF NOT EXISTS kg_entities (
    id          TEXT         NOT NULL PRIMARY KEY,
    agent_id    TEXT         NOT NULL REFERENCES agents(id)           ON DELETE CASCADE,
    user_id     TEXT         REFERENCES users(id)                     ON DELETE CASCADE,
    team_id     TEXT         REFERENCES agent_teams(id)               ON DELETE CASCADE,
    contact_id  TEXT         REFERENCES channel_contacts(id)          ON DELETE CASCADE,
    project_id  TEXT         REFERENCES projects(id)                  ON DELETE SET NULL,
    external_id VARCHAR(255) NOT NULL,
    name        TEXT         NOT NULL,
    entity_type VARCHAR(100) NOT NULL,
    description TEXT         DEFAULT '',
    properties  TEXT         DEFAULT '{}',
    source_id   VARCHAR(255) DEFAULT '',
    confidence  REAL         NOT NULL DEFAULT 1.0,
    embedding      BLOB,
    embedding_norm REAL,
    created_at  TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at  TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    valid_from  TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    valid_until TEXT
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_kg_entities_unique ON kg_entities(agent_id, COALESCE(user_id, ''), external_id);
CREATE INDEX IF NOT EXISTS idx_kg_entities_scope   ON kg_entities(agent_id, user_id);
CREATE INDEX IF NOT EXISTS idx_kg_entities_type    ON kg_entities(agent_id, user_id, entity_type);
CREATE INDEX IF NOT EXISTS idx_kg_entities_current ON kg_entities(agent_id, user_id) WHERE valid_until IS NULL;
CREATE INDEX IF NOT EXISTS idx_kg_entities_team    ON kg_entities(team_id)    WHERE team_id    IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_kg_contact          ON kg_entities(contact_id) WHERE contact_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_kg_project          ON kg_entities(project_id) WHERE project_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS kg_relations (
    id               TEXT         NOT NULL PRIMARY KEY,
    agent_id         TEXT         NOT NULL REFERENCES agents(id)      ON DELETE CASCADE,
    user_id          TEXT         REFERENCES users(id)                ON DELETE SET NULL,
    source_entity_id TEXT         NOT NULL REFERENCES kg_entities(id) ON DELETE CASCADE,
    relation_type    VARCHAR(200) NOT NULL,
    target_entity_id TEXT         NOT NULL REFERENCES kg_entities(id) ON DELETE CASCADE,
    confidence       REAL         NOT NULL DEFAULT 1.0,
    properties       TEXT         DEFAULT '{}',
    team_id          TEXT         REFERENCES agent_teams(id)          ON DELETE SET NULL,
    created_at       TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    valid_from       TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    valid_until      TEXT
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_kg_relations_unique ON kg_relations(agent_id, COALESCE(user_id, ''), source_entity_id, relation_type, target_entity_id);
CREATE INDEX IF NOT EXISTS idx_kg_relations_source        ON kg_relations(source_entity_id, relation_type);
CREATE INDEX IF NOT EXISTS idx_kg_relations_target        ON kg_relations(target_entity_id);
CREATE INDEX IF NOT EXISTS idx_kg_relations_current       ON kg_relations(agent_id, user_id) WHERE valid_until IS NULL;
CREATE INDEX IF NOT EXISTS idx_kg_relations_team          ON kg_relations(team_id)           WHERE team_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS kg_dedup_candidates (
    id          TEXT         NOT NULL PRIMARY KEY,
    agent_id    TEXT         NOT NULL REFERENCES agents(id)      ON DELETE CASCADE,
    user_id     TEXT         REFERENCES users(id)                ON DELETE SET NULL,
    entity_a_id TEXT         NOT NULL REFERENCES kg_entities(id) ON DELETE CASCADE,
    entity_b_id TEXT         NOT NULL REFERENCES kg_entities(id) ON DELETE CASCADE,
    similarity  REAL         NOT NULL,
    status      VARCHAR(20)  NOT NULL DEFAULT 'pending',
    created_at  TEXT         NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE(entity_a_id, entity_b_id)
);

CREATE INDEX IF NOT EXISTS idx_kg_dedup_agent ON kg_dedup_candidates(agent_id, status);

-- ============================================================
-- Section 7: Vault (vault_versions absent — not needed in lite edition)
-- ============================================================

-- Note: embedding (vector) and tsv (tsvector) columns omitted.
-- Scope consistency enforced via BEFORE INSERT/UPDATE triggers below
-- (SQLite cannot express multi-column CHECK constraints spanning expressions).
CREATE TABLE IF NOT EXISTS vault_documents (
    id             TEXT NOT NULL PRIMARY KEY,
    agent_id       TEXT REFERENCES agents(id)      ON DELETE SET NULL,
    owner_user_id  TEXT REFERENCES users(id)       ON DELETE SET NULL,
    team_id        TEXT REFERENCES agent_teams(id) ON DELETE SET NULL,
    chat_id        TEXT,
    scope          TEXT NOT NULL DEFAULT 'personal'
                   CHECK (scope IN ('personal', 'team', 'shared', 'custom')),
    custom_scope   TEXT,
    path           TEXT NOT NULL,
    path_basename  TEXT NOT NULL DEFAULT '',
    title          TEXT NOT NULL DEFAULT '',
    doc_type       TEXT NOT NULL DEFAULT 'note',
    content_hash   TEXT NOT NULL DEFAULT '',
    summary        TEXT NOT NULL DEFAULT '',
    metadata       TEXT DEFAULT '{}',
    embedding      BLOB,
    embedding_norm REAL,
    created_at     TEXT DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at     TEXT DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_vault_docs_unique_path
    ON vault_documents(scope, COALESCE(custom_scope, ''), path, COALESCE(owner_user_id, ''));
CREATE INDEX IF NOT EXISTS idx_vault_docs_agent_scope ON vault_documents(agent_id, scope);
CREATE INDEX IF NOT EXISTS idx_vault_docs_type        ON vault_documents(agent_id, doc_type);
CREATE INDEX IF NOT EXISTS idx_vault_docs_hash        ON vault_documents(content_hash);
CREATE INDEX IF NOT EXISTS idx_vault_docs_team        ON vault_documents(team_id);
CREATE INDEX IF NOT EXISTS idx_vault_docs_team_chat   ON vault_documents(team_id, chat_id) WHERE team_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_vault_docs_basename    ON vault_documents(path_basename);
CREATE INDEX IF NOT EXISTS idx_vault_docs_path_prefix ON vault_documents(path);

-- Scope consistency triggers.
CREATE TRIGGER IF NOT EXISTS trg_vault_docs_scope_consistency_ins
  BEFORE INSERT ON vault_documents
  FOR EACH ROW
  WHEN NOT (
    (NEW.scope = 'personal' AND NEW.agent_id IS NOT NULL AND NEW.team_id IS NULL) OR
    (NEW.scope = 'team'     AND NEW.team_id  IS NOT NULL AND NEW.agent_id IS NULL) OR
    (NEW.scope = 'shared'   AND NEW.agent_id IS NULL     AND NEW.team_id  IS NULL) OR
    NEW.scope = 'custom'
  )
  BEGIN
    SELECT RAISE(ABORT, 'vault_documents_scope_consistency violation');
  END;

CREATE TRIGGER IF NOT EXISTS trg_vault_docs_scope_consistency_upd
  BEFORE UPDATE OF scope, agent_id, team_id ON vault_documents
  FOR EACH ROW
  WHEN NOT (
    (NEW.scope = 'personal' AND NEW.agent_id IS NOT NULL AND NEW.team_id IS NULL) OR
    (NEW.scope = 'team'     AND NEW.team_id  IS NOT NULL AND NEW.agent_id IS NULL) OR
    (NEW.scope = 'shared'   AND NEW.agent_id IS NULL     AND NEW.team_id  IS NULL) OR
    NEW.scope = 'custom'
  )
  BEGIN
    SELECT RAISE(ABORT, 'vault_documents_scope_consistency violation');
  END;

CREATE TABLE IF NOT EXISTS vault_links (
    id           TEXT NOT NULL PRIMARY KEY,
    from_doc_id  TEXT NOT NULL REFERENCES vault_documents(id) ON DELETE CASCADE,
    to_doc_id    TEXT NOT NULL REFERENCES vault_documents(id) ON DELETE CASCADE,
    link_type    TEXT NOT NULL DEFAULT 'wikilink',
    context      TEXT NOT NULL DEFAULT '',
    metadata     TEXT NOT NULL DEFAULT '{}',
    custom_scope TEXT,
    created_at   TEXT DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE(from_doc_id, to_doc_id, link_type)
);

CREATE INDEX IF NOT EXISTS idx_vault_links_from ON vault_links(from_doc_id);
CREATE INDEX IF NOT EXISTS idx_vault_links_to   ON vault_links(to_doc_id);

-- ============================================================
-- Section 8: Skills
-- ============================================================

-- Note: embedding (vector) column omitted.
CREATE TABLE IF NOT EXISTS skills (
    id              TEXT         NOT NULL PRIMARY KEY,
    name            VARCHAR(255) NOT NULL,
    slug            VARCHAR(255) NOT NULL,
    description     TEXT,
    owner_id        VARCHAR(255) NOT NULL,
    visibility      VARCHAR(10)  NOT NULL DEFAULT 'private',
    version         INT          NOT NULL DEFAULT 1,
    status          VARCHAR(20)  NOT NULL DEFAULT 'active',
    frontmatter     TEXT         NOT NULL DEFAULT '{}',
    file_path       TEXT         NOT NULL,
    file_size       INTEGER      NOT NULL DEFAULT 0,
    file_hash       VARCHAR(64),
    tags            TEXT,
    source          VARCHAR(20)  NOT NULL DEFAULT 'user-uploaded'
                    CHECK (source IN ('builtin', 'hub-verified', 'hub-unverified', 'agent-created', 'user-uploaded')),
    deps            TEXT         NOT NULL DEFAULT '{}',
    enabled         INTEGER      NOT NULL DEFAULT 1,
    last_used_at    TEXT,
    last_viewed_at  TEXT,
    last_patched_at TEXT,
    pinned          INTEGER      NOT NULL DEFAULT 0,
    usage_count     INTEGER      NOT NULL DEFAULT 0,
    metadata        TEXT         NOT NULL DEFAULT '{}',
    embedding       BLOB,
    embedding_norm  REAL,
    created_at      TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at      TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_skills_slug       ON skills(slug)       WHERE status = 'active';
CREATE INDEX IF NOT EXISTS idx_skills_owner             ON skills(owner_id);
CREATE INDEX IF NOT EXISTS idx_skills_visibility        ON skills(visibility) WHERE status = 'active';
CREATE INDEX IF NOT EXISTS idx_skills_source            ON skills(source)     WHERE status = 'active';
CREATE INDEX IF NOT EXISTS idx_skills_enabled           ON skills(enabled)    WHERE enabled = 0;
CREATE INDEX IF NOT EXISTS idx_skills_pinned            ON skills(pinned)     WHERE pinned = 1;

CREATE TABLE IF NOT EXISTS skill_agent_grants (
    id             TEXT         NOT NULL PRIMARY KEY,
    skill_id       TEXT         NOT NULL REFERENCES skills(id) ON DELETE CASCADE,
    agent_id       TEXT         NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    pinned_version INT          NOT NULL,
    granted_by     VARCHAR(255) NOT NULL,
    created_at     TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE(skill_id, agent_id)
);

CREATE INDEX IF NOT EXISTS idx_skill_agent_grants_agent ON skill_agent_grants(agent_id);

CREATE TABLE IF NOT EXISTS skill_user_grants (
    id         TEXT         NOT NULL PRIMARY KEY,
    skill_id   TEXT         NOT NULL REFERENCES skills(id) ON DELETE CASCADE,
    user_id    TEXT         NOT NULL REFERENCES users(id)  ON DELETE CASCADE,
    granted_by VARCHAR(255) NOT NULL,
    created_at TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE(skill_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_skill_user_grants_user ON skill_user_grants(user_id);

CREATE TABLE IF NOT EXISTS skill_versions (
    id           TEXT         NOT NULL PRIMARY KEY,
    skill_id     TEXT         NOT NULL REFERENCES skills(id) ON DELETE CASCADE,
    version      INT          NOT NULL,
    file_hash    VARCHAR(64)  NOT NULL,
    file_path    TEXT         NOT NULL,
    file_size    INTEGER      NOT NULL DEFAULT 0,
    frontmatter  TEXT         NOT NULL DEFAULT '{}',
    content      TEXT         NOT NULL DEFAULT '',
    changelog    TEXT,
    published_by VARCHAR(255),
    archived_at  TEXT,
    archive_path TEXT,
    metadata     TEXT         NOT NULL DEFAULT '{}',
    created_at   TEXT         NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE(skill_id, version)
);

CREATE INDEX IF NOT EXISTS idx_skill_versions_skill    ON skill_versions(skill_id);
CREATE INDEX IF NOT EXISTS idx_skill_versions_archived ON skill_versions(skill_id) WHERE archived_at IS NOT NULL;

CREATE TABLE IF NOT EXISTS curator_runs (
    id           TEXT        NOT NULL PRIMARY KEY,
    skill_id     TEXT        REFERENCES skills(id) ON DELETE SET NULL,
    status       VARCHAR(20) NOT NULL DEFAULT 'running'
                 CHECK (status IN ('running', 'completed', 'failed')),
    result       TEXT,
    error        TEXT,
    triggered_by VARCHAR(255),
    started_at   TEXT        NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    finished_at  TEXT
);

CREATE INDEX IF NOT EXISTS idx_curator_runs_skill  ON curator_runs(skill_id);
CREATE INDEX IF NOT EXISTS idx_curator_runs_status ON curator_runs(status);

CREATE TABLE IF NOT EXISTS curator_events (
    id         TEXT         NOT NULL PRIMARY KEY,
    run_id     TEXT         NOT NULL REFERENCES curator_runs(id) ON DELETE CASCADE,
    event_type VARCHAR(32)  NOT NULL,
    payload    TEXT         NOT NULL DEFAULT '{}',
    created_at TEXT         NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_curator_events_run ON curator_events(run_id, created_at);

-- ============================================================
-- Section 9: Channels
-- ============================================================

CREATE TABLE IF NOT EXISTS channel_instances (
    id           TEXT         NOT NULL PRIMARY KEY,
    name         VARCHAR(100) NOT NULL,
    display_name VARCHAR(255) DEFAULT '',
    channel_type VARCHAR(50)  NOT NULL,
    agent_id     TEXT         NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    credentials  BLOB,
    config       TEXT         DEFAULT '{}',
    enabled      INTEGER      DEFAULT 1,
    created_by   VARCHAR(255) DEFAULT '',
    metadata     TEXT         NOT NULL DEFAULT '{}',
    created_at   TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at   TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_channel_instances_name ON channel_instances(name);
CREATE INDEX IF NOT EXISTS idx_channel_instances_type        ON channel_instances(channel_type);
CREATE INDEX IF NOT EXISTS idx_channel_instances_agent       ON channel_instances(agent_id);

CREATE TABLE IF NOT EXISTS channel_pending_messages (
    id              TEXT         NOT NULL PRIMARY KEY,
    channel_name    VARCHAR(100) NOT NULL,
    history_key     VARCHAR(200) NOT NULL,
    sender          VARCHAR(255) NOT NULL,
    sender_id       VARCHAR(255) NOT NULL DEFAULT '',
    body            TEXT         NOT NULL,
    platform_msg_id VARCHAR(100) NOT NULL DEFAULT '',
    is_summary      INTEGER      NOT NULL DEFAULT 0,
    created_at      TEXT         NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at      TEXT         NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_channel_pending_messages_lookup ON channel_pending_messages(channel_name, history_key, created_at);

CREATE TABLE IF NOT EXISTS channel_contacts (
    id                 TEXT         NOT NULL PRIMARY KEY,
    channel_type       VARCHAR(50)  NOT NULL,
    channel_instance   VARCHAR(255),
    sender_id          VARCHAR(255) NOT NULL,
    user_id            TEXT         REFERENCES users(id) ON DELETE SET NULL,
    display_name       VARCHAR(255),
    username           VARCHAR(255),
    avatar_url         TEXT,
    peer_kind          VARCHAR(20),
    contact_type       VARCHAR(20)  NOT NULL DEFAULT 'user',
    thread_id          VARCHAR(100),
    thread_type        VARCHAR(20),
    metadata           TEXT         DEFAULT '{}',
    merge_audit        TEXT         NOT NULL DEFAULT '{}',
    merged_id          TEXT         REFERENCES users(id) ON DELETE SET NULL,
    default_project_id TEXT         REFERENCES projects(id) ON DELETE SET NULL,
    first_seen_at      TEXT         NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    last_seen_at       TEXT         NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_channel_contacts_type_sender
    ON channel_contacts(channel_type, sender_id, COALESCE(thread_id, ''));
CREATE INDEX IF NOT EXISTS idx_channel_contacts_instance        ON channel_contacts(channel_instance)       WHERE channel_instance IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_channel_contacts_merged          ON channel_contacts(merged_id)              WHERE merged_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_channel_contacts_search          ON channel_contacts(display_name, username);
CREATE INDEX IF NOT EXISTS idx_channel_contacts_user            ON channel_contacts(user_id)                WHERE user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_channel_contacts_default_project ON channel_contacts(default_project_id)    WHERE default_project_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS pairing_requests (
    id         TEXT         NOT NULL PRIMARY KEY,
    code       VARCHAR(8)   NOT NULL UNIQUE,
    sender_id  VARCHAR(200) NOT NULL,
    channel    VARCHAR(255) NOT NULL,
    chat_id    VARCHAR(200) NOT NULL,
    account_id VARCHAR(100) NOT NULL DEFAULT 'default',
    metadata   TEXT         DEFAULT '{}',
    expires_at TEXT         NOT NULL,
    created_at TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE IF NOT EXISTS paired_devices (
    id         TEXT         NOT NULL PRIMARY KEY,
    sender_id  VARCHAR(200) NOT NULL,
    channel    VARCHAR(255) NOT NULL,
    chat_id    VARCHAR(200) NOT NULL,
    user_id    TEXT         REFERENCES users(id) ON DELETE SET NULL,
    paired_by  VARCHAR(100) NOT NULL DEFAULT 'operator',
    metadata   TEXT         DEFAULT '{}',
    expires_at TEXT,
    paired_at  TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_paired_devices_sender_channel ON paired_devices(sender_id, channel);
CREATE INDEX IF NOT EXISTS idx_paired_devices_user                  ON paired_devices(user_id) WHERE user_id IS NOT NULL;

-- ============================================================
-- Section 10: Cron & Heartbeat
-- ============================================================

CREATE TABLE IF NOT EXISTS cron_jobs (
    id               TEXT         NOT NULL PRIMARY KEY,
    agent_id         TEXT         REFERENCES agents(id)      ON DELETE CASCADE,
    user_id          TEXT         REFERENCES users(id)       ON DELETE SET NULL,
    name             VARCHAR(255) NOT NULL,
    enabled          INTEGER      NOT NULL DEFAULT 1,
    schedule_kind    VARCHAR(10)  NOT NULL,
    cron_expression  VARCHAR(100),
    interval_ms      INTEGER,
    run_at           TEXT,
    timezone         VARCHAR(50),
    payload          TEXT         NOT NULL,
    delete_after_run INTEGER      NOT NULL DEFAULT 0,
    stateless        INTEGER      NOT NULL DEFAULT 0,
    deliver          INTEGER      NOT NULL DEFAULT 0,
    deliver_channel  TEXT         NOT NULL DEFAULT '',
    deliver_to       TEXT         NOT NULL DEFAULT '',
    wake_heartbeat   INTEGER      NOT NULL DEFAULT 0,
    next_run_at      TEXT,
    last_run_at      TEXT,
    last_status      VARCHAR(20),
    last_error       TEXT,
    team_id          TEXT         REFERENCES agent_teams(id) ON DELETE SET NULL,
    metadata         TEXT         NOT NULL DEFAULT '{}',
    created_at       TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at       TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_cron_jobs_user       ON cron_jobs(user_id);
CREATE INDEX IF NOT EXISTS idx_cron_jobs_agent_user ON cron_jobs(agent_id, user_id);
CREATE INDEX IF NOT EXISTS idx_cron_jobs_team       ON cron_jobs(team_id) WHERE team_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_cron_jobs_agent_name ON cron_jobs(agent_id, name);

CREATE TABLE IF NOT EXISTS cron_run_logs (
    id            TEXT        NOT NULL PRIMARY KEY,
    job_id        TEXT        NOT NULL REFERENCES cron_jobs(id)    ON DELETE CASCADE,
    agent_id      TEXT        REFERENCES agents(id)                ON DELETE SET NULL,
    status        VARCHAR(20) NOT NULL,
    summary       TEXT,
    error         TEXT,
    duration_ms   INT,
    input_tokens  INT         DEFAULT 0,
    output_tokens INT         DEFAULT 0,
    team_id       TEXT        REFERENCES agent_teams(id)           ON DELETE SET NULL,
    ran_at        TEXT        DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    created_at    TEXT        DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_cron_run_logs_job  ON cron_run_logs(job_id, ran_at DESC);
CREATE INDEX IF NOT EXISTS idx_cron_run_logs_team ON cron_run_logs(team_id) WHERE team_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS agent_heartbeats (
    id                 TEXT         NOT NULL PRIMARY KEY,
    agent_id           TEXT         NOT NULL UNIQUE REFERENCES agents(id) ON DELETE CASCADE,
    enabled            INTEGER      NOT NULL DEFAULT 0,
    interval_sec       INT          NOT NULL DEFAULT 1800,
    prompt             TEXT,
    provider_id        TEXT         REFERENCES llm_providers(id) ON DELETE SET NULL,
    model              VARCHAR(200),
    isolated_session   INTEGER      NOT NULL DEFAULT 1,
    light_context      INTEGER      NOT NULL DEFAULT 0,
    ack_max_chars      INT          NOT NULL DEFAULT 300,
    max_retries        INT          NOT NULL DEFAULT 2,
    active_hours_start VARCHAR(5),
    active_hours_end   VARCHAR(5),
    timezone           TEXT,
    channel            VARCHAR(50),
    chat_id            TEXT,
    next_run_at        TEXT,
    last_run_at        TEXT,
    last_status        VARCHAR(20),
    last_error         TEXT,
    run_count          INT          NOT NULL DEFAULT 0,
    suppress_count     INT          NOT NULL DEFAULT 0,
    metadata           TEXT         DEFAULT '{}',
    created_at         TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at         TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_heartbeats_due ON agent_heartbeats(next_run_at) WHERE enabled = 1 AND next_run_at IS NOT NULL;

CREATE TABLE IF NOT EXISTS heartbeat_run_logs (
    id            TEXT         NOT NULL PRIMARY KEY,
    heartbeat_id  TEXT         NOT NULL REFERENCES agent_heartbeats(id) ON DELETE CASCADE,
    agent_id      TEXT         NOT NULL REFERENCES agents(id)           ON DELETE CASCADE,
    status        VARCHAR(20)  NOT NULL,
    summary       TEXT,
    error         TEXT,
    duration_ms   INT,
    input_tokens  INT          DEFAULT 0,
    output_tokens INT          DEFAULT 0,
    skip_reason   VARCHAR(50),
    metadata      TEXT         DEFAULT '{}',
    ran_at        TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    created_at    TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_hb_logs_heartbeat ON heartbeat_run_logs(heartbeat_id, ran_at DESC);
CREATE INDEX IF NOT EXISTS idx_hb_logs_agent     ON heartbeat_run_logs(agent_id,     ran_at DESC);

-- ============================================================
-- Section 11: MCP (Model Context Protocol)
-- ============================================================

CREATE TABLE IF NOT EXISTS mcp_servers (
    id           TEXT         NOT NULL PRIMARY KEY,
    name         VARCHAR(255) NOT NULL UNIQUE,
    display_name VARCHAR(255),
    transport    VARCHAR(50)  NOT NULL,
    command      TEXT,
    args         TEXT         DEFAULT '[]',
    url          TEXT,
    headers      TEXT         DEFAULT '{}',
    env          TEXT         DEFAULT '{}',
    api_key      TEXT,
    tool_prefix  VARCHAR(50),
    timeout_sec  INT          DEFAULT 60,
    settings     TEXT         NOT NULL DEFAULT '{}',
    enabled      INTEGER      NOT NULL DEFAULT 1,
    created_by   VARCHAR(255) NOT NULL,
    metadata     TEXT         NOT NULL DEFAULT '{}',
    -- Scope columns: exactly one of {both NULL (global), team_id only, project_id only}
    team_id      TEXT         NULL REFERENCES agent_teams(id) ON DELETE CASCADE,
    project_id   TEXT         NULL REFERENCES projects(id)    ON DELETE CASCADE,
    created_at   TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at   TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    -- 3-state scope mutex: global (both NULL), team-scoped, or project-scoped.
    CONSTRAINT mcp_servers_scope_exclusive CHECK (
        (team_id IS NULL     AND project_id IS NULL)     OR
        (team_id IS NOT NULL AND project_id IS NULL)     OR
        (team_id IS NULL     AND project_id IS NOT NULL)
    )
);

CREATE INDEX IF NOT EXISTS idx_mcp_servers_team    ON mcp_servers(team_id)    WHERE team_id    IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_mcp_servers_project ON mcp_servers(project_id) WHERE project_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS mcp_agent_grants (
    id               TEXT         NOT NULL PRIMARY KEY,
    server_id        TEXT         NOT NULL REFERENCES mcp_servers(id) ON DELETE CASCADE,
    agent_id         TEXT         NOT NULL REFERENCES agents(id)      ON DELETE CASCADE,
    enabled          INTEGER      NOT NULL DEFAULT 1,
    tool_allow       TEXT,
    tool_deny        TEXT,
    config_overrides TEXT,
    granted_by       VARCHAR(255) NOT NULL,
    created_at       TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE(server_id, agent_id)
);

CREATE INDEX IF NOT EXISTS idx_mcp_agent_grants_agent ON mcp_agent_grants(agent_id);

CREATE TABLE IF NOT EXISTS mcp_user_grants (
    id         TEXT         NOT NULL PRIMARY KEY,
    server_id  TEXT         NOT NULL REFERENCES mcp_servers(id) ON DELETE CASCADE,
    user_id    TEXT         NOT NULL REFERENCES users(id)        ON DELETE CASCADE,
    enabled    INTEGER      NOT NULL DEFAULT 1,
    tool_allow TEXT,
    tool_deny  TEXT,
    granted_by VARCHAR(255) NOT NULL,
    created_at TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE(server_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_mcp_user_grants_user ON mcp_user_grants(user_id);

CREATE TABLE IF NOT EXISTS mcp_access_requests (
    id           TEXT         NOT NULL PRIMARY KEY,
    server_id    TEXT         NOT NULL REFERENCES mcp_servers(id) ON DELETE CASCADE,
    agent_id     TEXT         REFERENCES agents(id)               ON DELETE CASCADE,
    user_id      TEXT         REFERENCES users(id)                ON DELETE CASCADE,
    scope        VARCHAR(10)  NOT NULL,
    status       VARCHAR(20)  NOT NULL DEFAULT 'pending',
    reason       TEXT,
    tool_allow   TEXT,
    requested_by VARCHAR(255) NOT NULL,
    reviewed_by  VARCHAR(255),
    reviewed_at  TEXT,
    review_note  TEXT,
    created_at   TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    CONSTRAINT mcp_access_requests_scope_check
        CHECK (scope IN ('agent', 'user')),
    CONSTRAINT mcp_access_requests_scope_shape_check
        CHECK (
            (scope = 'agent' AND agent_id IS NOT NULL AND user_id IS NULL) OR
            (scope = 'user'  AND user_id  IS NOT NULL AND agent_id IS NULL)
        ),
    CONSTRAINT mcp_access_requests_status_check
        CHECK (status IN ('pending', 'granted', 'denied', 'revoked'))
);

-- Partial UNIQUE index emulating NULLS NOT DISTINCT via COALESCE sentinel.
-- The zero-UUID sentinel '00000000-0000-0000-0000-000000000000' is reserved
-- and will never appear as a real agent_id or user_id (uuid_v7 is never all-zero).
-- WHERE status='pending' limits deduplication to in-flight requests only —
-- denied/granted/revoked rows are kept as audit history.
CREATE UNIQUE INDEX IF NOT EXISTS mcp_access_requests_unique_pending
    ON mcp_access_requests(
        server_id,
        COALESCE(agent_id, '00000000-0000-0000-0000-000000000000'),
        COALESCE(user_id,  '00000000-0000-0000-0000-000000000000')
    )
    WHERE status = 'pending';

CREATE INDEX IF NOT EXISTS idx_mcp_requests_status ON mcp_access_requests(status) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_mcp_requests_server ON mcp_access_requests(server_id);

CREATE TABLE IF NOT EXISTS mcp_user_credentials (
    id         TEXT         NOT NULL PRIMARY KEY,
    server_id  TEXT         NOT NULL REFERENCES mcp_servers(id) ON DELETE CASCADE,
    user_id    TEXT         NOT NULL REFERENCES users(id)        ON DELETE CASCADE,
    api_key    TEXT,
    headers    BLOB,
    env        BLOB,
    created_at TEXT         NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT         NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE(server_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_mcp_user_credentials_server ON mcp_user_credentials(server_id);
CREATE INDEX IF NOT EXISTS idx_mcp_user_credentials_user   ON mcp_user_credentials(user_id);

-- ============================================================
-- Section 12: Tracing
-- ============================================================

CREATE TABLE IF NOT EXISTS traces (
    id                  TEXT         NOT NULL PRIMARY KEY,
    agent_id            TEXT         REFERENCES agents(id)      ON DELETE SET NULL,
    user_id             TEXT         REFERENCES users(id)       ON DELETE SET NULL,
    session_key         TEXT,
    run_id              TEXT,
    start_time          TEXT         NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    end_time            TEXT,
    duration_ms         INT,
    name                TEXT,
    channel             VARCHAR(50),
    input_preview       TEXT,
    output_preview      TEXT,
    total_input_tokens  INT          DEFAULT 0,
    total_output_tokens INT          DEFAULT 0,
    total_cost          REAL         DEFAULT 0,
    span_count          INT          DEFAULT 0,
    llm_call_count      INT          DEFAULT 0,
    tool_call_count     INT          DEFAULT 0,
    status              VARCHAR(20)  DEFAULT 'running',
    error               TEXT,
    metadata            TEXT,
    tags                TEXT,
    parent_trace_id     TEXT         REFERENCES traces(id) ON DELETE SET NULL,
    team_id             TEXT         REFERENCES agent_teams(id) ON DELETE SET NULL,
    contact_id          TEXT         REFERENCES channel_contacts(id) ON DELETE SET NULL,
    created_at          TEXT         NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_traces_agent_time  ON traces(agent_id,    created_at DESC);
CREATE INDEX IF NOT EXISTS idx_traces_user_time   ON traces(user_id,     created_at DESC) WHERE user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_traces_session     ON traces(session_key, created_at DESC) WHERE session_key IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_traces_status      ON traces(status)                       WHERE status = 'error';
CREATE INDEX IF NOT EXISTS idx_traces_parent      ON traces(parent_trace_id)              WHERE parent_trace_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_traces_quota       ON traces(user_id,     created_at DESC) WHERE parent_trace_id IS NULL AND user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_traces_start_root  ON traces(start_time DESC)              WHERE parent_trace_id IS NULL;
CREATE INDEX IF NOT EXISTS idx_traces_team        ON traces(team_id,     created_at DESC) WHERE team_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_traces_contact     ON traces(contact_id)                   WHERE contact_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS spans (
    id             TEXT         NOT NULL PRIMARY KEY,
    trace_id       TEXT         NOT NULL REFERENCES traces(id) ON DELETE CASCADE,
    parent_span_id TEXT         REFERENCES spans(id) ON DELETE SET NULL,
    agent_id       TEXT         REFERENCES agents(id) ON DELETE SET NULL,
    span_type      VARCHAR(20)  NOT NULL,
    name           TEXT,
    start_time     TEXT         NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    end_time       TEXT,
    duration_ms    INT,
    status         VARCHAR(20)  DEFAULT 'running',
    error          TEXT,
    level          VARCHAR(10)  DEFAULT 'DEFAULT',
    model          VARCHAR(200),
    provider       VARCHAR(50),
    input_tokens   INT,
    output_tokens  INT,
    total_cost     REAL,
    finish_reason  VARCHAR(50),
    model_params   TEXT,
    tool_name      VARCHAR(200),
    tool_call_id   VARCHAR(100),
    input_preview  TEXT,
    output_preview TEXT,
    metadata       TEXT,
    team_id        TEXT         REFERENCES agent_teams(id) ON DELETE SET NULL,
    contact_id     TEXT         REFERENCES channel_contacts(id) ON DELETE SET NULL,
    created_at     TEXT         NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_spans_trace      ON spans(trace_id,    start_time);
CREATE INDEX IF NOT EXISTS idx_spans_trace_type ON spans(trace_id,    span_type);
CREATE INDEX IF NOT EXISTS idx_spans_parent     ON spans(parent_span_id) WHERE parent_span_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_spans_agent_time ON spans(agent_id,    created_at DESC);
CREATE INDEX IF NOT EXISTS idx_spans_type       ON spans(span_type,   created_at DESC);
CREATE INDEX IF NOT EXISTS idx_spans_model      ON spans(model,       created_at DESC) WHERE model IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_spans_error      ON spans(status)                       WHERE status = 'error';
CREATE INDEX IF NOT EXISTS idx_spans_team       ON spans(team_id)                      WHERE team_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_spans_contact    ON spans(contact_id)                   WHERE contact_id IS NOT NULL;

-- ============================================================
-- Section 13: Tools (builtin, secure CLI, subagent tasks)
-- ============================================================

CREATE TABLE IF NOT EXISTS builtin_tools (
    name         VARCHAR(100) NOT NULL PRIMARY KEY,
    display_name VARCHAR(255) NOT NULL,
    description  TEXT         NOT NULL DEFAULT '',
    category     VARCHAR(50)  NOT NULL DEFAULT 'general',
    enabled      INTEGER      NOT NULL DEFAULT 1,
    settings     TEXT         NOT NULL DEFAULT '{}',
    requires     TEXT         DEFAULT '[]',
    metadata     TEXT         DEFAULT '{}',
    created_at   TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at   TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_builtin_tools_category ON builtin_tools(category);

CREATE TABLE IF NOT EXISTS secure_cli_binaries (
    id              TEXT         NOT NULL PRIMARY KEY,
    binary_name     TEXT         NOT NULL,
    binary_path     TEXT,
    description     TEXT         NOT NULL DEFAULT '',
    encrypted_env   BLOB         NOT NULL,
    deny_args       TEXT         NOT NULL DEFAULT '[]',
    deny_verbose    TEXT         NOT NULL DEFAULT '[]',
    timeout_seconds INTEGER      NOT NULL DEFAULT 30,
    tips            TEXT         NOT NULL DEFAULT '',
    is_global       INTEGER      NOT NULL DEFAULT 1,
    enabled         INTEGER      NOT NULL DEFAULT 1,
    created_by      TEXT         NOT NULL DEFAULT '',
    created_at      TEXT         NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at      TEXT         NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_secure_cli_unique_binary ON secure_cli_binaries(binary_name);
CREATE INDEX IF NOT EXISTS idx_secure_cli_binary_name          ON secure_cli_binaries(binary_name);

CREATE TABLE IF NOT EXISTS secure_cli_agent_grants (
    id              TEXT         NOT NULL PRIMARY KEY,
    binary_id       TEXT         NOT NULL REFERENCES secure_cli_binaries(id) ON DELETE CASCADE,
    agent_id        TEXT         NOT NULL REFERENCES agents(id)              ON DELETE CASCADE,
    deny_args       TEXT,
    deny_verbose    TEXT,
    timeout_seconds INTEGER,
    tips            TEXT,
    enabled         INTEGER      NOT NULL DEFAULT 1,
    created_at      TEXT         NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at      TEXT         NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE(binary_id, agent_id)
);

CREATE INDEX IF NOT EXISTS idx_scag_binary ON secure_cli_agent_grants(binary_id);
CREATE INDEX IF NOT EXISTS idx_scag_agent  ON secure_cli_agent_grants(agent_id);

CREATE TABLE IF NOT EXISTS secure_cli_user_credentials (
    id            TEXT         NOT NULL PRIMARY KEY,
    binary_id     TEXT         NOT NULL REFERENCES secure_cli_binaries(id) ON DELETE CASCADE,
    user_id       TEXT         NOT NULL REFERENCES users(id)               ON DELETE CASCADE,
    encrypted_env BLOB         NOT NULL,
    metadata      TEXT         NOT NULL DEFAULT '{}',
    created_at    TEXT         NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at    TEXT         NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE(binary_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_scuc_binary ON secure_cli_user_credentials(binary_id);
CREATE INDEX IF NOT EXISTS idx_scuc_user   ON secure_cli_user_credentials(user_id);

CREATE TABLE IF NOT EXISTS subagent_tasks (
    id               TEXT         NOT NULL PRIMARY KEY,
    parent_agent_key VARCHAR(255) NOT NULL,
    session_key      VARCHAR(500),
    subject          VARCHAR(255) NOT NULL,
    description      TEXT         NOT NULL,
    status           VARCHAR(20)  NOT NULL DEFAULT 'running',
    result           TEXT,
    depth            INTEGER      NOT NULL DEFAULT 1,
    model            VARCHAR(255),
    provider         VARCHAR(255),
    iterations       INTEGER      NOT NULL DEFAULT 0,
    input_tokens     INTEGER      NOT NULL DEFAULT 0,
    output_tokens    INTEGER      NOT NULL DEFAULT 0,
    origin_channel   VARCHAR(50),
    origin_chat_id   VARCHAR(255),
    origin_peer_kind VARCHAR(20),
    origin_user_id   TEXT         REFERENCES users(id) ON DELETE SET NULL,
    spawned_by       TEXT,
    completed_at     TEXT,
    archived_at      TEXT,
    metadata         TEXT         NOT NULL DEFAULT '{}',
    custom_scope     TEXT,
    project_id       TEXT         REFERENCES projects(id) ON DELETE SET NULL,
    created_at       TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at       TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_subagent_tasks_parent_status ON subagent_tasks(parent_agent_key, status);
CREATE INDEX IF NOT EXISTS idx_subagent_tasks_session       ON subagent_tasks(session_key);
CREATE INDEX IF NOT EXISTS idx_subagent_tasks_created       ON subagent_tasks(created_at);
CREATE INDEX IF NOT EXISTS idx_subagent_tasks_project       ON subagent_tasks(project_id) WHERE project_id IS NOT NULL;

-- ============================================================
-- Section 14: Audit & Config
-- ============================================================

CREATE TABLE IF NOT EXISTS activity_logs (
    id          TEXT         NOT NULL PRIMARY KEY,
    actor_type  VARCHAR(20)  NOT NULL,
    actor_id    VARCHAR(255) NOT NULL,
    action      VARCHAR(100) NOT NULL,
    entity_type VARCHAR(50),
    entity_id   VARCHAR(255),
    details     TEXT,
    ip_address  VARCHAR(45),
    user_id     TEXT         REFERENCES users(id) ON DELETE SET NULL,
    created_at  TEXT         NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_activity_logs_actor   ON activity_logs(actor_type, actor_id);
CREATE INDEX IF NOT EXISTS idx_activity_logs_action  ON activity_logs(action);
CREATE INDEX IF NOT EXISTS idx_activity_logs_entity  ON activity_logs(entity_type, entity_id);
CREATE INDEX IF NOT EXISTS idx_activity_logs_created ON activity_logs(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_activity_logs_user    ON activity_logs(user_id) WHERE user_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS usage_snapshots (
    id                  TEXT         NOT NULL PRIMARY KEY,
    bucket_hour         TEXT         NOT NULL,
    agent_id            TEXT         REFERENCES agents(id) ON DELETE SET NULL,
    provider            VARCHAR(50)  NOT NULL DEFAULT '',
    model               VARCHAR(200) NOT NULL DEFAULT '',
    channel             VARCHAR(50)  NOT NULL DEFAULT '',
    input_tokens        INTEGER      NOT NULL DEFAULT 0,
    output_tokens       INTEGER      NOT NULL DEFAULT 0,
    cache_read_tokens   INTEGER      NOT NULL DEFAULT 0,
    cache_create_tokens INTEGER      NOT NULL DEFAULT 0,
    thinking_tokens     INTEGER      NOT NULL DEFAULT 0,
    total_cost          REAL         NOT NULL DEFAULT 0,
    request_count       INTEGER      NOT NULL DEFAULT 0,
    llm_call_count      INTEGER      NOT NULL DEFAULT 0,
    tool_call_count     INTEGER      NOT NULL DEFAULT 0,
    error_count         INTEGER      NOT NULL DEFAULT 0,
    unique_users        INTEGER      NOT NULL DEFAULT 0,
    avg_duration_ms     INTEGER      NOT NULL DEFAULT 0,
    memory_docs         INTEGER      NOT NULL DEFAULT 0,
    memory_chunks       INTEGER      NOT NULL DEFAULT 0,
    kg_entities         INTEGER      NOT NULL DEFAULT 0,
    kg_relations        INTEGER      NOT NULL DEFAULT 0,
    created_at          TEXT         NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_usage_snapshots_bucket       ON usage_snapshots(bucket_hour DESC);
CREATE INDEX IF NOT EXISTS idx_usage_snapshots_agent_bucket ON usage_snapshots(agent_id, bucket_hour DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_usage_snapshots_unique ON usage_snapshots(
    bucket_hour,
    COALESCE(agent_id, '00000000-0000-0000-0000-000000000000'),
    COALESCE(provider, ''),
    COALESCE(model,    ''),
    COALESCE(channel,  '')
);

CREATE TABLE IF NOT EXISTS agent_config_permissions (
    id          TEXT         NOT NULL PRIMARY KEY,
    agent_id    TEXT         NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    scope       VARCHAR(255) NOT NULL,
    config_type VARCHAR(50)  NOT NULL CHECK (config_type IN ('write_file','edit_file','delete_file','cron','heartbeat','*')),
    user_id     VARCHAR(255) NOT NULL,
    permission  VARCHAR(10)  NOT NULL,
    granted_by  VARCHAR(255),
    metadata    TEXT         DEFAULT '{}',
    deny_globs  TEXT         NOT NULL DEFAULT '[".env*","secrets/**",".git/**","*.key","*.pem"]',
    created_at  TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at  TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE(agent_id, scope, config_type, user_id)
);

CREATE INDEX IF NOT EXISTS idx_acp_lookup ON agent_config_permissions(agent_id, scope, config_type);
CREATE INDEX IF NOT EXISTS idx_acp_user   ON agent_config_permissions(user_id);

CREATE TABLE IF NOT EXISTS system_configs (
    key        VARCHAR(100) NOT NULL PRIMARY KEY,
    value      TEXT         NOT NULL,
    metadata   TEXT         NOT NULL DEFAULT '{}',
    updated_at TEXT         NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE IF NOT EXISTS config_secrets (
    key        VARCHAR(100) NOT NULL PRIMARY KEY,
    value      BLOB         NOT NULL,
    created_at TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT         DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- ============================================================
-- Section 15: Hooks
-- ============================================================

CREATE TABLE IF NOT EXISTS hooks (
    id           TEXT         NOT NULL PRIMARY KEY,
    scope        TEXT         NOT NULL CHECK (scope IN ('global', 'user', 'agent')),
    event        TEXT         NOT NULL,
    handler_type TEXT         NOT NULL CHECK (handler_type IN ('command', 'http', 'prompt', 'script')),
    config       TEXT         NOT NULL DEFAULT '{}',
    matcher      TEXT,
    if_expr      TEXT,
    timeout_ms   INTEGER      NOT NULL DEFAULT 5000,
    on_timeout   TEXT         NOT NULL DEFAULT 'block' CHECK (on_timeout IN ('block', 'allow')),
    priority     INTEGER      NOT NULL DEFAULT 0,
    enabled      INTEGER      NOT NULL DEFAULT 1,
    version      INTEGER      NOT NULL DEFAULT 1,
    source       TEXT         NOT NULL DEFAULT 'ui' CHECK (source IN ('ui', 'api', 'seed', 'builtin')),
    metadata     TEXT         NOT NULL DEFAULT '{}',
    name         TEXT,
    created_by   TEXT,
    user_id      TEXT         REFERENCES users(id) ON DELETE SET NULL,
    created_at   TEXT         NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at   TEXT         NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_hooks_lookup ON hooks(event) WHERE enabled = 1;
CREATE INDEX IF NOT EXISTS idx_hooks_user   ON hooks(user_id) WHERE user_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS hook_agents (
    hook_id  TEXT NOT NULL REFERENCES hooks(id)  ON DELETE CASCADE,
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    PRIMARY KEY (hook_id, agent_id)
);

CREATE INDEX IF NOT EXISTS idx_hook_agents_agent ON hook_agents(agent_id);

CREATE TABLE IF NOT EXISTS hook_executions (
    id           TEXT         NOT NULL PRIMARY KEY,
    hook_id      TEXT         REFERENCES hooks(id) ON DELETE SET NULL,
    session_id   TEXT,
    event        TEXT         NOT NULL,
    input_hash   TEXT,
    decision     TEXT         NOT NULL CHECK (decision IN ('allow', 'block', 'error', 'timeout')),
    duration_ms  INTEGER      NOT NULL DEFAULT 0,
    retry        INTEGER      NOT NULL DEFAULT 0,
    dedup_key    TEXT,
    error        TEXT,
    error_detail BLOB,
    metadata     TEXT         NOT NULL DEFAULT '{}',
    created_at   TEXT         NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_hook_executions_session ON hook_executions(session_id, created_at);
CREATE UNIQUE INDEX IF NOT EXISTS uq_hook_executions_dedup ON hook_executions(dedup_key) WHERE dedup_key IS NOT NULL;

CREATE TABLE IF NOT EXISTS user_hook_budget (
    user_id        TEXT         NOT NULL PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    month_start    TEXT         NOT NULL,
    budget_total   INTEGER      NOT NULL DEFAULT 0,
    remaining      INTEGER      NOT NULL DEFAULT 0,
    last_warned_at TEXT,
    metadata       TEXT         NOT NULL DEFAULT '{}',
    updated_at     TEXT         NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- ============================================================
-- Section 16: Evolution
-- ============================================================

CREATE TABLE IF NOT EXISTS agent_evolution_metrics (
    id          TEXT         NOT NULL PRIMARY KEY,
    agent_id    TEXT         NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    session_key TEXT         NOT NULL,
    metric_type TEXT         NOT NULL,
    metric_key  TEXT         NOT NULL,
    value       TEXT         NOT NULL,
    created_at  TEXT         NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_evo_metrics_agent_type ON agent_evolution_metrics(agent_id, metric_type);
CREATE INDEX IF NOT EXISTS idx_evo_metrics_created    ON agent_evolution_metrics(created_at);

CREATE TABLE IF NOT EXISTS agent_evolution_suggestions (
    id              TEXT         NOT NULL PRIMARY KEY,
    agent_id        TEXT         NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    suggestion_type TEXT         NOT NULL,
    suggestion      TEXT         NOT NULL,
    rationale       TEXT         NOT NULL,
    parameters      TEXT,
    status          TEXT         NOT NULL DEFAULT 'pending',
    reviewed_by     TEXT,
    reviewed_at     TEXT,
    created_at      TEXT         NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_evo_suggestions_agent ON agent_evolution_suggestions(agent_id, status);
