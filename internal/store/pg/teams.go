package pg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/identity"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// PGTeamStore implements store.TeamStore backed by Postgres.
type PGTeamStore struct {
	db          *sql.DB
	embProvider store.EmbeddingProvider
}

// SetEmbeddingProvider sets the embedding provider for semantic task search.
func (s *PGTeamStore) SetEmbeddingProvider(p store.EmbeddingProvider) {
	s.embProvider = p
}

func NewPGTeamStore(db *sql.DB) *PGTeamStore {
	return &PGTeamStore{db: db}
}

// --- Column constants ---

const teamSelectCols = `id, name, lead_agent_id, description, status, settings, created_by, team_key, metadata, created_at, updated_at`

// ============================================================
// Team CRUD
// ============================================================

func (s *PGTeamStore) CreateTeam(ctx context.Context, team *store.TeamData) error {
	if team.ID == uuid.Nil {
		team.ID = store.GenNewID()
	}
	now := time.Now()
	team.CreatedAt = now
	team.UpdatedAt = now

	settings := team.Settings
	if len(settings) == 0 {
		settings = json.RawMessage(`{}`)
	}

	// Auto-generate stable slug from team name when caller did not supply one.
	if team.TeamKey == "" {
		team.TeamKey = identity.SlugFromName(team.Name, team.ID.String()[:6])
	}

	metadata := team.Metadata
	if len(metadata) == 0 {
		metadata = json.RawMessage(`{}`)
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_teams (id, name, lead_agent_id, description, status, settings, created_by, team_key, metadata, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		team.ID, team.Name, team.LeadAgentID, team.Description,
		team.Status, settings, team.CreatedBy, team.TeamKey, metadata, now, now,
	)
	return err
}

func (s *PGTeamStore) GetTeam(ctx context.Context, teamID uuid.UUID) (*store.TeamData, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+teamSelectCols+` FROM agent_teams WHERE id = $1`, teamID)
	return scanTeamRow(row)
}

func (s *PGTeamStore) UpdateTeam(ctx context.Context, teamID uuid.UUID, updates map[string]any) error {
	// Immutability: team_key is set at creation and must never change.
	delete(updates, "team_key")
	if len(updates) == 0 {
		return nil
	}
	return execMapUpdate(ctx, s.db, "agent_teams", teamID, updates)
}

func (s *PGTeamStore) DeleteTeam(ctx context.Context, teamID uuid.UUID) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM agent_teams WHERE id = $1`, teamID)
	return err
}

func (s *PGTeamStore) ListTeams(ctx context.Context) ([]store.TeamData, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT t.id, t.name, t.lead_agent_id, t.description, t.status, t.settings, t.created_by, t.team_key, t.metadata, t.created_at, t.updated_at,
		 COALESCE(a.agent_key, '') AS lead_agent_key,
		 COALESCE(a.display_name, '') AS lead_display_name
		 FROM agent_teams t
		 LEFT JOIN agents a ON a.id = t.lead_agent_id
		 ORDER BY t.created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var teams []store.TeamData
	teamIndex := map[uuid.UUID]int{} // map team ID → index in teams slice
	for rows.Next() {
		var d store.TeamData
		var desc sql.NullString
		if err := rows.Scan(
			&d.ID, &d.Name, &d.LeadAgentID, &desc, &d.Status,
			&d.Settings, &d.CreatedBy, &d.TeamKey, &d.Metadata, &d.CreatedAt, &d.UpdatedAt,
			&d.LeadAgentKey, &d.LeadDisplayName,
		); err != nil {
			return nil, err
		}
		if desc.Valid {
			d.Description = desc.String
		}
		teamIndex[d.ID] = len(teams)
		teams = append(teams, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Bulk-fetch all members for returned teams
	if len(teams) > 0 {
		mRows, err := s.db.QueryContext(ctx,
			`SELECT m.team_id, m.agent_id, m.role, m.joined_at,
			 COALESCE(a.agent_key, '') AS agent_key,
			 COALESCE(a.display_name, '') AS display_name,
			 COALESCE(a.frontmatter, '') AS frontmatter,
			 COALESCE(a.emoji, '') AS emoji
			 FROM agent_team_members m
			 JOIN agents a ON a.id = m.agent_id
			 WHERE a.status = 'active'
			 ORDER BY m.joined_at`)
		if err != nil {
			return nil, err
		}
		defer mRows.Close()

		for mRows.Next() {
			var m store.TeamMemberData
			if err := mRows.Scan(&m.TeamID, &m.AgentID, &m.Role, &m.JoinedAt, &m.AgentKey, &m.DisplayName, &m.Frontmatter, &m.Emoji); err != nil {
				return nil, err
			}
			if idx, ok := teamIndex[m.TeamID]; ok {
				teams[idx].Members = append(teams[idx].Members, m)
				teams[idx].MemberCount++
			}
		}
		if err := mRows.Err(); err != nil {
			return nil, err
		}
	}

	return teams, nil
}

// ============================================================
// Members
// ============================================================

func (s *PGTeamStore) AddMember(ctx context.Context, teamID, agentID uuid.UUID, role string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_team_members (team_id, agent_id, role, joined_at)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (team_id, agent_id) DO UPDATE SET role = EXCLUDED.role`,
		teamID, agentID, role, time.Now(),
	)
	return err
}

func (s *PGTeamStore) RemoveMember(ctx context.Context, teamID, agentID uuid.UUID) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM agent_team_members WHERE team_id = $1 AND agent_id = $2`,
		teamID, agentID,
	)
	return err
}

func (s *PGTeamStore) ListMembers(ctx context.Context, teamID uuid.UUID) ([]store.TeamMemberData, error) {
	var members []store.TeamMemberData
	err := pkgSqlxDB.SelectContext(ctx, &members, `
		SELECT m.team_id, m.agent_id, m.role, m.joined_at,
		 COALESCE(a.agent_key, '') AS agent_key,
		 COALESCE(a.display_name, '') AS display_name,
		 COALESCE(a.frontmatter, '') AS frontmatter,
		 COALESCE(a.emoji, '') AS emoji
		 FROM agent_team_members m
		 JOIN agents a ON a.id = m.agent_id
		 JOIN agent_teams at2 ON at2.id = m.team_id
		 WHERE m.team_id = $1 AND a.status = 'active'
		 ORDER BY m.joined_at`, teamID)
	return members, err
}

func (s *PGTeamStore) ListIdleMembers(ctx context.Context, teamID uuid.UUID) ([]store.TeamMemberData, error) {
	var members []store.TeamMemberData
	err := pkgSqlxDB.SelectContext(ctx, &members, `
		SELECT m.team_id, m.agent_id, m.role, m.joined_at,
		 COALESCE(a.agent_key, '') AS agent_key,
		 COALESCE(a.display_name, '') AS display_name,
		 COALESCE(a.frontmatter, '') AS frontmatter,
		 COALESCE(a.emoji, '') AS emoji
		 FROM agent_team_members m
		 JOIN agents a ON a.id = m.agent_id
		 JOIN agent_teams at2 ON at2.id = m.team_id
		 WHERE m.team_id = $1 AND a.status = 'active' AND m.role != $2
		   AND NOT EXISTS (
		     SELECT 1 FROM team_tasks tt
		     WHERE tt.owner_agent_id = m.agent_id AND tt.team_id = $1 AND tt.status = $3
		   )
		 ORDER BY m.joined_at`, teamID, store.TeamRoleLead, store.TeamTaskStatusInProgress)
	return members, err
}

func (s *PGTeamStore) GetTeamForAgent(ctx context.Context, agentID uuid.UUID) (*store.TeamData, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT t.id, t.name, t.lead_agent_id, t.description, t.status, t.settings, t.created_by, t.team_key, t.metadata, t.created_at, t.updated_at
		 FROM agent_teams t
		 WHERE (
		   t.lead_agent_id = $1
		   OR EXISTS (SELECT 1 FROM agent_team_members m WHERE m.team_id = t.id AND m.agent_id = $1)
		 ) AND t.status = $2
		 ORDER BY (t.lead_agent_id = $1) DESC LIMIT 1`,
		agentID, store.TeamStatusActive)
	d, err := scanTeamRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return d, err
}

func (s *PGTeamStore) KnownUserIDs(ctx context.Context, teamID uuid.UUID, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 100
	}
	var users []string
	err := pkgSqlxDB.SelectContext(ctx, &users, `
		SELECT DISTINCT s.user_id
		 FROM agent_sessions s
		 JOIN agent_team_members m ON m.agent_id = s.agent_id
		 WHERE m.team_id = $1 AND s.user_id IS NOT NULL
		 ORDER BY s.user_id LIMIT $2`, teamID, limit)
	return users, err
}

// ============================================================
// Team user grants
// ============================================================

func (s *PGTeamStore) GrantTeamAccess(ctx context.Context, teamID uuid.UUID, userID, role, grantedBy string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO team_user_grants (id, team_id, user_id, role, granted_by, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (team_id, user_id) DO UPDATE SET role = EXCLUDED.role, granted_by = EXCLUDED.granted_by`,
		store.GenNewID(), teamID, userID, role, grantedBy, time.Now(),
	)
	return err
}

func (s *PGTeamStore) RevokeTeamAccess(ctx context.Context, teamID uuid.UUID, userID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM team_user_grants WHERE team_id = $1 AND user_id = $2`,
		teamID, userID)
	return err
}

func (s *PGTeamStore) ListTeamGrants(ctx context.Context, teamID uuid.UUID) ([]store.TeamUserGrant, error) {
	var result []store.TeamUserGrant
	err := pkgSqlxDB.SelectContext(ctx, &result,
		`SELECT id, team_id, user_id, role, COALESCE(granted_by, '') AS granted_by, created_at
		 FROM team_user_grants WHERE team_id = $1 ORDER BY created_at DESC`, teamID)
	return result, err
}

func (s *PGTeamStore) ListUserTeams(ctx context.Context, userID string) ([]store.TeamData, error) {
	var teams []store.TeamData
	err := pkgSqlxDB.SelectContext(ctx, &teams, `
		SELECT id, name, lead_agent_id, COALESCE(description,'') AS description, status, settings, created_by, team_key, metadata, created_at, updated_at
		 FROM agent_teams t
		 WHERE t.status = $1
		   AND EXISTS (SELECT 1 FROM team_user_grants g WHERE g.team_id = t.id AND g.user_id = $2)
		 ORDER BY t.created_at DESC`, store.TeamStatusActive, userID)
	return teams, err
}

func (s *PGTeamStore) HasTeamAccess(ctx context.Context, teamID uuid.UUID, userID string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM team_user_grants WHERE team_id = $1 AND user_id = $2)`,
		teamID, userID,
	).Scan(&exists)
	return exists, err
}

// ============================================================
// Scan helpers
// ============================================================

func scanTeamRow(row *sql.Row) (*store.TeamData, error) {
	var d store.TeamData
	var desc sql.NullString
	err := row.Scan(
		&d.ID, &d.Name, &d.LeadAgentID, &desc, &d.Status,
		&d.Settings, &d.CreatedBy, &d.TeamKey, &d.Metadata, &d.CreatedAt, &d.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if desc.Valid {
		d.Description = desc.String
	}
	return &d, nil
}
