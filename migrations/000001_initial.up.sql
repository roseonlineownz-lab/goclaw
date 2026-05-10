-- GoClaw v4 Initial Schema
-- Single-tenant, user-centric model. No tenant_id columns anywhere.
-- All id columns use uuid_generate_v7() (time-ordered UUID v7) for B-tree locality.
-- user_id / owner_user_id columns are UUID FK references to users(id).
-- Critical user-data FKs use ON DELETE SET NULL to preserve orphaned rows.
-- Sessions table renamed from v3 "sessions" to "agent_sessions".

-- ============================================================
-- Extensions
-- ============================================================

CREATE EXTENSION IF NOT EXISTS "pgcrypto";
CREATE EXTENSION IF NOT EXISTS "vector";

-- ============================================================
-- UUID v7 function (time-ordered, B-tree friendly)
-- Copied verbatim from v3 migrations/000001_init_schema.up.sql:8
-- ============================================================

CREATE OR REPLACE FUNCTION uuid_generate_v7() RETURNS uuid AS $$
DECLARE
    unix_ts_ms bytea;
    uuid_bytes bytea;
BEGIN
    unix_ts_ms = substring(int8send(floor(extract(epoch from clock_timestamp()) * 1000)::bigint) from 3);
    uuid_bytes = unix_ts_ms || gen_random_bytes(10);
    uuid_bytes = set_byte(uuid_bytes, 6, (b'0111' || get_byte(uuid_bytes, 6)::bit(4))::bit(8)::int);
    uuid_bytes = set_byte(uuid_bytes, 8, (b'10' || get_byte(uuid_bytes, 8)::bit(6))::bit(8)::int);
    RETURN encode(uuid_bytes, 'hex')::uuid;
END
$$ LANGUAGE plpgsql VOLATILE;

-- ============================================================
-- LLM Providers (defined before agents — agent_heartbeats refs provider_id)
-- ============================================================

CREATE TABLE IF NOT EXISTS llm_providers (
    id            UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    name          VARCHAR(50)  NOT NULL UNIQUE,
    display_name  VARCHAR(255),
    provider_type VARCHAR(30)  NOT NULL DEFAULT 'openai_compat',
    api_base      TEXT,
    api_key       TEXT,
    enabled       BOOLEAN      NOT NULL DEFAULT TRUE,
    settings      JSONB        NOT NULL DEFAULT '{}',
    metadata      JSONB        NOT NULL DEFAULT '{}',
    created_at    TIMESTAMPTZ  DEFAULT NOW(),
    updated_at    TIMESTAMPTZ  DEFAULT NOW()
);

-- ============================================================
-- Section 1: Core — users, user_sessions, agents, shares,
--             context files, profiles, overrides
-- ============================================================

CREATE TABLE IF NOT EXISTS users (
    id            UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    email         VARCHAR(255) NOT NULL UNIQUE,
    display_name  VARCHAR(255),
    password_hash TEXT         NOT NULL,
    role          VARCHAR(20)  NOT NULL DEFAULT 'member',
    status        VARCHAR(20)  NOT NULL DEFAULT 'active',
    deleted_at    TIMESTAMPTZ  NULL,
    metadata      JSONB        NOT NULL DEFAULT '{}',
    -- Stable workspace folder identifier derived from email local-part.
    -- Generated once at insert time; immutable thereafter.
    user_key      VARCHAR(100) NOT NULL UNIQUE,
    -- Identity kind: 'human' (default) or 'channel' (merged channel contact).
    -- Only two values are valid; future channel types extend channel_contacts.channel_type,
    -- not this column.
    kind          VARCHAR(20)  NOT NULL DEFAULT 'human',
    -- Channel platform when kind='channel' (e.g. 'telegram','discord').
    -- Must be NULL when kind='human'; must be NOT NULL when kind='channel'.
    -- Values validated at app layer against channel_contacts.channel_type.
    channel_type  VARCHAR(20)  NULL,
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    CONSTRAINT users_role_check         CHECK (role IN ('root', 'admin', 'member', 'viewer')),
    CONSTRAINT users_kind_check         CHECK (kind IN ('human', 'channel')),
    CONSTRAINT users_channel_type_shape CHECK (
        (kind = 'human'   AND channel_type IS NULL) OR
        (kind = 'channel' AND channel_type IS NOT NULL)
    )
);

-- Partial UNIQUE: at most one root user may exist at any time.
-- Bootstrap relies on this index + advisory lock to prevent concurrent root creation.
CREATE UNIQUE INDEX users_only_one_root ON users(role) WHERE role = 'root';
CREATE INDEX idx_users_email ON users(email);
CREATE INDEX idx_users_status ON users(status) WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS user_sessions (
    id                  UUID        NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    user_id             UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    family_id           UUID        NOT NULL,
    refresh_token_hash  TEXT        NOT NULL UNIQUE,
    expires_at          TIMESTAMPTZ NOT NULL,
    revoked_at          TIMESTAMPTZ NULL,
    metadata            JSONB       NOT NULL DEFAULT '{}',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_user_sessions_user      ON user_sessions(user_id);
CREATE INDEX idx_user_sessions_expires   ON user_sessions(expires_at) WHERE revoked_at IS NULL;
-- Enables efficient family-revocation: UPDATE ... WHERE family_id = $1
CREATE INDEX user_sessions_family_idx    ON user_sessions(family_id);

-- Single-use, time-bounded password-reset tokens. Raw token mailed/displayed
-- once; only the SHA-256 hex hash is persisted. used_at NULL = active. The
-- partial idx_password_reset_active index keeps validation lookups fast even
-- after the table grows historic rows.
CREATE TABLE IF NOT EXISTS password_reset_tokens (
    id          UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    user_id     UUID         NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash  VARCHAR(128) NOT NULL,
    expires_at  TIMESTAMPTZ  NOT NULL,
    used_at     TIMESTAMPTZ  NULL,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX idx_password_reset_token_hash ON password_reset_tokens(token_hash);
CREATE INDEX idx_password_reset_user              ON password_reset_tokens(user_id);
CREATE INDEX idx_password_reset_active            ON password_reset_tokens(token_hash) WHERE used_at IS NULL;

CREATE TABLE IF NOT EXISTS agents (
    id                    UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    agent_key             VARCHAR(100) NOT NULL,
    display_name          VARCHAR(255),
    owner_id              VARCHAR(255) NOT NULL,
    owner_user_id         UUID         REFERENCES users(id) ON DELETE SET NULL,
    provider              VARCHAR(50)  NOT NULL DEFAULT 'openrouter',
    model                 VARCHAR(200) NOT NULL,
    context_window        INT          NOT NULL DEFAULT 200000,
    max_tool_iterations   INT          NOT NULL DEFAULT 20,
    workspace             TEXT         NOT NULL DEFAULT '.',
    restrict_to_workspace BOOLEAN      NOT NULL DEFAULT TRUE,
    tools_config          JSONB        NOT NULL DEFAULT '{}',
    sandbox_config        JSONB,
    subagents_config      JSONB,
    memory_config         JSONB,
    compaction_config     JSONB,
    context_pruning       JSONB,
    other_config          JSONB        NOT NULL DEFAULT '{}',
    emoji                 TEXT         NOT NULL DEFAULT '',
    agent_description     TEXT         NOT NULL DEFAULT '',
    thinking_level        TEXT         NOT NULL DEFAULT '',
    max_tokens            INT          NOT NULL DEFAULT 0,
    self_evolve           BOOLEAN      NOT NULL DEFAULT FALSE,
    skill_evolve          BOOLEAN      NOT NULL DEFAULT FALSE,
    skill_nudge_interval  INT          NOT NULL DEFAULT 0,
    reasoning_config      JSONB        NOT NULL DEFAULT '{}',
    share_workspace       BOOLEAN      NOT NULL DEFAULT FALSE,
    share_memory          BOOLEAN      NOT NULL DEFAULT FALSE,
    chatgpt_oauth_routing JSONB        NOT NULL DEFAULT '{}',
    shell_deny_groups     JSONB        NOT NULL DEFAULT '{}',
    kg_dedup_config       JSONB        NOT NULL DEFAULT '{}',
    is_default            BOOLEAN      NOT NULL DEFAULT FALSE,
    status                VARCHAR(20)  DEFAULT 'active',
    frontmatter           TEXT,
    budget_monthly_cents  INTEGER,
    tsv                   tsvector GENERATED ALWAYS AS (
        to_tsvector('simple', coalesce(agent_key,'') || ' ' || coalesce(display_name,''))
    ) STORED,
    metadata              JSONB        NOT NULL DEFAULT '{}',
    created_at            TIMESTAMPTZ  DEFAULT NOW(),
    updated_at            TIMESTAMPTZ  DEFAULT NOW(),
    deleted_at            TIMESTAMPTZ
);

CREATE UNIQUE INDEX idx_agents_agent_key_active ON agents(agent_key) WHERE deleted_at IS NULL;
CREATE INDEX idx_agents_owner          ON agents(owner_id)      WHERE deleted_at IS NULL;
CREATE INDEX idx_agents_owner_user     ON agents(owner_user_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_agents_status         ON agents(status)        WHERE deleted_at IS NULL;
CREATE INDEX idx_agents_tsv            ON agents USING gin(tsv);

-- Explicit grants of agent access to either a user OR a team (mutex).
-- Owner role is implicit via agents.owner_id and is NOT a value of `role`.
-- Implicit team membership grants are computed by the access resolver
-- (internal/permissions/agent_access.go), not stored here.
CREATE TABLE IF NOT EXISTS agent_shares (
    id                  UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    agent_id            UUID         NOT NULL REFERENCES agents(id)       ON DELETE CASCADE,
    shared_with_user_id UUID         NULL     REFERENCES users(id)        ON DELETE CASCADE,
    -- shared_with_team_id FK to agent_teams added via ALTER TABLE after
    -- agent_teams is created (Section 3) — same pattern as agent_links.team_id.
    shared_with_team_id UUID         NULL,
    role                VARCHAR(20)  NOT NULL CHECK (role IN ('viewer','member','editor')),
    metadata            JSONB        NOT NULL DEFAULT '{}',
    created_by          UUID         NOT NULL REFERENCES users(id)        ON DELETE RESTRICT,
    created_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    -- Target mutex: exactly one of (user, team) must be set per row.
    CONSTRAINT agent_shares_target_mutex CHECK (
        (shared_with_user_id IS NOT NULL AND shared_with_team_id IS NULL) OR
        (shared_with_user_id IS NULL     AND shared_with_team_id IS NOT NULL)
    )
);

CREATE INDEX idx_agent_shares_agent ON agent_shares(agent_id);
CREATE UNIQUE INDEX idx_agent_shares_user
    ON agent_shares(agent_id, shared_with_user_id)
    WHERE shared_with_user_id IS NOT NULL;
CREATE UNIQUE INDEX idx_agent_shares_team
    ON agent_shares(agent_id, shared_with_team_id)
    WHERE shared_with_team_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS agent_context_files (
    id         UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    agent_id   UUID         NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    file_name  VARCHAR(255) NOT NULL,
    content    TEXT         NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ  DEFAULT NOW(),
    updated_at TIMESTAMPTZ  DEFAULT NOW(),
    UNIQUE(agent_id, file_name)
);

CREATE TABLE IF NOT EXISTS user_context_files (
    id         UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    agent_id   UUID         NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    user_id    UUID         NOT NULL REFERENCES users(id)  ON DELETE CASCADE,
    file_name  VARCHAR(255) NOT NULL,
    content    TEXT         NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ  DEFAULT NOW(),
    updated_at TIMESTAMPTZ  DEFAULT NOW(),
    UNIQUE(agent_id, user_id, file_name)
);

CREATE INDEX idx_user_context_files_user ON user_context_files(user_id);

CREATE TABLE IF NOT EXISTS user_agent_profiles (
    agent_id      UUID        NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    user_id       UUID        NOT NULL REFERENCES users(id)  ON DELETE CASCADE,
    workspace     TEXT,
    metadata      JSONB       DEFAULT '{}',
    first_seen_at TIMESTAMPTZ DEFAULT NOW(),
    last_seen_at  TIMESTAMPTZ DEFAULT NOW(),
    PRIMARY KEY (agent_id, user_id)
);

CREATE INDEX idx_user_agent_profiles_user ON user_agent_profiles(user_id);

CREATE TABLE IF NOT EXISTS user_agent_overrides (
    id         UUID        NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    agent_id   UUID        NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    user_id    UUID        NOT NULL REFERENCES users(id)  ON DELETE CASCADE,
    provider   VARCHAR(50),
    model      VARCHAR(200),
    settings   JSONB       NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(agent_id, user_id)
);

CREATE INDEX idx_user_agent_overrides_user ON user_agent_overrides(user_id);

-- ============================================================
-- Section 2: API Keys & Agent Links
-- ============================================================

CREATE TABLE IF NOT EXISTS api_keys (
    id            UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    name          VARCHAR(100) NOT NULL,
    prefix        VARCHAR(8)   NOT NULL,
    key_hash      VARCHAR(64)  NOT NULL UNIQUE,
    scopes        JSONB        NOT NULL DEFAULT '[]',
    expires_at    TIMESTAMPTZ,
    last_used_at  TIMESTAMPTZ,
    revoked       BOOLEAN      NOT NULL DEFAULT FALSE,
    created_by    VARCHAR(255),
    owner_user_id UUID         NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_api_keys_key_hash ON api_keys(key_hash) WHERE NOT revoked;
CREATE INDEX idx_api_keys_prefix   ON api_keys(prefix);
CREATE INDEX idx_api_keys_owner    ON api_keys(owner_user_id);

CREATE TABLE IF NOT EXISTS agent_links (
    id              UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    source_agent_id UUID         NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    target_agent_id UUID         NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    direction       VARCHAR(20)  NOT NULL DEFAULT 'outbound',
    description     TEXT,
    max_concurrent  INT          NOT NULL DEFAULT 3,
    settings        JSONB        NOT NULL DEFAULT '{}',
    status          VARCHAR(20)  NOT NULL DEFAULT 'active',
    created_by      VARCHAR(255) NOT NULL,
    team_id         UUID,
    metadata        JSONB        NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ  DEFAULT NOW(),
    updated_at      TIMESTAMPTZ  DEFAULT NOW(),
    UNIQUE(source_agent_id, target_agent_id),
    CHECK (source_agent_id != target_agent_id)
);

CREATE INDEX idx_agent_links_source ON agent_links(source_agent_id) WHERE status = 'active';
CREATE INDEX idx_agent_links_target ON agent_links(target_agent_id) WHERE status = 'active';

-- ============================================================
-- Section 3: Teams & Collaboration
-- ============================================================

CREATE TABLE IF NOT EXISTS agent_teams (
    id            UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    name          VARCHAR(255) NOT NULL,
    lead_agent_id UUID         NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    description   TEXT,
    status        VARCHAR(20)  NOT NULL DEFAULT 'active',
    settings      JSONB        NOT NULL DEFAULT '{}',
    created_by    VARCHAR(255) NOT NULL,
    owner_user_id UUID         REFERENCES users(id) ON DELETE SET NULL,
    -- Stable workspace folder identifier derived from team name.
    -- Generated once at insert time; immutable thereafter.
    team_key      VARCHAR(100) NOT NULL UNIQUE,
    metadata      JSONB        NOT NULL DEFAULT '{}',
    created_at    TIMESTAMPTZ  DEFAULT NOW(),
    updated_at    TIMESTAMPTZ  DEFAULT NOW()
);

CREATE INDEX idx_agent_teams_owner ON agent_teams(owner_user_id);

CREATE TABLE IF NOT EXISTS agent_team_members (
    team_id   UUID        NOT NULL REFERENCES agent_teams(id) ON DELETE CASCADE,
    agent_id  UUID        NOT NULL REFERENCES agents(id)      ON DELETE CASCADE,
    role      VARCHAR(20) NOT NULL DEFAULT 'member',
    joined_at TIMESTAMPTZ DEFAULT NOW(),
    PRIMARY KEY (team_id, agent_id)
);

CREATE TABLE IF NOT EXISTS team_user_grants (
    id         UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    team_id    UUID         NOT NULL REFERENCES agent_teams(id) ON DELETE CASCADE,
    user_id    UUID         NOT NULL REFERENCES users(id)       ON DELETE CASCADE,
    role       VARCHAR(50)  NOT NULL DEFAULT 'viewer',
    granted_by VARCHAR(255),
    created_at TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    UNIQUE(team_id, user_id)
);

CREATE INDEX idx_team_user_grants_user ON team_user_grants(user_id);
CREATE INDEX idx_team_user_grants_team ON team_user_grants(team_id);

CREATE TABLE IF NOT EXISTS team_tasks (
    id                  UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    team_id             UUID         NOT NULL REFERENCES agent_teams(id) ON DELETE CASCADE,
    subject             VARCHAR(500) NOT NULL,
    description         TEXT,
    status              VARCHAR(20)  NOT NULL DEFAULT 'pending',
    owner_agent_id      UUID         REFERENCES agents(id) ON DELETE SET NULL,
    blocked_by          JSONB        NOT NULL DEFAULT '[]',
    priority            INT          NOT NULL DEFAULT 0,
    result              TEXT,
    metadata            JSONB        NOT NULL DEFAULT '{}',
    user_id             UUID         REFERENCES users(id) ON DELETE SET NULL,
    channel             VARCHAR(50),
    task_type           VARCHAR(30)  NOT NULL DEFAULT 'general',
    task_number         INT          NOT NULL DEFAULT 0,
    identifier          VARCHAR(20),
    created_by_agent_id UUID         REFERENCES agents(id) ON DELETE SET NULL,
    assignee_user_id    UUID         REFERENCES users(id) ON DELETE SET NULL,
    parent_id           UUID         REFERENCES team_tasks(id) ON DELETE SET NULL,
    chat_id             VARCHAR(255) DEFAULT '',
    locked_at           TIMESTAMPTZ,
    lock_expires_at     TIMESTAMPTZ,
    progress_percent    INT          DEFAULT 0 CHECK (progress_percent BETWEEN 0 AND 100),
    progress_step       TEXT,
    followup_at         TIMESTAMPTZ,
    followup_count      INT          NOT NULL DEFAULT 0,
    followup_max        INT          NOT NULL DEFAULT 0,
    followup_message    TEXT,
    followup_channel    VARCHAR(60),
    followup_chat_id    VARCHAR(255),
    confidence_score    REAL,
    comment_count       INT          NOT NULL DEFAULT 0,
    attachment_count    INT          NOT NULL DEFAULT 0,
    custom_scope        TEXT,
    tsv                 tsvector GENERATED ALWAYS AS (
        to_tsvector('simple', coalesce(subject,'') || ' ' || coalesce(description,''))
    ) STORED,
    embedding           halfvec(3072),
    created_at          TIMESTAMPTZ  DEFAULT NOW(),
    updated_at          TIMESTAMPTZ  DEFAULT NOW()
);

CREATE INDEX idx_team_tasks_team         ON team_tasks(team_id);
CREATE INDEX idx_team_tasks_status       ON team_tasks(team_id, status);
CREATE INDEX idx_team_tasks_user_scope   ON team_tasks(team_id, user_id) WHERE user_id IS NOT NULL;
CREATE INDEX idx_tt_parent               ON team_tasks(parent_id)        WHERE parent_id IS NOT NULL;
CREATE INDEX idx_tt_scope                ON team_tasks(team_id, channel, chat_id);
CREATE INDEX idx_tt_type                 ON team_tasks(team_id, task_type);
CREATE INDEX idx_tt_lock                 ON team_tasks(lock_expires_at)   WHERE lock_expires_at IS NOT NULL AND status = 'in_progress';
CREATE UNIQUE INDEX idx_tt_identifier    ON team_tasks(team_id, identifier) WHERE identifier IS NOT NULL;
CREATE INDEX idx_tt_followup             ON team_tasks(followup_at)       WHERE followup_at IS NOT NULL AND status = 'in_progress';
CREATE INDEX idx_tt_owner_status         ON team_tasks(team_id, owner_agent_id, status);
CREATE INDEX idx_tt_blocked_by           ON team_tasks USING gin(blocked_by);
CREATE INDEX idx_tt_tsv                  ON team_tasks USING gin(tsv);
CREATE INDEX idx_tt_embedding            ON team_tasks USING hnsw(embedding halfvec_cosine_ops) WITH (m = 16, ef_construction = 64);

CREATE TABLE IF NOT EXISTS team_task_comments (
    id               UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    task_id          UUID         NOT NULL REFERENCES team_tasks(id)  ON DELETE CASCADE,
    agent_id         UUID         REFERENCES agents(id)               ON DELETE SET NULL,
    user_id          UUID         REFERENCES users(id)                ON DELETE SET NULL,
    content          TEXT         NOT NULL,
    metadata         JSONB        DEFAULT '{}',
    comment_type     VARCHAR(20)  NOT NULL DEFAULT 'note',
    confidence_score REAL,
    custom_scope     TEXT,
    created_at       TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_ttc_task ON team_task_comments(task_id);

CREATE TABLE IF NOT EXISTS team_task_events (
    id           UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    task_id      UUID         NOT NULL REFERENCES team_tasks(id) ON DELETE CASCADE,
    event_type   VARCHAR(30)  NOT NULL,
    actor_type   VARCHAR(10)  NOT NULL,
    actor_id     VARCHAR(255) NOT NULL,
    data         JSONB,
    custom_scope TEXT,
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_tte_task ON team_task_events(task_id);

CREATE TABLE IF NOT EXISTS team_task_attachments (
    id                   UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    task_id              UUID         NOT NULL REFERENCES team_tasks(id)   ON DELETE CASCADE,
    team_id              UUID         NOT NULL REFERENCES agent_teams(id)  ON DELETE CASCADE,
    chat_id              VARCHAR(255) NOT NULL DEFAULT '',
    path                 TEXT         NOT NULL,
    base_name            TEXT         NOT NULL DEFAULT '',
    file_size            BIGINT       NOT NULL DEFAULT 0,
    mime_type            VARCHAR(100) DEFAULT '',
    created_by_agent_id  UUID         REFERENCES agents(id),
    created_by_sender_id VARCHAR(255) DEFAULT '',
    metadata             JSONB        NOT NULL DEFAULT '{}',
    custom_scope         TEXT,
    created_at           TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    UNIQUE(task_id, path)
);

CREATE INDEX idx_tta_task ON team_task_attachments(task_id);
CREATE INDEX idx_tta_team ON team_task_attachments(team_id);

-- ============================================================
-- Section 3b: Team-user membership
-- ============================================================
-- User↔team membership, distinct from agent↔team in agent_team_members.
-- Roles (viewer/member/admin) control what a user can do as a team member
-- (manage agents, invite users, accept grants). Separate from project_grants.role.
-- added_by is kept for audit; SET NULL on granter delete preserves the row.

CREATE TABLE IF NOT EXISTS team_user_members (
    team_id    UUID        NOT NULL REFERENCES agent_teams(id) ON DELETE CASCADE,
    user_id    UUID        NOT NULL REFERENCES users(id)       ON DELETE CASCADE,
    role       VARCHAR(20) NOT NULL CHECK (role IN ('viewer', 'member', 'admin')),
    added_by   UUID        REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (team_id, user_id)
);

CREATE INDEX idx_team_user_members_user ON team_user_members(user_id);

-- ============================================================
-- Section 3c: Projects
-- ============================================================
-- Top-level entity owned by a user. Slug is immutable post-create
-- (FS path coupling). Archive via status change; no hard delete in rc1.

CREATE TABLE IF NOT EXISTS projects (
    id            UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    slug          VARCHAR(100) NOT NULL UNIQUE
                      CHECK (slug ~ '^[a-z0-9][a-z0-9-]{1,98}[a-z0-9]$'),
    owner_user_id UUID         NOT NULL REFERENCES users(id) ON DELETE RESTRICT, -- transfer or archive project before user deletion
    status        VARCHAR(20)  NOT NULL DEFAULT 'active'
                      CHECK (status IN ('active', 'archived')),
    metadata      JSONB        NOT NULL DEFAULT '{}',
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_projects_owner  ON projects(owner_user_id);
CREATE INDEX idx_projects_status ON projects(status);

-- ============================================================
-- Section 3d: Project grants
-- ============================================================
-- Controls who (user or team) can access a project and at what role.
-- Exactly one of user_id/team_id must be set (XOR via CHECK constraint).
-- granted_by uses SET NULL so audit rows survive granter deletion.
-- UNIQUE NULLS NOT DISTINCT prevents duplicate (project, user) or (project, team) pairs.

CREATE TABLE IF NOT EXISTS project_grants (
    id          UUID        NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    project_id  UUID        NOT NULL REFERENCES projects(id)     ON DELETE CASCADE,
    user_id     UUID                 REFERENCES users(id)        ON DELETE CASCADE,
    team_id     UUID                 REFERENCES agent_teams(id)  ON DELETE CASCADE,
    role        VARCHAR(20) NOT NULL CHECK (role IN ('viewer', 'member', 'editor')),
    granted_by  UUID                 REFERENCES users(id)        ON DELETE SET NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (
        (user_id IS NOT NULL AND team_id IS NULL) OR
        (user_id IS NULL     AND team_id IS NOT NULL)
    ),
    UNIQUE NULLS NOT DISTINCT (project_id, user_id, team_id)
);

CREATE INDEX idx_project_grants_project ON project_grants(project_id);
CREATE INDEX idx_project_grants_user    ON project_grants(user_id)  WHERE user_id IS NOT NULL;
CREATE INDEX idx_project_grants_team    ON project_grants(team_id)  WHERE team_id IS NOT NULL;

-- ============================================================
-- Section 4: Agent Sessions (renamed from v3 "sessions")
-- ============================================================

CREATE TABLE IF NOT EXISTS agent_sessions (
    id                            UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    session_key                   VARCHAR(500) NOT NULL,
    agent_id                      UUID         REFERENCES agents(id)      ON DELETE CASCADE,
    user_id                       UUID         REFERENCES users(id)       ON DELETE SET NULL,
    messages                      JSONB        NOT NULL DEFAULT '[]',
    summary                       TEXT,
    model                         VARCHAR(200),
    provider                      VARCHAR(50),
    channel                       VARCHAR(50),
    input_tokens                  BIGINT       NOT NULL DEFAULT 0,
    output_tokens                 BIGINT       NOT NULL DEFAULT 0,
    compaction_count              INT          NOT NULL DEFAULT 0,
    memory_flush_compaction_count INT          NOT NULL DEFAULT 0,
    memory_flush_at               BIGINT       DEFAULT 0,
    label                         VARCHAR(500),
    spawned_by                    VARCHAR(200),
    spawn_depth                   INT          NOT NULL DEFAULT 0,
    metadata                      JSONB        DEFAULT '{}',
    team_id                       UUID         REFERENCES agent_teams(id) ON DELETE SET NULL,
    project_id                    UUID         REFERENCES projects(id)    ON DELETE SET NULL,
    created_at                    TIMESTAMPTZ  DEFAULT NOW(),
    updated_at                    TIMESTAMPTZ  DEFAULT NOW()
);

CREATE UNIQUE INDEX idx_agent_sessions_session_key ON agent_sessions(session_key);
CREATE INDEX idx_agent_sessions_agent       ON agent_sessions(agent_id);
CREATE INDEX idx_agent_sessions_user        ON agent_sessions(user_id);
CREATE INDEX idx_agent_sessions_updated     ON agent_sessions(updated_at DESC);
CREATE INDEX idx_agent_sessions_user_agent  ON agent_sessions(user_id, agent_id);
CREATE INDEX idx_agent_sessions_team        ON agent_sessions(team_id)    WHERE team_id    IS NOT NULL;
CREATE INDEX idx_agent_sessions_project     ON agent_sessions(project_id) WHERE project_id IS NOT NULL;

-- ============================================================
-- Section 4b: Channel contacts
-- ============================================================
-- Defined here (before Section 5+) because memory_documents, memory_chunks,
-- episodic_summaries, kg_entities, traces, and spans all carry a contact_id
-- FK to channel_contacts(id). Channel-instance metadata is in Section 8.

CREATE TABLE IF NOT EXISTS channel_contacts (
    id                 UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    channel_type       VARCHAR(50)  NOT NULL,
    channel_instance   VARCHAR(255),
    sender_id          VARCHAR(255) NOT NULL,
    user_id            UUID         REFERENCES users(id) ON DELETE SET NULL,
    display_name       VARCHAR(255),
    username           VARCHAR(255),
    avatar_url         TEXT,
    peer_kind          VARCHAR(20),
    contact_type       VARCHAR(20)  NOT NULL DEFAULT 'user',
    thread_id          VARCHAR(100),
    thread_type        VARCHAR(20),
    metadata           JSONB        DEFAULT '{}',
    merge_audit        JSONB        NOT NULL DEFAULT '{}',
    merged_id          UUID         REFERENCES users(id) ON DELETE SET NULL,
    default_project_id UUID         REFERENCES projects(id) ON DELETE SET NULL,
    first_seen_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    last_seen_at       TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX idx_channel_contacts_type_sender
    ON channel_contacts(channel_type, sender_id, COALESCE(thread_id, ''));
CREATE INDEX idx_channel_contacts_instance       ON channel_contacts(channel_instance)       WHERE channel_instance IS NOT NULL;
CREATE INDEX idx_channel_contacts_merged         ON channel_contacts(merged_id)              WHERE merged_id IS NOT NULL;
CREATE INDEX idx_channel_contacts_search         ON channel_contacts(display_name, username);
CREATE INDEX idx_channel_contacts_user           ON channel_contacts(user_id)                WHERE user_id IS NOT NULL;
CREATE INDEX idx_channel_contacts_default_project ON channel_contacts(default_project_id)    WHERE default_project_id IS NOT NULL;

-- ============================================================
-- Section 5: Memory
-- ============================================================

CREATE TABLE IF NOT EXISTS memory_documents (
    id           UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    agent_id     UUID         NOT NULL REFERENCES agents(id)            ON DELETE CASCADE,
    team_id      UUID         REFERENCES agent_teams(id)                ON DELETE CASCADE,
    user_id      UUID         REFERENCES users(id)                      ON DELETE CASCADE,
    contact_id   UUID         REFERENCES channel_contacts(id)           ON DELETE CASCADE,
    project_id   UUID         REFERENCES projects(id)                   ON DELETE SET NULL,
    path         VARCHAR(500) NOT NULL,
    file_path    VARCHAR(500) NOT NULL DEFAULT '',
    content_hash VARCHAR(64)  NOT NULL DEFAULT '',
    version      INT          NOT NULL DEFAULT 1,
    metadata     JSONB        NOT NULL DEFAULT '{}',
    created_at   TIMESTAMPTZ  DEFAULT NOW(),
    updated_at   TIMESTAMPTZ  DEFAULT NOW()
);

-- 5D scope unique: same agent cannot have two docs at the same path under the same full scope.
-- COALESCE collapses NULLs so that (agent, NULL, NULL, NULL, NULL, path) is a single unique slot.
CREATE UNIQUE INDEX idx_memdoc_unique ON memory_documents(
    agent_id,
    COALESCE(team_id::text,    ''),
    COALESCE(user_id::text,    ''),
    COALESCE(contact_id::text, ''),
    COALESCE(project_id::text, ''),
    path
);
CREATE INDEX idx_memdoc_agent_user ON memory_documents(agent_id, user_id);
CREATE INDEX idx_memdoc_team       ON memory_documents(team_id)    WHERE team_id    IS NOT NULL;
CREATE INDEX idx_memdoc_user       ON memory_documents(user_id)    WHERE user_id    IS NOT NULL;
CREATE INDEX idx_memdoc_contact    ON memory_documents(contact_id) WHERE contact_id IS NOT NULL;
CREATE INDEX idx_memdoc_project    ON memory_documents(project_id) WHERE project_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS memory_chunks (
    id           UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    agent_id     UUID         NOT NULL REFERENCES agents(id)            ON DELETE CASCADE,
    document_id  UUID         REFERENCES memory_documents(id)           ON DELETE CASCADE,
    team_id      UUID         REFERENCES agent_teams(id)                ON DELETE CASCADE,
    user_id      UUID         REFERENCES users(id)                      ON DELETE CASCADE,
    contact_id   UUID         REFERENCES channel_contacts(id)           ON DELETE CASCADE,
    project_id   UUID         REFERENCES projects(id)                   ON DELETE SET NULL,
    path         TEXT         NOT NULL,
    start_line   INT          NOT NULL DEFAULT 0,
    end_line     INT          NOT NULL DEFAULT 0,
    hash         VARCHAR(64)  NOT NULL,
    text         TEXT         NOT NULL,
    embedding    halfvec(3072),
    tsv          tsvector GENERATED ALWAYS AS (to_tsvector('simple', coalesce(text,''))) STORED,
    created_at   TIMESTAMPTZ  DEFAULT NOW(),
    updated_at   TIMESTAMPTZ  DEFAULT NOW()
);

CREATE INDEX idx_mem_agent_user   ON memory_chunks(agent_id, user_id);
CREATE INDEX idx_mem_global       ON memory_chunks(agent_id)        WHERE user_id    IS NULL;
CREATE INDEX idx_mem_document     ON memory_chunks(document_id);
CREATE INDEX idx_memchunk_team    ON memory_chunks(team_id)         WHERE team_id    IS NOT NULL;
CREATE INDEX idx_memchunk_contact ON memory_chunks(contact_id)      WHERE contact_id IS NOT NULL;
CREATE INDEX idx_memchunk_project ON memory_chunks(project_id)      WHERE project_id IS NOT NULL;
CREATE INDEX idx_mem_embedding    ON memory_chunks USING hnsw(embedding halfvec_cosine_ops) WITH (m = 16, ef_construction = 64);
CREATE INDEX idx_mem_tsv          ON memory_chunks USING gin(tsv);

CREATE TABLE IF NOT EXISTS embedding_cache (
    hash       VARCHAR(64)  NOT NULL,
    provider   VARCHAR(50)  NOT NULL,
    model      VARCHAR(200) NOT NULL,
    dims       INT          NOT NULL DEFAULT 0,
    embedding  halfvec(3072),
    created_at TIMESTAMPTZ  DEFAULT NOW(),
    updated_at TIMESTAMPTZ  DEFAULT NOW(),
    PRIMARY KEY (hash, provider, model)
);

CREATE TABLE IF NOT EXISTS episodic_summaries (
    id               UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    agent_id         UUID         NOT NULL REFERENCES agents(id)          ON DELETE CASCADE,
    user_id          UUID         REFERENCES users(id)                    ON DELETE CASCADE,
    team_id          UUID         REFERENCES agent_teams(id)              ON DELETE CASCADE,
    contact_id       UUID         REFERENCES channel_contacts(id)         ON DELETE CASCADE,
    project_id       UUID         REFERENCES projects(id)                 ON DELETE SET NULL,
    session_key      TEXT         NOT NULL,
    summary          TEXT         NOT NULL,
    l0_abstract      TEXT         NOT NULL DEFAULT '',
    key_topics       JSONB        NOT NULL DEFAULT '[]',
    source_type      TEXT         NOT NULL DEFAULT 'session',
    source_id        TEXT,
    turn_count       INTEGER      NOT NULL DEFAULT 0,
    token_count      INTEGER      NOT NULL DEFAULT 0,
    created_at       TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    expires_at       TIMESTAMPTZ,
    promoted_at      TIMESTAMPTZ,
    recall_count     INTEGER      NOT NULL DEFAULT 0,
    recall_score     REAL         NOT NULL DEFAULT 0,
    last_recalled_at TIMESTAMPTZ
);

CREATE INDEX idx_episodic_agent_user ON episodic_summaries(agent_id, user_id);
-- 5D-aware dedup: same agent cannot have two episodic summaries for the same source
-- under the same full scope. COALESCE collapses NULLs for index uniqueness.
CREATE UNIQUE INDEX idx_episodic_source_dedup ON episodic_summaries(
    agent_id,
    COALESCE(user_id::text,    ''),
    COALESCE(team_id::text,    ''),
    COALESCE(contact_id::text, ''),
    COALESCE(project_id::text, ''),
    source_id
) WHERE source_id IS NOT NULL;
CREATE INDEX idx_episodic_team    ON episodic_summaries(team_id)    WHERE team_id    IS NOT NULL;
CREATE INDEX idx_episodic_contact ON episodic_summaries(contact_id) WHERE contact_id IS NOT NULL;
CREATE INDEX idx_episodic_project ON episodic_summaries(project_id) WHERE project_id IS NOT NULL;
CREATE INDEX idx_episodic_unpromoted    ON episodic_summaries(agent_id, user_id, created_at)
    WHERE promoted_at IS NULL;
CREATE INDEX idx_episodic_recall_unpromoted ON episodic_summaries(agent_id, user_id, recall_score DESC)
    WHERE promoted_at IS NULL;

-- ============================================================
-- Section 6: Knowledge Graph
-- ============================================================

CREATE TABLE IF NOT EXISTS kg_entities (
    id          UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    agent_id    UUID         NOT NULL REFERENCES agents(id)       ON DELETE CASCADE,
    user_id     UUID         REFERENCES users(id)                 ON DELETE CASCADE,
    team_id     UUID         REFERENCES agent_teams(id)           ON DELETE CASCADE,
    contact_id  UUID         REFERENCES channel_contacts(id)      ON DELETE CASCADE,
    project_id  UUID         REFERENCES projects(id)              ON DELETE SET NULL,
    external_id VARCHAR(255) NOT NULL,
    name        TEXT         NOT NULL,
    entity_type VARCHAR(100) NOT NULL,
    description TEXT         DEFAULT '',
    properties  JSONB        DEFAULT '{}',
    source_id   VARCHAR(255) DEFAULT '',
    confidence  REAL         NOT NULL DEFAULT 1.0,
    embedding   halfvec(3072),
    tsv         tsvector GENERATED ALWAYS AS (
        to_tsvector('simple', coalesce(name,'') || ' ' || coalesce(description,''))
    ) STORED,
    created_at  TIMESTAMPTZ  DEFAULT NOW(),
    updated_at  TIMESTAMPTZ  DEFAULT NOW(),
    valid_from  TIMESTAMPTZ  DEFAULT NOW(),
    valid_until TIMESTAMPTZ
);

CREATE UNIQUE INDEX idx_kg_entities_unique ON kg_entities(agent_id, COALESCE(user_id::text,''), external_id);
CREATE INDEX idx_kg_entities_scope   ON kg_entities(agent_id, user_id);
CREATE INDEX idx_kg_entities_type    ON kg_entities(agent_id, user_id, entity_type);
CREATE INDEX idx_kg_entities_current ON kg_entities(agent_id, user_id) WHERE valid_until IS NULL;
CREATE INDEX idx_kg_entities_team    ON kg_entities(team_id)    WHERE team_id    IS NOT NULL;
CREATE INDEX idx_kg_contact          ON kg_entities(contact_id) WHERE contact_id IS NOT NULL;
CREATE INDEX idx_kg_project          ON kg_entities(project_id) WHERE project_id IS NOT NULL;
CREATE INDEX idx_kg_embedding        ON kg_entities USING hnsw(embedding halfvec_cosine_ops) WITH (m = 16, ef_construction = 64);
CREATE INDEX idx_kg_entities_tsv     ON kg_entities USING gin(tsv);

CREATE TABLE IF NOT EXISTS kg_relations (
    id               UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    agent_id         UUID         NOT NULL REFERENCES agents(id)      ON DELETE CASCADE,
    user_id          UUID         REFERENCES users(id)                ON DELETE SET NULL,
    source_entity_id UUID         NOT NULL REFERENCES kg_entities(id) ON DELETE CASCADE,
    relation_type    VARCHAR(200) NOT NULL,
    target_entity_id UUID         NOT NULL REFERENCES kg_entities(id) ON DELETE CASCADE,
    confidence       REAL         NOT NULL DEFAULT 1.0,
    properties       JSONB        DEFAULT '{}',
    team_id          UUID         REFERENCES agent_teams(id)          ON DELETE SET NULL,
    created_at       TIMESTAMPTZ  DEFAULT NOW(),
    valid_from       TIMESTAMPTZ  DEFAULT NOW(),
    valid_until      TIMESTAMPTZ
);

CREATE UNIQUE INDEX idx_kg_relations_unique ON kg_relations(agent_id, COALESCE(user_id::text,''), source_entity_id, relation_type, target_entity_id);

CREATE INDEX idx_kg_relations_source  ON kg_relations(source_entity_id, relation_type);
CREATE INDEX idx_kg_relations_target  ON kg_relations(target_entity_id);
CREATE INDEX idx_kg_relations_current ON kg_relations(agent_id, user_id) WHERE valid_until IS NULL;
CREATE INDEX idx_kg_relations_team    ON kg_relations(team_id)           WHERE team_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS kg_dedup_candidates (
    id          UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    agent_id    UUID         NOT NULL REFERENCES agents(id)      ON DELETE CASCADE,
    user_id     UUID         REFERENCES users(id)                ON DELETE SET NULL,
    entity_a_id UUID         NOT NULL REFERENCES kg_entities(id) ON DELETE CASCADE,
    entity_b_id UUID         NOT NULL REFERENCES kg_entities(id) ON DELETE CASCADE,
    similarity  REAL         NOT NULL,
    status      VARCHAR(20)  NOT NULL DEFAULT 'pending',
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    UNIQUE(entity_a_id, entity_b_id)
);

CREATE INDEX idx_kg_dedup_agent ON kg_dedup_candidates(agent_id, status);

-- ============================================================
-- Section 7: Vault
-- ============================================================

CREATE TABLE IF NOT EXISTS vault_documents (
    id            UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    agent_id      UUID         REFERENCES agents(id)      ON DELETE SET NULL,
    owner_user_id UUID         REFERENCES users(id)       ON DELETE SET NULL,
    team_id       UUID         REFERENCES agent_teams(id) ON DELETE SET NULL,
    chat_id       TEXT,
    scope         TEXT         NOT NULL DEFAULT 'personal',
    custom_scope  TEXT,
    path          TEXT         NOT NULL,
    path_basename TEXT         NOT NULL DEFAULT '',
    title         TEXT         NOT NULL DEFAULT '',
    doc_type      TEXT         NOT NULL DEFAULT 'note',
    content_hash  TEXT         NOT NULL DEFAULT '',
    summary       TEXT         NOT NULL DEFAULT '',
    metadata      JSONB        DEFAULT '{}',
    embedding     halfvec(3072),
    tsv           tsvector GENERATED ALWAYS AS (
        to_tsvector('simple', coalesce(title,'') || ' ' || coalesce(path,''))
    ) STORED,
    created_at    TIMESTAMPTZ  DEFAULT NOW(),
    updated_at    TIMESTAMPTZ  DEFAULT NOW(),
    CONSTRAINT vault_documents_scope_check CHECK (
        scope IN ('personal', 'team', 'shared', 'custom')
    ),
    CONSTRAINT vault_documents_scope_consistency CHECK (
        (scope = 'personal' AND agent_id IS NOT NULL AND team_id IS NULL) OR
        (scope = 'team'     AND team_id  IS NOT NULL AND agent_id IS NULL) OR
        (scope = 'shared'   AND agent_id IS NULL     AND team_id IS NULL)  OR
        scope = 'custom'
    )
);

CREATE UNIQUE INDEX idx_vault_docs_unique_path
    ON vault_documents(scope, COALESCE(custom_scope,''), path, COALESCE(owner_user_id::text,''));
CREATE INDEX idx_vault_docs_agent_scope  ON vault_documents(agent_id, scope);
CREATE INDEX idx_vault_docs_type         ON vault_documents(agent_id, doc_type);
CREATE INDEX idx_vault_docs_hash         ON vault_documents(content_hash);
CREATE INDEX idx_vault_docs_team         ON vault_documents(team_id);
CREATE INDEX idx_vault_docs_team_chat    ON vault_documents(team_id, chat_id) WHERE team_id IS NOT NULL;
CREATE INDEX idx_vault_docs_basename     ON vault_documents(path_basename);
CREATE INDEX idx_vault_docs_path_prefix  ON vault_documents(path);
CREATE INDEX idx_vault_docs_tsv          ON vault_documents USING gin(tsv);
CREATE INDEX idx_vault_docs_embedding    ON vault_documents USING hnsw(embedding halfvec_cosine_ops) WITH (m = 16, ef_construction = 64);
CREATE INDEX idx_vault_docs_delegation   ON vault_documents((metadata->>'delegation_id'))
    WHERE metadata->>'delegation_id' IS NOT NULL;

CREATE TABLE IF NOT EXISTS vault_links (
    id           UUID        NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    from_doc_id  UUID        NOT NULL REFERENCES vault_documents(id) ON DELETE CASCADE,
    to_doc_id    UUID        NOT NULL REFERENCES vault_documents(id) ON DELETE CASCADE,
    link_type    TEXT        NOT NULL DEFAULT 'wikilink',
    context      TEXT        NOT NULL DEFAULT '',
    metadata     JSONB       NOT NULL DEFAULT '{}',
    custom_scope TEXT,
    created_at   TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(from_doc_id, to_doc_id, link_type)
);

CREATE INDEX idx_vault_links_from ON vault_links(from_doc_id);
CREATE INDEX idx_vault_links_to   ON vault_links(to_doc_id);

CREATE TABLE IF NOT EXISTS vault_versions (
    id         UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    doc_id     UUID         NOT NULL REFERENCES vault_documents(id) ON DELETE CASCADE,
    version    INT          NOT NULL DEFAULT 1,
    content    TEXT         NOT NULL DEFAULT '',
    changed_by TEXT         NOT NULL DEFAULT '',
    custom_scope TEXT,
    created_at TIMESTAMPTZ  DEFAULT NOW(),
    UNIQUE(doc_id, version)
);

CREATE INDEX idx_vault_versions_doc ON vault_versions(doc_id);

-- ============================================================
-- Section 8: Skills
-- ============================================================

CREATE TABLE IF NOT EXISTS skills (
    id              UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    name            VARCHAR(255) NOT NULL,
    slug            VARCHAR(255) NOT NULL,
    description     TEXT,
    owner_id        VARCHAR(255) NOT NULL,
    visibility      VARCHAR(10)  NOT NULL DEFAULT 'private',
    version         INT          NOT NULL DEFAULT 1,
    status          VARCHAR(20)  NOT NULL DEFAULT 'active',
    frontmatter     JSONB        NOT NULL DEFAULT '{}',
    file_path       TEXT         NOT NULL,
    file_size       BIGINT       NOT NULL DEFAULT 0,
    file_hash       VARCHAR(64),
    tags            JSONB,
    source          VARCHAR(20)  NOT NULL DEFAULT 'user-uploaded'
                    CHECK (source IN ('builtin', 'hub-verified', 'hub-unverified', 'agent-created', 'user-uploaded')),
    deps            JSONB        NOT NULL DEFAULT '{}',
    enabled         BOOLEAN      NOT NULL DEFAULT TRUE,
    embedding       halfvec(3072),
    last_used_at    TIMESTAMPTZ,
    last_viewed_at  TIMESTAMPTZ,
    last_patched_at TIMESTAMPTZ,
    pinned          BOOLEAN      NOT NULL DEFAULT FALSE,
    usage_count     BIGINT       NOT NULL DEFAULT 0,
    metadata        JSONB        NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ  DEFAULT NOW(),
    updated_at      TIMESTAMPTZ  DEFAULT NOW()
);

CREATE UNIQUE INDEX idx_skills_slug       ON skills(slug) WHERE status = 'active';
CREATE INDEX idx_skills_owner             ON skills(owner_id);
CREATE INDEX idx_skills_visibility        ON skills(visibility)  WHERE status = 'active';
CREATE INDEX idx_skills_source            ON skills(source)      WHERE status = 'active';
CREATE INDEX idx_skills_enabled           ON skills(enabled)     WHERE enabled = FALSE;
CREATE INDEX idx_skills_pinned            ON skills(pinned)      WHERE pinned = TRUE;
CREATE INDEX idx_skills_embedding         ON skills USING hnsw(embedding halfvec_cosine_ops) WITH (m = 16, ef_construction = 64);

CREATE TABLE IF NOT EXISTS skill_agent_grants (
    id             UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    skill_id       UUID         NOT NULL REFERENCES skills(id) ON DELETE CASCADE,
    agent_id       UUID         NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    pinned_version INT          NOT NULL,
    granted_by     VARCHAR(255) NOT NULL,
    created_at     TIMESTAMPTZ  DEFAULT NOW(),
    UNIQUE(skill_id, agent_id)
);

CREATE INDEX idx_skill_agent_grants_agent ON skill_agent_grants(agent_id);

CREATE TABLE IF NOT EXISTS skill_user_grants (
    id         UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    skill_id   UUID         NOT NULL REFERENCES skills(id) ON DELETE CASCADE,
    user_id    UUID         NOT NULL REFERENCES users(id)  ON DELETE CASCADE,
    granted_by VARCHAR(255) NOT NULL,
    created_at TIMESTAMPTZ  DEFAULT NOW(),
    UNIQUE(skill_id, user_id)
);

CREATE INDEX idx_skill_user_grants_user ON skill_user_grants(user_id);

CREATE TABLE IF NOT EXISTS skill_versions (
    id           UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    skill_id     UUID         NOT NULL REFERENCES skills(id) ON DELETE CASCADE,
    version      INT          NOT NULL,
    file_hash    VARCHAR(64)  NOT NULL,
    file_path    TEXT         NOT NULL,
    file_size    BIGINT       NOT NULL DEFAULT 0,
    frontmatter  JSONB        NOT NULL DEFAULT '{}',
    content      TEXT         NOT NULL DEFAULT '',
    changelog    TEXT,
    published_by VARCHAR(255),
    archived_at  TIMESTAMPTZ,
    archive_path TEXT,
    metadata     JSONB        NOT NULL DEFAULT '{}',
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    UNIQUE(skill_id, version)
);

CREATE INDEX idx_skill_versions_skill    ON skill_versions(skill_id);
CREATE INDEX idx_skill_versions_archived ON skill_versions(skill_id) WHERE archived_at IS NOT NULL;

CREATE TABLE IF NOT EXISTS curator_runs (
    id           UUID        NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    skill_id     UUID        REFERENCES skills(id) ON DELETE SET NULL,
    status       VARCHAR(20) NOT NULL DEFAULT 'running'
                 CHECK (status IN ('running', 'completed', 'failed')),
    result       JSONB,
    error        TEXT,
    triggered_by VARCHAR(255),
    started_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at  TIMESTAMPTZ
);

CREATE INDEX idx_curator_runs_skill  ON curator_runs(skill_id);
CREATE INDEX idx_curator_runs_status ON curator_runs(status);

CREATE TABLE IF NOT EXISTS curator_events (
    id         UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    run_id     UUID         NOT NULL REFERENCES curator_runs(id) ON DELETE CASCADE,
    event_type VARCHAR(32)  NOT NULL,
    payload    JSONB        NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_curator_events_run ON curator_events(run_id, created_at);

-- ============================================================
-- Section 9: Channels
-- ============================================================

CREATE TABLE IF NOT EXISTS channel_instances (
    id           UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    name         VARCHAR(100) NOT NULL,
    display_name VARCHAR(255) DEFAULT '',
    channel_type VARCHAR(50)  NOT NULL,
    agent_id     UUID         NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    credentials  BYTEA,
    config       JSONB        DEFAULT '{}',
    enabled      BOOLEAN      DEFAULT TRUE,
    created_by   VARCHAR(255) DEFAULT '',
    metadata     JSONB        NOT NULL DEFAULT '{}',
    created_at   TIMESTAMPTZ  DEFAULT NOW(),
    updated_at   TIMESTAMPTZ  DEFAULT NOW()
);

CREATE UNIQUE INDEX idx_channel_instances_name ON channel_instances(name);
CREATE INDEX idx_channel_instances_type        ON channel_instances(channel_type);
CREATE INDEX idx_channel_instances_agent       ON channel_instances(agent_id);

CREATE TABLE IF NOT EXISTS channel_pending_messages (
    id              UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    channel_name    VARCHAR(100) NOT NULL,
    history_key     VARCHAR(200) NOT NULL,
    sender          VARCHAR(255) NOT NULL,
    sender_id       VARCHAR(255) NOT NULL DEFAULT '',
    body            TEXT         NOT NULL,
    platform_msg_id VARCHAR(100) NOT NULL DEFAULT '',
    is_summary      BOOLEAN      NOT NULL DEFAULT FALSE,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_channel_pending_messages_lookup ON channel_pending_messages(channel_name, history_key, created_at);

-- channel_contacts moved to Section 4b (defined before Section 5 to satisfy FK
-- references from memory_documents, memory_chunks, episodic_summaries,
-- kg_entities, traces, and spans).

CREATE TABLE IF NOT EXISTS pairing_requests (
    id         UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    code       VARCHAR(8)   NOT NULL UNIQUE,
    sender_id  VARCHAR(200) NOT NULL,
    channel    VARCHAR(255) NOT NULL,
    chat_id    VARCHAR(200) NOT NULL,
    account_id VARCHAR(100) NOT NULL DEFAULT 'default',
    metadata   JSONB        DEFAULT '{}',
    expires_at TIMESTAMPTZ  NOT NULL,
    created_at TIMESTAMPTZ  DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS paired_devices (
    id         UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    sender_id  VARCHAR(200) NOT NULL,
    channel    VARCHAR(255) NOT NULL,
    chat_id    VARCHAR(200) NOT NULL,
    user_id    UUID         REFERENCES users(id) ON DELETE SET NULL,
    paired_by  VARCHAR(100) NOT NULL DEFAULT 'operator',
    metadata   JSONB        DEFAULT '{}',
    expires_at TIMESTAMPTZ,
    paired_at  TIMESTAMPTZ  DEFAULT NOW()
);

CREATE UNIQUE INDEX idx_paired_devices_sender_channel ON paired_devices(sender_id, channel);
CREATE INDEX idx_paired_devices_user                  ON paired_devices(user_id) WHERE user_id IS NOT NULL;

-- ============================================================
-- Section 10: Cron & Heartbeat
-- ============================================================

CREATE TABLE IF NOT EXISTS cron_jobs (
    id               UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    agent_id         UUID         REFERENCES agents(id)      ON DELETE CASCADE,
    user_id          UUID         REFERENCES users(id)       ON DELETE SET NULL,
    name             VARCHAR(255) NOT NULL,
    enabled          BOOLEAN      NOT NULL DEFAULT TRUE,
    schedule_kind    VARCHAR(10)  NOT NULL,
    cron_expression  VARCHAR(100),
    interval_ms      BIGINT,
    run_at           TIMESTAMPTZ,
    timezone         VARCHAR(50),
    payload          TEXT         NOT NULL,
    delete_after_run BOOLEAN      NOT NULL DEFAULT FALSE,
    stateless        INTEGER      NOT NULL DEFAULT 0,
    deliver          INTEGER      NOT NULL DEFAULT 0,
    deliver_channel  TEXT         NOT NULL DEFAULT '',
    deliver_to       TEXT         NOT NULL DEFAULT '',
    wake_heartbeat   INTEGER      NOT NULL DEFAULT 0,
    next_run_at      TIMESTAMPTZ,
    last_run_at      TIMESTAMPTZ,
    last_status      VARCHAR(20),
    last_error       TEXT,
    team_id          UUID         REFERENCES agent_teams(id) ON DELETE SET NULL,
    metadata         JSONB        NOT NULL DEFAULT '{}',
    created_at       TIMESTAMPTZ  DEFAULT NOW(),
    updated_at       TIMESTAMPTZ  DEFAULT NOW()
);

CREATE INDEX idx_cron_jobs_user          ON cron_jobs(user_id);
CREATE INDEX idx_cron_jobs_agent_user    ON cron_jobs(agent_id, user_id);
CREATE INDEX idx_cron_jobs_team          ON cron_jobs(team_id)  WHERE team_id IS NOT NULL;
CREATE UNIQUE INDEX uq_cron_jobs_agent_name ON cron_jobs(agent_id, name);

CREATE TABLE IF NOT EXISTS cron_run_logs (
    id            UUID        NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    job_id        UUID        NOT NULL REFERENCES cron_jobs(id)    ON DELETE CASCADE,
    agent_id      UUID        REFERENCES agents(id)                ON DELETE SET NULL,
    status        VARCHAR(20) NOT NULL,
    summary       TEXT,
    error         TEXT,
    duration_ms   INT,
    input_tokens  INT         DEFAULT 0,
    output_tokens INT         DEFAULT 0,
    team_id       UUID        REFERENCES agent_teams(id)           ON DELETE SET NULL,
    ran_at        TIMESTAMPTZ DEFAULT NOW(),
    created_at    TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX idx_cron_run_logs_job  ON cron_run_logs(job_id, ran_at DESC);
CREATE INDEX idx_cron_run_logs_team ON cron_run_logs(team_id) WHERE team_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS agent_heartbeats (
    id                 UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    agent_id           UUID         NOT NULL UNIQUE REFERENCES agents(id) ON DELETE CASCADE,
    enabled            BOOLEAN      NOT NULL DEFAULT FALSE,
    interval_sec       INT          NOT NULL DEFAULT 1800,
    prompt             TEXT,
    provider_id        UUID         REFERENCES llm_providers(id) ON DELETE SET NULL,
    model              VARCHAR(200),
    isolated_session   BOOLEAN      NOT NULL DEFAULT TRUE,
    light_context      BOOLEAN      NOT NULL DEFAULT FALSE,
    ack_max_chars      INT          NOT NULL DEFAULT 300,
    max_retries        INT          NOT NULL DEFAULT 2,
    active_hours_start VARCHAR(5),
    active_hours_end   VARCHAR(5),
    timezone           TEXT,
    channel            VARCHAR(50),
    chat_id            TEXT,
    next_run_at        TIMESTAMPTZ,
    last_run_at        TIMESTAMPTZ,
    last_status        VARCHAR(20),
    last_error         TEXT,
    run_count          INT          NOT NULL DEFAULT 0,
    suppress_count     INT          NOT NULL DEFAULT 0,
    metadata           JSONB        DEFAULT '{}',
    created_at         TIMESTAMPTZ  DEFAULT NOW(),
    updated_at         TIMESTAMPTZ  DEFAULT NOW()
);

CREATE INDEX idx_heartbeats_due ON agent_heartbeats(next_run_at) WHERE enabled = TRUE AND next_run_at IS NOT NULL;

CREATE TABLE IF NOT EXISTS heartbeat_run_logs (
    id            UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    heartbeat_id  UUID         NOT NULL REFERENCES agent_heartbeats(id) ON DELETE CASCADE,
    agent_id      UUID         NOT NULL REFERENCES agents(id)           ON DELETE CASCADE,
    status        VARCHAR(20)  NOT NULL,
    summary       TEXT,
    error         TEXT,
    duration_ms   INT,
    input_tokens  INT          DEFAULT 0,
    output_tokens INT          DEFAULT 0,
    skip_reason   VARCHAR(50),
    metadata      JSONB        DEFAULT '{}',
    ran_at        TIMESTAMPTZ  DEFAULT NOW(),
    created_at    TIMESTAMPTZ  DEFAULT NOW()
);

CREATE INDEX idx_hb_logs_heartbeat ON heartbeat_run_logs(heartbeat_id, ran_at DESC);
CREATE INDEX idx_hb_logs_agent     ON heartbeat_run_logs(agent_id,     ran_at DESC);

-- ============================================================
-- Section 11: MCP (Model Context Protocol)
-- ============================================================

CREATE TABLE IF NOT EXISTS mcp_servers (
    id           UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    name         VARCHAR(255) NOT NULL UNIQUE,
    display_name VARCHAR(255),
    transport    VARCHAR(50)  NOT NULL,
    command      TEXT,
    args         JSONB        DEFAULT '[]',
    url          TEXT,
    headers      JSONB        DEFAULT '{}',
    env          JSONB        DEFAULT '{}',
    api_key      TEXT,
    tool_prefix  VARCHAR(50),
    timeout_sec  INT          DEFAULT 60,
    settings     JSONB        NOT NULL DEFAULT '{}',
    enabled      BOOLEAN      NOT NULL DEFAULT TRUE,
    created_by   VARCHAR(255) NOT NULL,
    metadata     JSONB        NOT NULL DEFAULT '{}',
    -- Scope columns: exactly one of {both NULL (global), team_id only, project_id only}
    team_id      UUID         NULL REFERENCES agent_teams(id) ON DELETE CASCADE,
    project_id   UUID         NULL REFERENCES projects(id)    ON DELETE CASCADE,
    created_at   TIMESTAMPTZ  DEFAULT NOW(),
    updated_at   TIMESTAMPTZ  DEFAULT NOW(),
    -- 3-state scope mutex: global (both NULL), team-scoped, or project-scoped — never both set.
    CONSTRAINT mcp_servers_scope_exclusive CHECK (
        (team_id IS NULL     AND project_id IS NULL)     OR
        (team_id IS NOT NULL AND project_id IS NULL)     OR
        (team_id IS NULL     AND project_id IS NOT NULL)
    )
);

CREATE INDEX idx_mcp_servers_team    ON mcp_servers(team_id)    WHERE team_id    IS NOT NULL;
CREATE INDEX idx_mcp_servers_project ON mcp_servers(project_id) WHERE project_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS mcp_agent_grants (
    id               UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    server_id        UUID         NOT NULL REFERENCES mcp_servers(id) ON DELETE CASCADE,
    agent_id         UUID         NOT NULL REFERENCES agents(id)      ON DELETE CASCADE,
    enabled          BOOLEAN      NOT NULL DEFAULT TRUE,
    tool_allow       JSONB,
    tool_deny        JSONB,
    config_overrides JSONB,
    granted_by       VARCHAR(255) NOT NULL,
    created_at       TIMESTAMPTZ  DEFAULT NOW(),
    UNIQUE(server_id, agent_id)
);

CREATE INDEX idx_mcp_agent_grants_agent ON mcp_agent_grants(agent_id);

CREATE TABLE IF NOT EXISTS mcp_user_grants (
    id         UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    server_id  UUID         NOT NULL REFERENCES mcp_servers(id) ON DELETE CASCADE,
    user_id    UUID         NOT NULL REFERENCES users(id)        ON DELETE CASCADE,
    enabled    BOOLEAN      NOT NULL DEFAULT TRUE,
    tool_allow JSONB,
    tool_deny  JSONB,
    granted_by VARCHAR(255) NOT NULL,
    created_at TIMESTAMPTZ  DEFAULT NOW(),
    UNIQUE(server_id, user_id)
);

CREATE INDEX idx_mcp_user_grants_user ON mcp_user_grants(user_id);

CREATE TABLE IF NOT EXISTS mcp_access_requests (
    id           UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    server_id    UUID         NOT NULL REFERENCES mcp_servers(id) ON DELETE CASCADE,
    agent_id     UUID         REFERENCES agents(id)               ON DELETE CASCADE,
    user_id      UUID         REFERENCES users(id)                ON DELETE CASCADE,
    scope        VARCHAR(10)  NOT NULL,
    status       VARCHAR(20)  NOT NULL DEFAULT 'pending',
    reason       TEXT,
    tool_allow   JSONB,
    requested_by VARCHAR(255) NOT NULL,
    reviewed_by  VARCHAR(255),
    reviewed_at  TIMESTAMPTZ,
    review_note  TEXT,
    created_at   TIMESTAMPTZ  DEFAULT NOW(),
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

-- Partial UNIQUE index: prevents duplicate pending requests for the same
-- (server, agent, user) tuple while allowing re-request after deny or revoke.
-- NULLS NOT DISTINCT treats NULL agent_id or user_id as equal for uniqueness
-- within the pending subset, so two agent-scope pending rows with the same
-- server+agent (user_id NULL both) are correctly de-duplicated.
CREATE UNIQUE INDEX mcp_access_requests_unique_pending
    ON mcp_access_requests (server_id, agent_id, user_id)
    NULLS NOT DISTINCT
    WHERE status = 'pending';

CREATE INDEX idx_mcp_requests_status ON mcp_access_requests(status) WHERE status = 'pending';
CREATE INDEX idx_mcp_requests_server ON mcp_access_requests(server_id);

CREATE TABLE IF NOT EXISTS mcp_user_credentials (
    id         UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    server_id  UUID         NOT NULL REFERENCES mcp_servers(id) ON DELETE CASCADE,
    user_id    UUID         NOT NULL REFERENCES users(id)        ON DELETE CASCADE,
    api_key    TEXT,
    headers    BYTEA,
    env        BYTEA,
    created_at TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    UNIQUE(server_id, user_id)
);

CREATE INDEX idx_mcp_user_credentials_server ON mcp_user_credentials(server_id);
CREATE INDEX idx_mcp_user_credentials_user   ON mcp_user_credentials(user_id);

-- ============================================================
-- Section 12: Tracing
-- ============================================================

CREATE TABLE IF NOT EXISTS traces (
    id                  UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    agent_id            UUID         REFERENCES agents(id)      ON DELETE SET NULL,
    user_id             UUID         REFERENCES users(id)       ON DELETE SET NULL,
    session_key         TEXT,
    run_id              TEXT,
    start_time          TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    end_time            TIMESTAMPTZ,
    duration_ms         INT,
    name                TEXT,
    channel             VARCHAR(50),
    input_preview       TEXT,
    output_preview      TEXT,
    total_input_tokens  INT          DEFAULT 0,
    total_output_tokens INT          DEFAULT 0,
    total_cost          NUMERIC(12,6) DEFAULT 0,
    span_count          INT          DEFAULT 0,
    llm_call_count      INT          DEFAULT 0,
    tool_call_count     INT          DEFAULT 0,
    status              VARCHAR(20)  DEFAULT 'running',
    error               TEXT,
    metadata            JSONB,
    tags                JSONB,
    parent_trace_id     UUID         REFERENCES traces(id) ON DELETE SET NULL,
    team_id             UUID         REFERENCES agent_teams(id) ON DELETE SET NULL,
    contact_id          UUID         REFERENCES channel_contacts(id) ON DELETE SET NULL,
    created_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_traces_agent_time  ON traces(agent_id,      created_at DESC);
CREATE INDEX idx_traces_user_time   ON traces(user_id,       created_at DESC) WHERE user_id IS NOT NULL;
CREATE INDEX idx_traces_session     ON traces(session_key,   created_at DESC) WHERE session_key IS NOT NULL;
CREATE INDEX idx_traces_status      ON traces(status)                         WHERE status = 'error';
CREATE INDEX idx_traces_parent      ON traces(parent_trace_id)                WHERE parent_trace_id IS NOT NULL;
CREATE INDEX idx_traces_quota       ON traces(user_id,       created_at DESC) WHERE parent_trace_id IS NULL AND user_id IS NOT NULL;
CREATE INDEX idx_traces_start_root  ON traces(start_time DESC)                WHERE parent_trace_id IS NULL;
CREATE INDEX idx_traces_team        ON traces(team_id,       created_at DESC) WHERE team_id IS NOT NULL;
CREATE INDEX idx_traces_contact     ON traces(contact_id)                     WHERE contact_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS spans (
    id             UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    trace_id       UUID         NOT NULL REFERENCES traces(id) ON DELETE CASCADE,
    parent_span_id UUID         REFERENCES spans(id) ON DELETE SET NULL,
    agent_id       UUID         REFERENCES agents(id) ON DELETE SET NULL,
    span_type      VARCHAR(20)  NOT NULL,
    name           TEXT,
    start_time     TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    end_time       TIMESTAMPTZ,
    duration_ms    INT,
    status         VARCHAR(20)  DEFAULT 'running',
    error          TEXT,
    level          VARCHAR(10)  DEFAULT 'DEFAULT',
    model          VARCHAR(200),
    provider       VARCHAR(50),
    input_tokens   INT,
    output_tokens  INT,
    total_cost     NUMERIC(12,8),
    finish_reason  VARCHAR(50),
    model_params   JSONB,
    tool_name      VARCHAR(200),
    tool_call_id   VARCHAR(100),
    input_preview  TEXT,
    output_preview TEXT,
    metadata       JSONB,
    team_id        UUID         REFERENCES agent_teams(id) ON DELETE SET NULL,
    contact_id     UUID         REFERENCES channel_contacts(id) ON DELETE SET NULL,
    created_at     TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_spans_trace      ON spans(trace_id,    start_time);
CREATE INDEX idx_spans_trace_type ON spans(trace_id,    span_type);
CREATE INDEX idx_spans_parent     ON spans(parent_span_id)          WHERE parent_span_id IS NOT NULL;
CREATE INDEX idx_spans_agent_time ON spans(agent_id,    created_at DESC);
CREATE INDEX idx_spans_type       ON spans(span_type,   created_at DESC);
CREATE INDEX idx_spans_model      ON spans(model,        created_at DESC) WHERE model IS NOT NULL;
CREATE INDEX idx_spans_error      ON spans(status)                        WHERE status = 'error';
CREATE INDEX idx_spans_team       ON spans(team_id)                       WHERE team_id IS NOT NULL;
CREATE INDEX idx_spans_contact    ON spans(contact_id)                    WHERE contact_id IS NOT NULL;

-- ============================================================
-- Section 13: Tools (builtin, secure CLI, subagent tasks)
-- ============================================================

CREATE TABLE IF NOT EXISTS builtin_tools (
    name         VARCHAR(100) NOT NULL PRIMARY KEY,
    display_name VARCHAR(255) NOT NULL,
    description  TEXT         NOT NULL DEFAULT '',
    category     VARCHAR(50)  NOT NULL DEFAULT 'general',
    enabled      BOOLEAN      NOT NULL DEFAULT TRUE,
    settings     JSONB        NOT NULL DEFAULT '{}',
    requires     JSONB        DEFAULT '[]',
    metadata     JSONB        DEFAULT '{}',
    created_at   TIMESTAMPTZ  DEFAULT NOW(),
    updated_at   TIMESTAMPTZ  DEFAULT NOW()
);

CREATE INDEX idx_builtin_tools_category ON builtin_tools(category);

CREATE TABLE IF NOT EXISTS secure_cli_binaries (
    id              UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    binary_name     TEXT         NOT NULL,
    binary_path     TEXT,
    description     TEXT         NOT NULL DEFAULT '',
    encrypted_env   BYTEA        NOT NULL,
    deny_args       JSONB        NOT NULL DEFAULT '[]',
    deny_verbose    JSONB        NOT NULL DEFAULT '[]',
    timeout_seconds INTEGER      NOT NULL DEFAULT 30,
    tips            TEXT         NOT NULL DEFAULT '',
    is_global       BOOLEAN      NOT NULL DEFAULT TRUE,
    enabled         BOOLEAN      NOT NULL DEFAULT TRUE,
    created_by      TEXT         NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX idx_secure_cli_unique_binary ON secure_cli_binaries(binary_name);
CREATE INDEX idx_secure_cli_binary_name          ON secure_cli_binaries(binary_name);

CREATE TABLE IF NOT EXISTS secure_cli_agent_grants (
    id              UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    binary_id       UUID         NOT NULL REFERENCES secure_cli_binaries(id) ON DELETE CASCADE,
    agent_id        UUID         NOT NULL REFERENCES agents(id)              ON DELETE CASCADE,
    deny_args       JSONB,
    deny_verbose    JSONB,
    timeout_seconds INTEGER,
    tips            TEXT,
    enabled         BOOLEAN      NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    UNIQUE(binary_id, agent_id)
);

CREATE INDEX idx_scag_binary ON secure_cli_agent_grants(binary_id);
CREATE INDEX idx_scag_agent  ON secure_cli_agent_grants(agent_id);

CREATE TABLE IF NOT EXISTS secure_cli_user_credentials (
    id            UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    binary_id     UUID         NOT NULL REFERENCES secure_cli_binaries(id) ON DELETE CASCADE,
    user_id       UUID         NOT NULL REFERENCES users(id)               ON DELETE CASCADE,
    encrypted_env BYTEA        NOT NULL,
    metadata      JSONB        NOT NULL DEFAULT '{}',
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    UNIQUE(binary_id, user_id)
);

CREATE INDEX idx_scuc_binary ON secure_cli_user_credentials(binary_id);
CREATE INDEX idx_scuc_user   ON secure_cli_user_credentials(user_id);

CREATE TABLE IF NOT EXISTS subagent_tasks (
    id               UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
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
    origin_user_id   UUID         REFERENCES users(id) ON DELETE SET NULL,
    spawned_by       TEXT,
    completed_at     TIMESTAMPTZ,
    archived_at      TIMESTAMPTZ,
    metadata         JSONB        NOT NULL DEFAULT '{}',
    custom_scope     TEXT,
    project_id       UUID         REFERENCES projects(id) ON DELETE SET NULL,
    created_at       TIMESTAMPTZ  DEFAULT NOW(),
    updated_at       TIMESTAMPTZ  DEFAULT NOW()
);

CREATE INDEX idx_subagent_tasks_parent_status ON subagent_tasks(parent_agent_key, status);
CREATE INDEX idx_subagent_tasks_session       ON subagent_tasks(session_key);
CREATE INDEX idx_subagent_tasks_created       ON subagent_tasks(created_at);
CREATE INDEX idx_subagent_tasks_project       ON subagent_tasks(project_id) WHERE project_id IS NOT NULL;

-- ============================================================
-- Section 14: Audit & Config
-- ============================================================

CREATE TABLE IF NOT EXISTS activity_logs (
    id          UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    actor_type  VARCHAR(20)  NOT NULL,
    actor_id    VARCHAR(255) NOT NULL,
    action      VARCHAR(100) NOT NULL,
    entity_type VARCHAR(50),
    entity_id   VARCHAR(255),
    details     JSONB,
    ip_address  VARCHAR(45),
    user_id     UUID         REFERENCES users(id) ON DELETE SET NULL,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_activity_logs_actor   ON activity_logs(actor_type, actor_id);
CREATE INDEX idx_activity_logs_action  ON activity_logs(action);
CREATE INDEX idx_activity_logs_entity  ON activity_logs(entity_type, entity_id);
CREATE INDEX idx_activity_logs_created ON activity_logs(created_at DESC);
CREATE INDEX idx_activity_logs_user    ON activity_logs(user_id) WHERE user_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS usage_snapshots (
    id                  UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    bucket_hour         TEXT         NOT NULL,
    agent_id            UUID         REFERENCES agents(id) ON DELETE SET NULL,
    provider            VARCHAR(50)  NOT NULL DEFAULT '',
    model               VARCHAR(200) NOT NULL DEFAULT '',
    channel             VARCHAR(50)  NOT NULL DEFAULT '',
    input_tokens        BIGINT       NOT NULL DEFAULT 0,
    output_tokens       BIGINT       NOT NULL DEFAULT 0,
    cache_read_tokens   BIGINT       NOT NULL DEFAULT 0,
    cache_create_tokens BIGINT       NOT NULL DEFAULT 0,
    thinking_tokens     BIGINT       NOT NULL DEFAULT 0,
    total_cost          NUMERIC(12,6) NOT NULL DEFAULT 0,
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
    created_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_usage_snapshots_bucket          ON usage_snapshots(bucket_hour DESC);
CREATE INDEX idx_usage_snapshots_agent_bucket    ON usage_snapshots(agent_id,  bucket_hour DESC);
CREATE INDEX idx_usage_snapshots_provider_bucket ON usage_snapshots(provider,  bucket_hour DESC) WHERE provider != '';
CREATE INDEX idx_usage_snapshots_channel_bucket  ON usage_snapshots(channel,   bucket_hour DESC) WHERE channel  != '';
CREATE UNIQUE INDEX idx_usage_snapshots_unique ON usage_snapshots(
    bucket_hour,
    COALESCE(agent_id::text, '00000000-0000-0000-0000-000000000000'),
    COALESCE(provider, ''),
    COALESCE(model,    ''),
    COALESCE(channel,  '')
);

CREATE TABLE IF NOT EXISTS agent_config_permissions (
    id          UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    agent_id    UUID         NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    scope       VARCHAR(255) NOT NULL,
    config_type VARCHAR(50)  NOT NULL CHECK (config_type IN ('write_file','edit_file','delete_file','cron','heartbeat','*')),
    user_id     VARCHAR(255) NOT NULL,
    permission  VARCHAR(10)  NOT NULL,
    granted_by  VARCHAR(255),
    metadata    JSONB        DEFAULT '{}',
    deny_globs  TEXT[]       NOT NULL DEFAULT ARRAY['.env*','secrets/**','.git/**','*.key','*.pem']::TEXT[],
    created_at  TIMESTAMPTZ  DEFAULT NOW(),
    updated_at  TIMESTAMPTZ  DEFAULT NOW(),
    UNIQUE(agent_id, scope, config_type, user_id)
);

CREATE INDEX idx_acp_lookup ON agent_config_permissions(agent_id, scope, config_type);
CREATE INDEX idx_acp_user   ON agent_config_permissions(user_id);

CREATE TABLE IF NOT EXISTS system_configs (
    key        VARCHAR(100) NOT NULL PRIMARY KEY,
    value      TEXT         NOT NULL,
    metadata   JSONB        NOT NULL DEFAULT '{}',
    updated_at TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS config_secrets (
    key        VARCHAR(100) NOT NULL PRIMARY KEY,
    value      BYTEA        NOT NULL,
    created_at TIMESTAMPTZ  DEFAULT NOW(),
    updated_at TIMESTAMPTZ  DEFAULT NOW()
);

-- ============================================================
-- Section 15: Hooks & Evolution
-- ============================================================

CREATE TABLE IF NOT EXISTS hooks (
    id           UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    scope        TEXT         NOT NULL CHECK (scope IN ('global', 'user', 'agent')),
    event        TEXT         NOT NULL,
    handler_type TEXT         NOT NULL CHECK (handler_type IN ('command', 'http', 'prompt', 'script')),
    config       JSONB        NOT NULL DEFAULT '{}',
    matcher      TEXT,
    if_expr      TEXT,
    timeout_ms   INTEGER      NOT NULL DEFAULT 5000,
    on_timeout   TEXT         NOT NULL DEFAULT 'block' CHECK (on_timeout IN ('block', 'allow')),
    priority     INTEGER      NOT NULL DEFAULT 0,
    enabled      BOOLEAN      NOT NULL DEFAULT TRUE,
    version      INTEGER      NOT NULL DEFAULT 1,
    source       TEXT         NOT NULL DEFAULT 'ui' CHECK (source IN ('ui', 'api', 'seed', 'builtin')),
    metadata     JSONB        NOT NULL DEFAULT '{}',
    name         TEXT,
    created_by   TEXT,
    user_id      UUID         REFERENCES users(id) ON DELETE SET NULL,
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_hooks_lookup ON hooks(event) WHERE enabled = TRUE;
CREATE INDEX idx_hooks_user   ON hooks(user_id) WHERE user_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS hook_agents (
    hook_id  UUID NOT NULL REFERENCES hooks(id)  ON DELETE CASCADE,
    agent_id UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    PRIMARY KEY (hook_id, agent_id)
);

CREATE INDEX idx_hook_agents_agent ON hook_agents(agent_id);

CREATE TABLE IF NOT EXISTS hook_executions (
    id           UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    hook_id      UUID         REFERENCES hooks(id) ON DELETE SET NULL,
    session_id   TEXT,
    event        TEXT         NOT NULL,
    input_hash   TEXT,
    decision     TEXT         NOT NULL CHECK (decision IN ('allow', 'block', 'error', 'timeout')),
    duration_ms  INTEGER      NOT NULL DEFAULT 0,
    retry        INTEGER      NOT NULL DEFAULT 0,
    dedup_key    TEXT,
    error        TEXT,
    error_detail BYTEA,
    metadata     JSONB        NOT NULL DEFAULT '{}',
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_hook_executions_session ON hook_executions(session_id, created_at);
CREATE UNIQUE INDEX uq_hook_executions_dedup ON hook_executions(dedup_key) WHERE dedup_key IS NOT NULL;

CREATE TABLE IF NOT EXISTS user_hook_budget (
    user_id        UUID         NOT NULL PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    month_start    DATE         NOT NULL,
    budget_total   INTEGER      NOT NULL DEFAULT 0,
    remaining      INTEGER      NOT NULL DEFAULT 0,
    last_warned_at TIMESTAMPTZ,
    metadata       JSONB        NOT NULL DEFAULT '{}',
    updated_at     TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

-- ============================================================
-- Section 16: Evolution
-- ============================================================

CREATE TABLE IF NOT EXISTS agent_evolution_metrics (
    id          UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    agent_id    UUID         NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    session_key TEXT         NOT NULL,
    metric_type TEXT         NOT NULL,
    metric_key  TEXT         NOT NULL,
    value       TEXT         NOT NULL,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_evo_metrics_agent_type ON agent_evolution_metrics(agent_id, metric_type);
CREATE INDEX idx_evo_metrics_created    ON agent_evolution_metrics(created_at);

CREATE TABLE IF NOT EXISTS agent_evolution_suggestions (
    id              UUID         NOT NULL PRIMARY KEY DEFAULT uuid_generate_v7(),
    agent_id        UUID         NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    suggestion_type TEXT         NOT NULL,
    suggestion      TEXT         NOT NULL,
    rationale       TEXT         NOT NULL,
    parameters      JSONB,
    status          TEXT         NOT NULL DEFAULT 'pending',
    reviewed_by     TEXT,
    reviewed_at     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_evo_suggestions_agent ON agent_evolution_suggestions(agent_id, status);

-- agent_links.team_id FK is defined inline but references agent_teams which is
-- defined later in the file. PostgreSQL resolves FKs at statement execution time
-- so this CREATE TABLE must come after agent_teams. The FK was intentionally
-- inlined in Section 2 referencing the already-created agent_teams from Section 3.
-- Because agent_links appears before agent_teams in Section 2, we use ALTER TABLE
-- to add the FK after both tables exist.
ALTER TABLE agent_links
    ADD CONSTRAINT fk_agent_links_team
    FOREIGN KEY (team_id) REFERENCES agent_teams(id) ON DELETE SET NULL;

ALTER TABLE agent_shares
    ADD CONSTRAINT fk_agent_shares_team
    FOREIGN KEY (shared_with_team_id) REFERENCES agent_teams(id) ON DELETE CASCADE;
