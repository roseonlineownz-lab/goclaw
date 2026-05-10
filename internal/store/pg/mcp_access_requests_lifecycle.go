package pg

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// MarkGranted transitions a pending request to 'granted' and inserts the grant
// row for the request's scope in a single transaction.
// Only the first admin to call this wins; concurrent grant of the same request
// returns an error because the UPDATE WHERE status='pending' matches zero rows.
func (s *PGMCPServerStore) MarkGranted(ctx context.Context, requestID uuid.UUID, reviewedBy string) error {
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
	tClause, tArgs, _, err2 := scopeClause(ctx, 2)
	if err2 != nil {
		return err2
	}
	err = tx.QueryRowContext(ctx,
		`SELECT id, server_id, agent_id, user_id, scope, tool_allow
		 FROM mcp_access_requests WHERE id = $1 AND status = 'pending'`+tClause,
		append([]any{requestID}, tArgs...)...,
	).Scan(&req.ID, &req.ServerID, &agentID, &userID, &req.Scope, &req.ToolAllow)
	if err != nil {
		return fmt.Errorf("request not found or not pending: %w", err)
	}

	now := time.Now()
	_, err = tx.ExecContext(ctx,
		`UPDATE mcp_access_requests SET status = 'granted', reviewed_by = $1, reviewed_at = $2 WHERE id = $3`,
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
			 VALUES ($1,$2,$3,true,$4,$5,$6)
			 ON CONFLICT (server_id, agent_id) DO UPDATE SET enabled = true, tool_allow = EXCLUDED.tool_allow, granted_by = EXCLUDED.granted_by`,
			store.GenNewID(), req.ServerID, *agentID, jsonOrNull(req.ToolAllow), reviewedBy, now,
		)
	case store.MCPRequestScopeUser:
		if userID == nil || *userID == "" {
			return fmt.Errorf("user_id required for user scope")
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO mcp_user_grants (id, server_id, user_id, enabled, tool_allow, granted_by, created_at)
			 VALUES ($1,$2,$3,true,$4,$5,$6)
			 ON CONFLICT (server_id, user_id) DO UPDATE SET enabled = true, tool_allow = EXCLUDED.tool_allow, granted_by = EXCLUDED.granted_by`,
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
func (s *PGMCPServerStore) MarkDenied(ctx context.Context, requestID uuid.UUID, reviewedBy, note string) error {
	if err := store.ValidateUserID(reviewedBy); err != nil {
		return err
	}
	now := time.Now()
	result, err := s.db.ExecContext(ctx,
		`UPDATE mcp_access_requests SET status = 'denied', reviewed_by = $1, reviewed_at = $2, review_note = $3
		 WHERE id = $4 AND status = 'pending'`,
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
// associated grant row in a single transaction. One-way: re-request after
// revoke requires a new CreateRequest (the partial UNIQUE index allows it
// because 'revoked' rows are excluded from the pending uniqueness scope).
func (s *PGMCPServerStore) MarkRevoked(ctx context.Context, requestID uuid.UUID) error {
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
		`SELECT server_id, agent_id, user_id, scope FROM mcp_access_requests WHERE id = $1 AND status = 'granted'`,
		requestID,
	).Scan(&serverID, &agentID, &userID, &scope)
	if err != nil {
		return fmt.Errorf("request not found or not granted: %w", err)
	}

	switch scope {
	case store.MCPRequestScopeAgent:
		if agentID != nil {
			_, err = tx.ExecContext(ctx,
				`DELETE FROM mcp_agent_grants WHERE server_id = $1 AND agent_id = $2`,
				serverID, *agentID,
			)
			if err != nil {
				return err
			}
		}
	case store.MCPRequestScopeUser:
		if userID != nil && *userID != "" {
			_, err = tx.ExecContext(ctx,
				`DELETE FROM mcp_user_grants WHERE server_id = $1 AND user_id = $2`,
				serverID, *userID,
			)
			if err != nil {
				return err
			}
		}
	}

	_, err = tx.ExecContext(ctx,
		`UPDATE mcp_access_requests SET status = 'revoked' WHERE id = $1 AND status = 'granted'`,
		requestID,
	)
	if err != nil {
		return err
	}
	return tx.Commit()
}
