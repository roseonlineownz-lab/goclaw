package pg

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// --- Agent Grants ---

func (s *PGMCPServerStore) GrantToAgent(ctx context.Context, g *store.MCPAgentGrant) error {
	if err := store.ValidateUserID(g.GrantedBy); err != nil {
		return err
	}
	if g.ID == uuid.Nil {
		g.ID = store.GenNewID()
	}
	g.CreatedAt = time.Now()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO mcp_agent_grants (id, server_id, agent_id, enabled, tool_allow, tool_deny, config_overrides, granted_by, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		 ON CONFLICT (server_id, agent_id) DO UPDATE SET
		   enabled = EXCLUDED.enabled, tool_allow = EXCLUDED.tool_allow,
		   tool_deny = EXCLUDED.tool_deny, config_overrides = EXCLUDED.config_overrides,
		   granted_by = EXCLUDED.granted_by`,
		g.ID, g.ServerID, g.AgentID, g.Enabled,
		jsonOrNull(g.ToolAllow), jsonOrNull(g.ToolDeny), jsonOrNull(g.ConfigOverrides),
		g.GrantedBy, g.CreatedAt,
	)
	return err
}

func (s *PGMCPServerStore) RevokeFromAgent(ctx context.Context, serverID, agentID uuid.UUID) error {
	tClause, tArgs, _, err := scopeClause(ctx, 3)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		"DELETE FROM mcp_agent_grants WHERE server_id = $1 AND agent_id = $2"+tClause,
		append([]any{serverID, agentID}, tArgs...)...)
	return err
}

func (s *PGMCPServerStore) ListAgentGrants(ctx context.Context, agentID uuid.UUID) ([]store.MCPAgentGrant, error) {
	tClause, tArgs, _, err := scopeClause(ctx, 2)
	if err != nil {
		return nil, err
	}
	var result []store.MCPAgentGrant
	err = pkgSqlxDB.SelectContext(ctx, &result,
		`SELECT id, server_id, agent_id, enabled,
		 COALESCE(tool_allow, 'null'::jsonb) AS tool_allow,
		 COALESCE(tool_deny, 'null'::jsonb) AS tool_deny,
		 COALESCE(config_overrides, 'null'::jsonb) AS config_overrides,
		 granted_by, created_at
		 FROM mcp_agent_grants WHERE agent_id = $1`+tClause,
		append([]any{agentID}, tArgs...)...)
	return result, err
}

func (s *PGMCPServerStore) ListServerGrants(ctx context.Context, serverID uuid.UUID) ([]store.MCPAgentGrant, error) {
	tClause, tArgs, _, err := scopeClause(ctx, 2)
	if err != nil {
		return nil, err
	}
	result := make([]store.MCPAgentGrant, 0)
	err = pkgSqlxDB.SelectContext(ctx, &result,
		`SELECT id, server_id, agent_id, enabled,
		 COALESCE(tool_allow, '[]'::jsonb) AS tool_allow,
		 COALESCE(tool_deny, '[]'::jsonb) AS tool_deny,
		 COALESCE(config_overrides, '{}'::jsonb) AS config_overrides,
		 granted_by, created_at
		 FROM mcp_agent_grants WHERE server_id = $1`+tClause+` ORDER BY created_at`,
		append([]any{serverID}, tArgs...)...)
	return result, err
}

// --- Counts ---

func (s *PGMCPServerStore) CountAgentGrantsByServer(ctx context.Context) (map[uuid.UUID]int, error) {
	tClause, tArgs, _, err := scopeClause(ctx, 1)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT server_id, COUNT(*) FROM mcp_agent_grants WHERE 1=1`+tClause+` GROUP BY server_id`,
		tArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[uuid.UUID]int)
	for rows.Next() {
		var serverID uuid.UUID
		var count int
		if err := rows.Scan(&serverID, &count); err != nil {
			continue
		}
		result[serverID] = count
	}
	return result, nil
}

// --- User Grants ---

func (s *PGMCPServerStore) GrantToUser(ctx context.Context, g *store.MCPUserGrant) error {
	if err := store.ValidateUserID(g.UserID); err != nil {
		return err
	}
	if err := store.ValidateUserID(g.GrantedBy); err != nil {
		return err
	}
	if g.ID == uuid.Nil {
		g.ID = store.GenNewID()
	}
	g.CreatedAt = time.Now()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO mcp_user_grants (id, server_id, user_id, enabled, tool_allow, tool_deny, granted_by, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		 ON CONFLICT (server_id, user_id) DO UPDATE SET
		   enabled = EXCLUDED.enabled, tool_allow = EXCLUDED.tool_allow,
		   tool_deny = EXCLUDED.tool_deny, granted_by = EXCLUDED.granted_by`,
		g.ID, g.ServerID, g.UserID, g.Enabled,
		jsonOrNull(g.ToolAllow), jsonOrNull(g.ToolDeny),
		g.GrantedBy, g.CreatedAt,
	)
	return err
}

func (s *PGMCPServerStore) RevokeFromUser(ctx context.Context, serverID uuid.UUID, userID string) error {
	tClause, tArgs, _, err := scopeClause(ctx, 3)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		"DELETE FROM mcp_user_grants WHERE server_id = $1 AND user_id = $2"+tClause,
		append([]any{serverID, userID}, tArgs...)...)
	return err
}

