//go:build sqlite || sqliteonly

package sqlitestore

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// --- Agent Grants ---

func (s *SQLiteMCPServerStore) GrantToAgent(ctx context.Context, g *store.MCPAgentGrant) error {
	if err := store.ValidateUserID(g.GrantedBy); err != nil {
		return err
	}
	if g.ID == uuid.Nil {
		g.ID = store.GenNewID()
	}
	g.CreatedAt = time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO mcp_agent_grants (id, server_id, agent_id, enabled, tool_allow, tool_deny, config_overrides, granted_by, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?)
		 ON CONFLICT (server_id, agent_id) DO UPDATE SET
		   enabled = excluded.enabled, tool_allow = excluded.tool_allow,
		   tool_deny = excluded.tool_deny, config_overrides = excluded.config_overrides,
		   granted_by = excluded.granted_by`,
		g.ID, g.ServerID, g.AgentID, g.Enabled,
		jsonOrNull(g.ToolAllow), jsonOrNull(g.ToolDeny), jsonOrNull(g.ConfigOverrides),
		g.GrantedBy, g.CreatedAt,
	)
	return err
}

func (s *SQLiteMCPServerStore) RevokeFromAgent(ctx context.Context, serverID, agentID uuid.UUID) error {
	_, err := s.db.ExecContext(ctx,
		"DELETE FROM mcp_agent_grants WHERE server_id = ? AND agent_id = ?",
		serverID, agentID)
	return err
}

func (s *SQLiteMCPServerStore) ListAgentGrants(ctx context.Context, agentID uuid.UUID) ([]store.MCPAgentGrant, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, server_id, agent_id, enabled, tool_allow, tool_deny, config_overrides, granted_by, created_at
		 FROM mcp_agent_grants WHERE agent_id = ?`,
		agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []store.MCPAgentGrant
	for rows.Next() {
		var g store.MCPAgentGrant
		var toolAllow, toolDeny, configOverrides *string
		var createdAt sqliteTime
		if err := rows.Scan(&g.ID, &g.ServerID, &g.AgentID, &g.Enabled,
			&toolAllow, &toolDeny, &configOverrides, &g.GrantedBy, &createdAt); err != nil {
			continue
		}
		if toolAllow != nil {
			g.ToolAllow = json.RawMessage(*toolAllow)
		}
		if toolDeny != nil {
			g.ToolDeny = json.RawMessage(*toolDeny)
		}
		if configOverrides != nil {
			g.ConfigOverrides = json.RawMessage(*configOverrides)
		}
		g.CreatedAt = createdAt.Time
		result = append(result, g)
	}
	return result, rows.Err()
}

func (s *SQLiteMCPServerStore) ListServerGrants(ctx context.Context, serverID uuid.UUID) ([]store.MCPAgentGrant, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, server_id, agent_id, enabled,
		 COALESCE(tool_allow, '[]'), COALESCE(tool_deny, '[]'),
		 COALESCE(config_overrides, '{}'), granted_by, created_at
		 FROM mcp_agent_grants WHERE server_id = ? ORDER BY created_at`,
		serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]store.MCPAgentGrant, 0)
	for rows.Next() {
		var g store.MCPAgentGrant
		var toolAllow, toolDeny, configOverrides string
		var createdAt sqliteTime
		if err := rows.Scan(&g.ID, &g.ServerID, &g.AgentID, &g.Enabled,
			&toolAllow, &toolDeny, &configOverrides, &g.GrantedBy, &createdAt); err != nil {
			slog.Warn("mcp.list_server_grants.scan", "error", err)
			continue
		}
		g.ToolAllow = json.RawMessage(toolAllow)
		g.ToolDeny = json.RawMessage(toolDeny)
		g.ConfigOverrides = json.RawMessage(configOverrides)
		g.CreatedAt = createdAt.Time
		result = append(result, g)
	}
	return result, rows.Err()
}

// --- Counts ---

func (s *SQLiteMCPServerStore) CountAgentGrantsByServer(ctx context.Context) (map[uuid.UUID]int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT server_id, COUNT(*) FROM mcp_agent_grants GROUP BY server_id`)
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
	return result, rows.Err()
}

// --- User Grants ---

func (s *SQLiteMCPServerStore) GrantToUser(ctx context.Context, g *store.MCPUserGrant) error {
	if err := store.ValidateUserID(g.UserID); err != nil {
		return err
	}
	if err := store.ValidateUserID(g.GrantedBy); err != nil {
		return err
	}
	if g.ID == uuid.Nil {
		g.ID = store.GenNewID()
	}
	g.CreatedAt = time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO mcp_user_grants (id, server_id, user_id, enabled, tool_allow, tool_deny, granted_by, created_at)
		 VALUES (?,?,?,?,?,?,?,?)
		 ON CONFLICT (server_id, user_id) DO UPDATE SET
		   enabled = excluded.enabled, tool_allow = excluded.tool_allow,
		   tool_deny = excluded.tool_deny, granted_by = excluded.granted_by`,
		g.ID, g.ServerID, g.UserID, g.Enabled,
		jsonOrNull(g.ToolAllow), jsonOrNull(g.ToolDeny),
		g.GrantedBy, g.CreatedAt,
	)
	return err
}

func (s *SQLiteMCPServerStore) RevokeFromUser(ctx context.Context, serverID uuid.UUID, userID string) error {
	_, err := s.db.ExecContext(ctx,
		"DELETE FROM mcp_user_grants WHERE server_id = ? AND user_id = ?",
		serverID, userID)
	return err
}


