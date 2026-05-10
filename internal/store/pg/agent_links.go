package pg

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// PGAgentLinkStore implements store.AgentLinkStore backed by Postgres.
type PGAgentLinkStore struct {
	db *sql.DB
}

func NewPGAgentLinkStore(db *sql.DB) *PGAgentLinkStore {
	return &PGAgentLinkStore{db: db}
}

const linkSelectCols = `id, source_agent_id, target_agent_id, direction, team_id, description,
	max_concurrent, settings, status, created_by, metadata, created_at, updated_at`

// linkSelectColsJoined prefixes every column with l. to avoid ambiguity in JOINs.
const linkSelectColsJoined = `l.id, l.source_agent_id, l.target_agent_id, l.direction, l.team_id, l.description,
	l.max_concurrent, l.settings, l.status, l.created_by, l.metadata, l.created_at, l.updated_at`

// targetTeamLeadCols detects if the "target" agent (the other side of the link) is a team lead.
// Uses $1 = fromAgentID to determine which side is the target via CASE.
const targetTeamLeadCols = `EXISTS(
		SELECT 1 FROM agent_teams tl
		WHERE tl.lead_agent_id = CASE WHEN l.source_agent_id = $1 THEN l.target_agent_id ELSE l.source_agent_id END
		  AND tl.status = 'active'
	 ) AS target_is_team_lead,
	 COALESCE((
		SELECT tl.name FROM agent_teams tl
		WHERE tl.lead_agent_id = CASE WHEN l.source_agent_id = $1 THEN l.target_agent_id ELSE l.source_agent_id END
		  AND tl.status = 'active'
		LIMIT 1
	 ), '') AS target_team_name`

func (s *PGAgentLinkStore) CreateLink(ctx context.Context, link *store.AgentLinkData) error {
	if link.ID == uuid.Nil {
		link.ID = store.GenNewID()
	}
	now := time.Now()
	link.CreatedAt = now
	link.UpdatedAt = now

	settings := link.Settings
	if len(settings) == 0 {
		settings = json.RawMessage(`{}`)
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_links (id, source_agent_id, target_agent_id, direction, team_id, description,
		 max_concurrent, settings, status, created_by, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		link.ID, link.SourceAgentID, link.TargetAgentID, link.Direction, link.TeamID, link.Description,
		link.MaxConcurrent, settings, link.Status, link.CreatedBy, now, now,
	)
	return err
}

func (s *PGAgentLinkStore) DeleteLink(ctx context.Context, id uuid.UUID) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM agent_links WHERE id = $1`, id)
	return err
}

func (s *PGAgentLinkStore) UpdateLink(ctx context.Context, id uuid.UUID, updates map[string]any) error {
	if len(updates) == 0 {
		return nil
	}
	updates["updated_at"] = time.Now()
	return execMapUpdate(ctx, s.db, "agent_links", id, updates)
}

func (s *PGAgentLinkStore) GetLink(ctx context.Context, id uuid.UUID) (*store.AgentLinkData, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+linkSelectCols+` FROM agent_links WHERE id = $1`, id)
	return scanLinkRow(row)
}

func (s *PGAgentLinkStore) ListLinksFrom(ctx context.Context, agentID uuid.UUID) ([]store.AgentLinkData, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+linkSelectColsJoined+`,
		 sa.agent_key AS source_agent_key,
		 COALESCE(sa.display_name, '') AS source_display_name,
		 COALESCE(sa.emoji, '') AS source_emoji,
		 ta.agent_key AS target_agent_key,
		 COALESCE(ta.display_name, '') AS target_display_name,
		 COALESCE(ta.emoji, '') AS target_emoji,
		 COALESCE(ta.frontmatter, '') AS target_description,
		 COALESCE(tm.name, '') AS team_name,
		 EXISTS(SELECT 1 FROM agent_teams tl WHERE tl.lead_agent_id = l.target_agent_id AND tl.status = 'active') AS target_is_team_lead,
		 COALESCE((SELECT tl.name FROM agent_teams tl WHERE tl.lead_agent_id = l.target_agent_id AND tl.status = 'active' LIMIT 1), '') AS target_team_name
		 FROM agent_links l
		 JOIN agents sa ON sa.id = l.source_agent_id
		 JOIN agents ta ON ta.id = l.target_agent_id
		 LEFT JOIN agent_teams tm ON tm.id = l.team_id
		 WHERE l.source_agent_id = $1
		 ORDER BY l.created_at`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanLinkRowsJoined(rows)
}

func (s *PGAgentLinkStore) ListLinksTo(ctx context.Context, agentID uuid.UUID) ([]store.AgentLinkData, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+linkSelectColsJoined+`,
		 sa.agent_key AS source_agent_key,
		 COALESCE(sa.display_name, '') AS source_display_name,
		 COALESCE(sa.emoji, '') AS source_emoji,
		 ta.agent_key AS target_agent_key,
		 COALESCE(ta.display_name, '') AS target_display_name,
		 COALESCE(ta.emoji, '') AS target_emoji,
		 COALESCE(ta.frontmatter, '') AS target_description,
		 COALESCE(tm.name, '') AS team_name,
		 EXISTS(SELECT 1 FROM agent_teams tl WHERE tl.lead_agent_id = l.target_agent_id AND tl.status = 'active') AS target_is_team_lead,
		 COALESCE((SELECT tl.name FROM agent_teams tl WHERE tl.lead_agent_id = l.target_agent_id AND tl.status = 'active' LIMIT 1), '') AS target_team_name
		 FROM agent_links l
		 JOIN agents sa ON sa.id = l.source_agent_id
		 JOIN agents ta ON ta.id = l.target_agent_id
		 LEFT JOIN agent_teams tm ON tm.id = l.team_id
		 WHERE l.target_agent_id = $1
		 ORDER BY l.created_at`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanLinkRowsJoined(rows)
}

func (s *PGAgentLinkStore) CanDelegate(ctx context.Context, fromAgentID, toAgentID uuid.UUID) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS(
			SELECT 1 FROM agent_links WHERE status = 'active' AND (
				(source_agent_id = $1 AND target_agent_id = $2 AND direction IN ('outbound', 'bidirectional'))
				OR
				(source_agent_id = $2 AND target_agent_id = $1 AND direction IN ('inbound', 'bidirectional'))
			)
		)`, fromAgentID, toAgentID).Scan(&exists)
	return exists, err
}

func (s *PGAgentLinkStore) DelegateTargets(ctx context.Context, fromAgentID uuid.UUID) ([]store.AgentLinkData, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+linkSelectColsJoined+`,
		 CASE WHEN l.source_agent_id = $1 THEN sa.agent_key ELSE ta.agent_key END AS source_agent_key,
		 CASE WHEN l.source_agent_id = $1 THEN COALESCE(sa.display_name, '') ELSE COALESCE(ta.display_name, '') END AS source_display_name,
		 CASE WHEN l.source_agent_id = $1 THEN COALESCE(sa.emoji, '') ELSE COALESCE(ta.emoji, '') END AS source_emoji,
		 CASE WHEN l.source_agent_id = $1 THEN ta.agent_key ELSE sa.agent_key END AS target_agent_key,
		 CASE WHEN l.source_agent_id = $1 THEN COALESCE(ta.display_name, '') ELSE COALESCE(sa.display_name, '') END AS target_display_name,
		 CASE WHEN l.source_agent_id = $1 THEN COALESCE(ta.emoji, '') ELSE COALESCE(sa.emoji, '') END AS target_emoji,
		 CASE WHEN l.source_agent_id = $1 THEN COALESCE(ta.frontmatter, '') ELSE COALESCE(sa.frontmatter, '') END AS target_description,
		 COALESCE(tm.name, '') AS team_name,
		 `+targetTeamLeadCols+`
		 FROM agent_links l
		 JOIN agents sa ON sa.id = l.source_agent_id
		 JOIN agents ta ON ta.id = l.target_agent_id
		 LEFT JOIN agent_teams tm ON tm.id = l.team_id
		 WHERE l.status = 'active'
		   AND CASE WHEN l.source_agent_id = $1 THEN ta.status ELSE sa.status END = 'active'
		   AND (
			(l.source_agent_id = $1 AND l.direction IN ('outbound', 'bidirectional'))
			OR
			(l.target_agent_id = $1 AND l.direction IN ('inbound', 'bidirectional'))
		 )
		 ORDER BY CASE WHEN l.source_agent_id = $1 THEN ta.agent_key ELSE sa.agent_key END`, fromAgentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanLinkRowsJoined(rows)
}

func (s *PGAgentLinkStore) GetLinkBetween(ctx context.Context, fromAgentID, toAgentID uuid.UUID) (*store.AgentLinkData, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+linkSelectCols+`
		 FROM agent_links WHERE status = 'active' AND (
			(source_agent_id = $1 AND target_agent_id = $2 AND direction IN ('outbound', 'bidirectional'))
			OR
			(source_agent_id = $2 AND target_agent_id = $1 AND direction IN ('inbound', 'bidirectional'))
		 ) LIMIT 1`, fromAgentID, toAgentID)
	d, err := scanLinkRow(row)
	if err != nil {
		return nil, nil // no link found
	}
	return d, nil
}

func (s *PGAgentLinkStore) SearchDelegateTargets(ctx context.Context, fromAgentID uuid.UUID, query string, limit int) ([]store.AgentLinkData, error) {
	if limit <= 0 {
		limit = 5
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+linkSelectColsJoined+`,
		 CASE WHEN l.source_agent_id = $1 THEN sa.agent_key ELSE ta.agent_key END AS source_agent_key,
		 CASE WHEN l.source_agent_id = $1 THEN COALESCE(sa.display_name, '') ELSE COALESCE(ta.display_name, '') END AS source_display_name,
		 CASE WHEN l.source_agent_id = $1 THEN COALESCE(sa.emoji, '') ELSE COALESCE(ta.emoji, '') END AS source_emoji,
		 CASE WHEN l.source_agent_id = $1 THEN ta.agent_key ELSE sa.agent_key END AS target_agent_key,
		 CASE WHEN l.source_agent_id = $1 THEN COALESCE(ta.display_name, '') ELSE COALESCE(sa.display_name, '') END AS target_display_name,
		 CASE WHEN l.source_agent_id = $1 THEN COALESCE(ta.emoji, '') ELSE COALESCE(sa.emoji, '') END AS target_emoji,
		 CASE WHEN l.source_agent_id = $1 THEN COALESCE(ta.frontmatter, '') ELSE COALESCE(sa.frontmatter, '') END AS target_description,
		 COALESCE(tm.name, '') AS team_name,
		 `+targetTeamLeadCols+`
		 FROM agent_links l
		 JOIN agents sa ON sa.id = l.source_agent_id
		 JOIN agents ta ON ta.id = l.target_agent_id
		 LEFT JOIN agent_teams tm ON tm.id = l.team_id
		 WHERE l.status = 'active'
		   AND CASE WHEN l.source_agent_id = $1 THEN ta.status ELSE sa.status END = 'active'
		   AND (
		     (l.source_agent_id = $1 AND l.direction IN ('outbound', 'bidirectional'))
		     OR
		     (l.target_agent_id = $1 AND l.direction IN ('inbound', 'bidirectional'))
		   )
		   AND CASE WHEN l.source_agent_id = $1 THEN ta.tsv ELSE sa.tsv END @@ plainto_tsquery('simple', $2)
		 ORDER BY ts_rank(CASE WHEN l.source_agent_id = $1 THEN ta.tsv ELSE sa.tsv END, plainto_tsquery('simple', $2)) DESC
		 LIMIT $3`, fromAgentID, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanLinkRowsJoined(rows)
}

func (s *PGAgentLinkStore) SearchDelegateTargetsByEmbedding(ctx context.Context, fromAgentID uuid.UUID, embedding []float32, limit int) ([]store.AgentLinkData, error) {
	if limit <= 0 {
		limit = 5
	}
	vecStr := vectorToString(embedding)
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+linkSelectColsJoined+`,
		 CASE WHEN l.source_agent_id = $1 THEN sa.agent_key ELSE ta.agent_key END AS source_agent_key,
		 CASE WHEN l.source_agent_id = $1 THEN COALESCE(sa.display_name, '') ELSE COALESCE(ta.display_name, '') END AS source_display_name,
		 CASE WHEN l.source_agent_id = $1 THEN COALESCE(sa.emoji, '') ELSE COALESCE(ta.emoji, '') END AS source_emoji,
		 CASE WHEN l.source_agent_id = $1 THEN ta.agent_key ELSE sa.agent_key END AS target_agent_key,
		 CASE WHEN l.source_agent_id = $1 THEN COALESCE(ta.display_name, '') ELSE COALESCE(sa.display_name, '') END AS target_display_name,
		 CASE WHEN l.source_agent_id = $1 THEN COALESCE(ta.emoji, '') ELSE COALESCE(sa.emoji, '') END AS target_emoji,
		 CASE WHEN l.source_agent_id = $1 THEN COALESCE(ta.frontmatter, '') ELSE COALESCE(sa.frontmatter, '') END AS target_description,
		 COALESCE(tm.name, '') AS team_name,
		 `+targetTeamLeadCols+`
		 FROM agent_links l
		 JOIN agents sa ON sa.id = l.source_agent_id
		 JOIN agents ta ON ta.id = l.target_agent_id
		 LEFT JOIN agent_teams tm ON tm.id = l.team_id
		 WHERE l.status = 'active'
		   AND CASE WHEN l.source_agent_id = $1 THEN ta.status ELSE sa.status END = 'active'
		   AND (
		     (l.source_agent_id = $1 AND l.direction IN ('outbound', 'bidirectional'))
		     OR
		     (l.target_agent_id = $1 AND l.direction IN ('inbound', 'bidirectional'))
		   )
		   AND CASE WHEN l.source_agent_id = $1 THEN ta.embedding ELSE sa.embedding END IS NOT NULL
		 ORDER BY (CASE WHEN l.source_agent_id = $1 THEN ta.embedding ELSE sa.embedding END) <=> $2::halfvec
		 LIMIT $3`, fromAgentID, vecStr, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanLinkRowsJoined(rows)
}

func (s *PGAgentLinkStore) DeleteTeamLinksForAgent(ctx context.Context, teamID, agentID uuid.UUID) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM agent_links WHERE team_id = $1 AND (source_agent_id = $2 OR target_agent_id = $2)`,
		teamID, agentID,
	)
	return err
}

// --- scan helpers ---

func scanLinkRow(row *sql.Row) (*store.AgentLinkData, error) {
	var d store.AgentLinkData
	var desc sql.NullString
	err := row.Scan(
		&d.ID, &d.SourceAgentID, &d.TargetAgentID, &d.Direction, &d.TeamID, &desc,
		&d.MaxConcurrent, &d.Settings, &d.Status, &d.CreatedBy, &d.Metadata, &d.CreatedAt, &d.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("link not found: %w", err)
	}
	if desc.Valid {
		d.Description = desc.String
	}
	return &d, nil
}

func scanLinkRowsJoined(rows *sql.Rows) ([]store.AgentLinkData, error) {
	var links []store.AgentLinkData
	for rows.Next() {
		var d store.AgentLinkData
		var desc sql.NullString
		if err := rows.Scan(
			&d.ID, &d.SourceAgentID, &d.TargetAgentID, &d.Direction, &d.TeamID, &desc,
			&d.MaxConcurrent, &d.Settings, &d.Status, &d.CreatedBy, &d.Metadata, &d.CreatedAt, &d.UpdatedAt,
			&d.SourceAgentKey, &d.SourceDisplayName, &d.SourceEmoji,
			&d.TargetAgentKey, &d.TargetDisplayName, &d.TargetEmoji, &d.TargetDescription,
			&d.TeamName, &d.TargetIsTeamLead, &d.TargetTeamName,
		); err != nil {
			return nil, err
		}
		if desc.Valid {
			d.Description = desc.String
		}
		links = append(links, d)
	}
	return links, rows.Err()
}
