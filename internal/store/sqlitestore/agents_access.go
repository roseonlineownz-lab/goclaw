//go:build sqlite || sqliteonly

package sqlitestore

import (
	"context"
	"database/sql"
	"strings"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// CreateShare inserts a single explicit grant row (target = user XOR team).
func (s *SQLiteAgentStore) CreateShare(ctx context.Context, in store.AgentShareInput) error {
	if !store.ValidShareRole(in.Role) {
		return store.ErrInvalidShareRole
	}
	var userID, teamID any
	if in.SharedWithUserID != nil && *in.SharedWithUserID != uuid.Nil {
		userID = in.SharedWithUserID.String()
	}
	if in.SharedWithTeamID != nil && *in.SharedWithTeamID != uuid.Nil {
		teamID = in.SharedWithTeamID.String()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_shares
			(id, agent_id, shared_with_user_id, shared_with_team_id, role, created_by)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		store.GenNewID(), in.AgentID, userID, teamID, in.Role, in.CreatedBy)
	return err
}

func (s *SQLiteAgentStore) RevokeShareByUser(ctx context.Context, agentID, userID uuid.UUID) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM agent_shares WHERE agent_id = ? AND shared_with_user_id = ?`,
		agentID, userID)
	return err
}

func (s *SQLiteAgentStore) RevokeShareByTeam(ctx context.Context, agentID, teamID uuid.UUID) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM agent_shares WHERE agent_id = ? AND shared_with_team_id = ?`,
		agentID, teamID)
	return err
}

func (s *SQLiteAgentStore) ListShares(ctx context.Context, agentID uuid.UUID) ([]store.AgentShareData, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, agent_id, shared_with_user_id, shared_with_team_id, role, metadata, created_by,
		        created_at, updated_at
		   FROM agent_shares WHERE agent_id = ? ORDER BY created_at`,
		agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []store.AgentShareData
	for rows.Next() {
		var d store.AgentShareData
		var sharedUser, sharedTeam sql.NullString
		var meta *[]byte
		var createdAt, updatedAt sqliteTime
		if err := rows.Scan(&d.ID, &d.AgentID, &sharedUser, &sharedTeam, &d.Role, &meta,
			&d.CreatedBy, &createdAt, &updatedAt); err != nil {
			continue
		}
		if sharedUser.Valid {
			if u, perr := uuid.Parse(sharedUser.String); perr == nil {
				d.SharedWithUserID = &u
			}
		}
		if sharedTeam.Valid {
			if u, perr := uuid.Parse(sharedTeam.String); perr == nil {
				d.SharedWithTeamID = &u
			}
		}
		if meta != nil {
			d.Metadata = *meta
		}
		d.CreatedAt = createdAt.Time
		d.UpdatedAt = updatedAt.Time
		result = append(result, d)
	}
	return result, rows.Err()
}

func (s *SQLiteAgentStore) CanAccess(ctx context.Context, agentID uuid.UUID, userID string) (bool, string, error) {
	var ownerID string
	var isDefault bool
	err := s.db.QueryRowContext(ctx,
		"SELECT owner_id, is_default FROM agents WHERE id = ? AND deleted_at IS NULL", agentID,
	).Scan(&ownerID, &isDefault)
	if err != nil {
		return false, "", nil
	}
	if ownerID == userID {
		return true, "owner", nil
	}
	if isDefault {
		return true, store.ShareRoleViewer, nil
	}
	// Direct user grant — implicit team grants are computed by the resolver.
	if !looksLikeUUID(userID) {
		return false, "", nil
	}
	var role string
	err = s.db.QueryRowContext(ctx,
		`SELECT role FROM agent_shares
		  WHERE agent_id = ? AND shared_with_user_id = ?`, agentID, userID,
	).Scan(&role)
	if err != nil {
		return false, "", nil
	}
	return true, role, nil
}

func (s *SQLiteAgentStore) ListAccessible(ctx context.Context, userID string) ([]store.AgentData, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+agentSelectCols+`
		 FROM agents
		 WHERE deleted_at IS NULL AND (
		     owner_id = ?
		     OR is_default = 1
		     OR id IN (SELECT agent_id FROM agent_shares WHERE shared_with_user_id = ?)
		     OR id IN (
		         SELECT agent_id FROM channel_instances ci
		         WHERE ci.enabled = 1
		         AND EXISTS (
		             SELECT 1 FROM json_each(json_extract(ci.config, '$.allow_from'))
		             WHERE json_each.value = ?
		         )
		     )
		 )
		 ORDER BY created_at DESC`, userID, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAgentRows(rows)
}

// looksLikeUUID reports whether s is a syntactic UUID. We use it to avoid
// passing channel-style IDs (e.g. "telegram:123") into the UUID-typed
// shared_with_user_id column where they would always miss.
func looksLikeUUID(s string) bool {
	return len(s) == 36 && strings.Count(s, "-") == 4
}
