package pg

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/crypto"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// ListAccessible returns all MCP servers the agent+user pair can access,
// combined with per-grant tool filters. Used by runtime grant checker.
// Admin listing uses ListServers (no grant filter).
func (s *PGMCPServerStore) ListAccessible(ctx context.Context, agentID uuid.UUID, userID string) ([]store.MCPAccessInfo, error) {
	tClause, tArgs, _, err := scopeClauseAlias(ctx, 3, "ms")
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT ms.id, ms.name, ms.display_name, ms.transport, ms.command, ms.args, ms.url, ms.headers, ms.env,
		 ms.api_key, ms.tool_prefix, ms.timeout_sec, ms.settings, ms.enabled, ms.created_by, ms.created_at, ms.updated_at,
		 mag.tool_allow, mag.tool_deny
		 FROM mcp_servers ms
		 INNER JOIN mcp_agent_grants mag ON ms.id = mag.server_id AND mag.agent_id = $1 AND mag.enabled = true
		 LEFT JOIN mcp_user_grants mug ON ms.id = mug.server_id AND mug.user_id = $2
		 WHERE ms.enabled = true
		   AND (mug.id IS NULL OR mug.enabled = true)`+tClause,
		append([]any{agentID, userID}, tArgs...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]store.MCPAccessInfo, 0)
	for rows.Next() {
		var srv store.MCPServerData
		var displayName, command, url, apiKey, toolPrefix *string
		var args, headers, env *[]byte
		var toolAllowJSON, toolDenyJSON *[]byte

		if err := rows.Scan(
			&srv.ID, &srv.Name, &displayName, &srv.Transport, &command,
			&args, &url, &headers, &env,
			&apiKey, &toolPrefix, &srv.TimeoutSec,
			&srv.Settings, &srv.Enabled, &srv.CreatedBy, &srv.CreatedAt, &srv.UpdatedAt,
			&toolAllowJSON, &toolDenyJSON,
		); err != nil {
			continue
		}
		srv.DisplayName = derefStr(displayName)
		srv.Command = derefStr(command)
		srv.URL = derefStr(url)
		srv.ToolPrefix = derefStr(toolPrefix)
		srv.Args = derefBytes(args)
		srv.Headers = s.decryptJSONB(derefBytes(headers))
		srv.Env = s.decryptJSONB(derefBytes(env))
		if apiKey != nil && *apiKey != "" && s.encKey != "" {
			if decrypted, err := crypto.Decrypt(*apiKey, s.encKey); err == nil {
				srv.APIKey = decrypted
			}
		} else {
			srv.APIKey = derefStr(apiKey)
		}

		info := store.MCPAccessInfo{Server: srv}
		if toolAllowJSON != nil {
			json.Unmarshal(*toolAllowJSON, &info.ToolAllow)
		}
		if toolDenyJSON != nil {
			json.Unmarshal(*toolDenyJSON, &info.ToolDeny)
		}
		result = append(result, info)
	}
	return result, nil
}

// CreateRequest inserts a new access request with status='pending'.
// Shape validation (scope/agentID/userID consistency) is enforced by DB CHECK constraints;
// the app layer sets status='pending' so the caller cannot bypass the lifecycle.
func (s *PGMCPServerStore) CreateRequest(ctx context.Context, req *store.MCPAccessRequest) error {
	if err := store.ValidateUserID(req.RequestedBy); err != nil {
		return err
	}
	if req.ID == uuid.Nil {
		req.ID = store.GenNewID()
	}
	req.Status = store.MCPRequestStatusPending
	req.CreatedAt = time.Now()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO mcp_access_requests (id, server_id, agent_id, user_id, scope, status, reason, tool_allow, requested_by, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		req.ID, req.ServerID, nilUUID(req.AgentID), nilStr(req.UserID),
		req.Scope, req.Status, nilStr(req.Reason),
		jsonOrNull(req.ToolAllow), req.RequestedBy, req.CreatedAt,
	)
	return err
}

// ListPendingRequests returns all access requests with status='pending', ordered by creation time.
func (s *PGMCPServerStore) ListPendingRequests(ctx context.Context) ([]store.MCPAccessRequest, error) {
	tClause, tArgs, _, err := scopeClause(ctx, 1)
	if err != nil {
		return nil, err
	}
	var scanned []mcpAccessRequestRow
	if err := pkgSqlxDB.SelectContext(ctx, &scanned,
		`SELECT id, server_id, agent_id, user_id, scope, status, reason, tool_allow, requested_by,
		 reviewed_by, reviewed_at, review_note, created_at
		 FROM mcp_access_requests WHERE status = 'pending'`+tClause+` ORDER BY created_at`,
		tArgs...); err != nil {
		return nil, err
	}
	result := make([]store.MCPAccessRequest, 0, len(scanned))
	for i := range scanned {
		result = append(result, scanned[i].toMCPAccessRequest())
	}
	return result, nil
}

// ReviewRequest is a legacy helper that delegates to MarkGranted or MarkDenied.
// Prefer the lifecycle helpers (MarkGranted, MarkDenied, MarkRevoked) for new callers.
func (s *PGMCPServerStore) ReviewRequest(ctx context.Context, requestID uuid.UUID, approved bool, reviewedBy, note string) error {
	if approved {
		return s.MarkGranted(ctx, requestID, reviewedBy)
	}
	return s.MarkDenied(ctx, requestID, reviewedBy, note)
}
