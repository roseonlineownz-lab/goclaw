//go:build sqlite || sqliteonly

package sqlitestore

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
func (s *SQLiteMCPServerStore) ListAccessible(ctx context.Context, agentID uuid.UUID, userID string) ([]store.MCPAccessInfo, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT ms.id, ms.name, ms.display_name, ms.transport, ms.command, ms.args, ms.url, ms.headers, ms.env,
		 ms.api_key, ms.tool_prefix, ms.timeout_sec, ms.settings, ms.enabled, ms.created_by, ms.created_at, ms.updated_at,
		 mag.tool_allow, mag.tool_deny
		 FROM mcp_servers ms
		 INNER JOIN mcp_agent_grants mag ON ms.id = mag.server_id AND mag.agent_id = ? AND mag.enabled = 1
		 LEFT JOIN mcp_user_grants mug ON ms.id = mug.server_id AND mug.user_id = ?
		 WHERE ms.enabled = 1
		   AND (mug.id IS NULL OR mug.enabled = 1)`,
		agentID, userID)
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

		createdAt, updatedAt := scanTimePair()
		if err := rows.Scan(
			&srv.ID, &srv.Name, &displayName, &srv.Transport, &command,
			&args, &url, &headers, &env,
			&apiKey, &toolPrefix, &srv.TimeoutSec,
			&srv.Settings, &srv.Enabled, &srv.CreatedBy, createdAt, updatedAt,
			&toolAllowJSON, &toolDenyJSON,
		); err != nil {
			continue
		}
		srv.CreatedAt = createdAt.Time
		srv.UpdatedAt = updatedAt.Time
		srv.DisplayName = derefStr(displayName)
		srv.Command = derefStr(command)
		srv.URL = derefStr(url)
		srv.ToolPrefix = derefStr(toolPrefix)
		srv.Args = derefBytes(args)
		srv.Headers = s.decryptJSON(derefBytes(headers))
		srv.Env = s.decryptJSON(derefBytes(env))
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
	return result, rows.Err()
}

// CreateRequest inserts a new access request with status='pending'.
func (s *SQLiteMCPServerStore) CreateRequest(ctx context.Context, req *store.MCPAccessRequest) error {
	if err := store.ValidateUserID(req.RequestedBy); err != nil {
		return err
	}
	if req.ID == uuid.Nil {
		req.ID = store.GenNewID()
	}
	req.Status = store.MCPRequestStatusPending
	req.CreatedAt = time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO mcp_access_requests (id, server_id, agent_id, user_id, scope, status, reason, tool_allow, requested_by, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`,
		req.ID, req.ServerID, nilUUID(req.AgentID), nilStr(req.UserID),
		req.Scope, req.Status, nilStr(req.Reason),
		jsonOrNull(req.ToolAllow), req.RequestedBy, req.CreatedAt,
	)
	return err
}

// ListPendingRequests returns all access requests with status='pending', ordered by creation time.
func (s *SQLiteMCPServerStore) ListPendingRequests(ctx context.Context) ([]store.MCPAccessRequest, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, server_id, agent_id, user_id, scope, status, reason, tool_allow, requested_by,
		 reviewed_by, reviewed_at, review_note, created_at
		 FROM mcp_access_requests WHERE status = 'pending' ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []store.MCPAccessRequest
	for rows.Next() {
		var r store.MCPAccessRequest
		var agentID *uuid.UUID
		var userID, reviewedBy, reviewNote *string
		var reviewedAtSt, createdAtSt sqliteTime
		if err := rows.Scan(&r.ID, &r.ServerID, &agentID, &userID, &r.Scope, &r.Status,
			&r.Reason, &r.ToolAllow, &r.RequestedBy,
			&reviewedBy, &reviewedAtSt, &reviewNote, &createdAtSt); err != nil {
			continue
		}
		if !reviewedAtSt.Time.IsZero() {
			r.ReviewedAt = &reviewedAtSt.Time
		}
		r.CreatedAt = createdAtSt.Time
		r.AgentID = agentID
		r.UserID = derefStr(userID)
		r.ReviewedBy = derefStr(reviewedBy)
		r.ReviewNote = derefStr(reviewNote)
		result = append(result, r)
	}
	return result, rows.Err()
}
