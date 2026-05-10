//go:build sqlite || sqliteonly

package sqlitestore

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// MarkGranted transitions a pending request to 'granted' and inserts the grant
// row for the request's scope in a single transaction.
func (s *SQLiteMCPServerStore) MarkGranted(ctx context.Context, requestID uuid.UUID, reviewedBy string) error {
	if err := store.ValidateUserID(reviewedBy); err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var req store.MCPAccessRequest
	var agentID *uuid.UUID
	var userID *string
	err = tx.QueryRowContext(ctx,
		`SELECT id, server_id, agent_id, user_id, scope, tool_allow
		 FROM mcp_access_requests WHERE id = ? AND status = 'pending'`,
		requestID,
	).Scan(&req.ID, &req.ServerID, &agentID, &userID, &req.Scope, &req.ToolAllow)
	if err != nil {
		return fmt.Errorf("request not found or not pending: %w", err)
	}

	now := time.Now().UTC()
	_, err = tx.ExecContext(ctx,
		`UPDATE mcp_access_requests SET status = 'granted', reviewed_by = ?, reviewed_at = ? WHERE id = ?`,
		reviewedBy, now, requestID,
	)
	if err != nil {
		return err
	}

	switch req.Scope {
	case store.MCPRequestScopeAgent:
		if agentID == nil {
			return fmt.Errorf("agent_id required for agent scope")
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO mcp_agent_grants (id, server_id, agent_id, enabled, tool_allow, granted_by, created_at)
			 VALUES (?,?,?,1,?,?,?)
			 ON CONFLICT (server_id, agent_id) DO UPDATE SET enabled = 1, tool_allow = excluded.tool_allow, granted_by = excluded.granted_by`,
			store.GenNewID(), req.ServerID, *agentID, jsonOrNull(req.ToolAllow), reviewedBy, now,
		)
	case store.MCPRequestScopeUser:
		if userID == nil || *userID == "" {
			return fmt.Errorf("user_id required for user scope")
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO mcp_user_grants (id, server_id, user_id, enabled, tool_allow, granted_by, created_at)
			 VALUES (?,?,?,1,?,?,?)
			 ON CONFLICT (server_id, user_id) DO UPDATE SET enabled = 1, tool_allow = excluded.tool_allow, granted_by = excluded.granted_by`,
			store.GenNewID(), req.ServerID, *userID, jsonOrNull(req.ToolAllow), reviewedBy, now,
		)
	default:
		return fmt.Errorf("unknown scope: %s", req.Scope)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// MarkDenied transitions a pending request to 'denied', recording the reviewer and note.
func (s *SQLiteMCPServerStore) MarkDenied(ctx context.Context, requestID uuid.UUID, reviewedBy, note string) error {
	if err := store.ValidateUserID(reviewedBy); err != nil {
		return err
	}
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx,
		`UPDATE mcp_access_requests SET status = 'denied', reviewed_by = ?, reviewed_at = ?, review_note = ?
		 WHERE id = ? AND status = 'pending'`,
		reviewedBy, now, nilStr(note), requestID,
	)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("request not found or not pending")
	}
	return nil
}

// MarkRevoked transitions a granted request to 'revoked' and deletes the
// associated grant row in a single transaction. Re-request after revoke is
// allowed via a new CreateRequest call.
func (s *SQLiteMCPServerStore) MarkRevoked(ctx context.Context, requestID uuid.UUID) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var serverID uuid.UUID
	var agentID *uuid.UUID
	var userID *string
	var scope string
	err = tx.QueryRowContext(ctx,
		`SELECT server_id, agent_id, user_id, scope FROM mcp_access_requests WHERE id = ? AND status = 'granted'`,
		requestID,
	).Scan(&serverID, &agentID, &userID, &scope)
	if err != nil {
		return fmt.Errorf("request not found or not granted: %w", err)
	}

	switch scope {
	case store.MCPRequestScopeAgent:
		if agentID != nil {
			_, err = tx.ExecContext(ctx,
				`DELETE FROM mcp_agent_grants WHERE server_id = ? AND agent_id = ?`,
				serverID, *agentID,
			)
			if err != nil {
				return err
			}
		}
	case store.MCPRequestScopeUser:
		if userID != nil && *userID != "" {
			_, err = tx.ExecContext(ctx,
				`DELETE FROM mcp_user_grants WHERE server_id = ? AND user_id = ?`,
				serverID, *userID,
			)
			if err != nil {
				return err
			}
		}
	}

	_, err = tx.ExecContext(ctx,
		`UPDATE mcp_access_requests SET status = 'revoked' WHERE id = ? AND status = 'granted'`,
		requestID,
	)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// ReviewRequest is a legacy helper that delegates to MarkGranted or MarkDenied.
func (s *SQLiteMCPServerStore) ReviewRequest(ctx context.Context, requestID uuid.UUID, approved bool, reviewedBy, note string) error {
	if approved {
		return s.MarkGranted(ctx, requestID, reviewedBy)
	}
	return s.MarkDenied(ctx, requestID, reviewedBy, note)
}
