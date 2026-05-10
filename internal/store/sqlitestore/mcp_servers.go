//go:build sqlite || sqliteonly

package sqlitestore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/crypto"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

const mcpServerSelectCols = `id, name, display_name, transport, command, args, url, headers, env,
		 api_key, tool_prefix, timeout_sec, settings, enabled, created_by, metadata,
		 team_id, project_id, created_at, updated_at`

// SQLiteMCPServerStore implements store.MCPServerStore backed by SQLite.
type SQLiteMCPServerStore struct {
	db     *sql.DB
	encKey string
}

func NewSQLiteMCPServerStore(db *sql.DB, encryptionKey string) *SQLiteMCPServerStore {
	return &SQLiteMCPServerStore{db: db, encKey: encryptionKey}
}

func (s *SQLiteMCPServerStore) CreateServer(ctx context.Context, srv *store.MCPServerData) error {
	if err := store.ValidateUserID(srv.CreatedBy); err != nil {
		return err
	}
	// App-layer mutex guard: team_id and project_id cannot both be set.
	if srv.TeamID != nil && srv.ProjectID != nil {
		return fmt.Errorf("mcp server: team_id and project_id cannot both be set (scope mutex)")
	}
	if srv.ID == uuid.Nil {
		srv.ID = store.GenNewID()
	}

	apiKey := srv.APIKey
	if s.encKey != "" && apiKey != "" {
		encrypted, err := crypto.Encrypt(apiKey, s.encKey)
		if err != nil {
			return fmt.Errorf("encrypt api key: %w", err)
		}
		apiKey = encrypted
	}

	now := time.Now().UTC()
	srv.CreatedAt = now
	srv.UpdatedAt = now
	encHeaders := s.encryptJSON(jsonOrEmpty(srv.Headers))
	encEnv := s.encryptJSON(jsonOrEmpty(srv.Env))

	meta := srv.Metadata
	if len(meta) == 0 {
		meta = []byte("{}")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO mcp_servers (id, name, display_name, transport, command, args, url, headers, env,
		 api_key, tool_prefix, timeout_sec, settings, enabled, created_by, metadata,
		 team_id, project_id, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		srv.ID, srv.Name, nilStr(srv.DisplayName), srv.Transport, nilStr(srv.Command),
		jsonOrEmpty(srv.Args), nilStr(srv.URL), encHeaders, encEnv,
		nilStr(apiKey), nilStr(srv.ToolPrefix), srv.TimeoutSec,
		jsonOrEmpty(srv.Settings), srv.Enabled, srv.CreatedBy, meta,
		srv.TeamID, srv.ProjectID, now, now,
	)
	return err
}

func (s *SQLiteMCPServerStore) GetServer(ctx context.Context, id uuid.UUID) (*store.MCPServerData, error) {
	q := `SELECT ` + mcpServerSelectCols + ` FROM mcp_servers WHERE id = ?`
	var row mcpServerRow
	if err := pkgSqlxDB.GetContext(ctx, &row, q, id); err != nil {
		return nil, err
	}
	srv := row.toMCPServerData()
	s.decryptServerFields(&srv)
	return &srv, nil
}

func (s *SQLiteMCPServerStore) GetServerByName(ctx context.Context, name string) (*store.MCPServerData, error) {
	q := `SELECT ` + mcpServerSelectCols + ` FROM mcp_servers WHERE name = ?`
	var row mcpServerRow
	if err := pkgSqlxDB.GetContext(ctx, &row, q, name); err != nil {
		return nil, err
	}
	srv := row.toMCPServerData()
	s.decryptServerFields(&srv)
	return &srv, nil
}

func (s *SQLiteMCPServerStore) ListServers(ctx context.Context) ([]store.MCPServerData, error) {
	q := `SELECT ` + mcpServerSelectCols + ` FROM mcp_servers ORDER BY name`
	var rows []mcpServerRow
	if err := pkgSqlxDB.SelectContext(ctx, &rows, q); err != nil {
		return nil, err
	}
	result := make([]store.MCPServerData, 0, len(rows))
	for _, r := range rows {
		srv := r.toMCPServerData()
		s.decryptServerFields(&srv)
		result = append(result, srv)
	}
	return result, nil
}

func (s *SQLiteMCPServerStore) UpdateServer(ctx context.Context, id uuid.UUID, updates map[string]any) error {
	if key, ok := updates["api_key"]; ok {
		if keyStr, isStr := key.(string); isStr && keyStr != "" && s.encKey != "" {
			encrypted, err := crypto.Encrypt(keyStr, s.encKey)
			if err != nil {
				return fmt.Errorf("encrypt api key: %w", err)
			}
			updates["api_key"] = encrypted
		}
	}
	for _, field := range []string{"env", "headers"} {
		if v, ok := updates[field]; ok {
			var raw []byte
			switch val := v.(type) {
			case json.RawMessage:
				raw = []byte(val)
			default:
				raw, _ = json.Marshal(val)
			}
			if len(raw) > 0 {
				updates[field] = json.RawMessage(s.encryptJSON(raw))
			}
		}
	}
	updates["updated_at"] = time.Now().UTC()
	return execMapUpdate(ctx, s.db, "mcp_servers", id, updates)
}

func (s *SQLiteMCPServerStore) DeleteServer(ctx context.Context, id uuid.UUID) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM mcp_servers WHERE id = ?", id)
	return err
}

// ListAccessibleServers returns servers the agent can reach, filtered by scope context.
// Visibility = global UNION team-scoped (if teamID non-nil) UNION project-scoped
// (if projectID non-nil), intersected with active agent grants.
// Uses ? placeholders; scope columns (team_id, project_id) added in Phase 04 schema.
func (s *SQLiteMCPServerStore) ListAccessibleServers(ctx context.Context, agentID uuid.UUID, teamID, projectID *uuid.UUID) ([]store.MCPServerData, error) {
	var teamStr, projectStr *string
	if teamID != nil {
		v := teamID.String()
		teamStr = &v
	}
	if projectID != nil {
		v := projectID.String()
		projectStr = &v
	}

	var rows []mcpServerRow
	err := pkgSqlxDB.SelectContext(ctx, &rows,
		`SELECT `+mcpServerSelectCols+`
		 FROM mcp_servers
		 WHERE id IN (
		   SELECT s.id FROM mcp_servers s
		   INNER JOIN mcp_agent_grants g ON g.server_id = s.id
		   WHERE g.agent_id = ?
		     AND g.enabled = 1
		     AND s.enabled = 1
		     AND (
		           (s.team_id IS NULL AND s.project_id IS NULL)
		        OR (? IS NOT NULL AND s.team_id    = ?)
		        OR (? IS NOT NULL AND s.project_id = ?)
		     )
		 )
		 ORDER BY name`,
		agentID, teamStr, teamStr, projectStr, projectStr,
	)
	if err != nil {
		return nil, err
	}
	result := make([]store.MCPServerData, 0, len(rows))
	for _, r := range rows {
		srv := r.toMCPServerData()
		s.decryptServerFields(&srv)
		result = append(result, srv)
	}
	return result, nil
}

