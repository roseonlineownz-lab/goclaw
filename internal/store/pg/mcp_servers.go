package pg

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

// PGMCPServerStore implements store.MCPServerStore backed by Postgres.
type PGMCPServerStore struct {
	db     *sql.DB
	encKey string // AES-256 encryption key for API keys
}

func NewPGMCPServerStore(db *sql.DB, encryptionKey string) *PGMCPServerStore {
	return &PGMCPServerStore{db: db, encKey: encryptionKey}
}

// --- Server CRUD ---

func (s *PGMCPServerStore) CreateServer(ctx context.Context, srv *store.MCPServerData) error {
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

	now := time.Now()
	srv.CreatedAt = now
	srv.UpdatedAt = now
	encHeaders := s.encryptJSONB(jsonOrEmpty(srv.Headers))
	encEnv := s.encryptJSONB(jsonOrEmpty(srv.Env))

	meta := srv.Metadata
	if len(meta) == 0 {
		meta = []byte("{}")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO mcp_servers (id, name, display_name, transport, command, args, url, headers, env,
		 api_key, tool_prefix, timeout_sec, settings, enabled, created_by, metadata,
		 team_id, project_id, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)`,
		srv.ID, srv.Name, nilStr(srv.DisplayName), srv.Transport, nilStr(srv.Command),
		jsonOrEmpty(srv.Args), nilStr(srv.URL), encHeaders, encEnv,
		nilStr(apiKey), nilStr(srv.ToolPrefix), srv.TimeoutSec,
		jsonOrEmpty(srv.Settings), srv.Enabled, srv.CreatedBy, meta,
		srv.TeamID, srv.ProjectID, now, now,
	)
	return err
}

const mcpServerSelectCols = `id, name, COALESCE(display_name, '') AS display_name, transport,
		 COALESCE(command, '') AS command, args, COALESCE(url, '') AS url, headers, env,
		 COALESCE(api_key, '') AS api_key, COALESCE(tool_prefix, '') AS tool_prefix,
		 timeout_sec, settings, enabled, created_by, metadata, team_id, project_id, created_at, updated_at`

func (s *PGMCPServerStore) GetServer(ctx context.Context, id uuid.UUID) (*store.MCPServerData, error) {
	var srv store.MCPServerData
	if err := pkgSqlxDB.GetContext(ctx, &srv,
		`SELECT `+mcpServerSelectCols+` FROM mcp_servers WHERE id = $1`, id); err != nil {
		return nil, err
	}
	s.decryptServerFields(&srv)
	return &srv, nil
}

func (s *PGMCPServerStore) GetServerByName(ctx context.Context, name string) (*store.MCPServerData, error) {
	var srv store.MCPServerData
	if err := pkgSqlxDB.GetContext(ctx, &srv,
		`SELECT `+mcpServerSelectCols+` FROM mcp_servers WHERE name = $1`, name); err != nil {
		return nil, err
	}
	s.decryptServerFields(&srv)
	return &srv, nil
}


func (s *PGMCPServerStore) ListServers(ctx context.Context) ([]store.MCPServerData, error) {
	var result []store.MCPServerData
	if err := pkgSqlxDB.SelectContext(ctx, &result,
		`SELECT `+mcpServerSelectCols+` FROM mcp_servers ORDER BY name`); err != nil {
		return nil, err
	}
	for i := range result {
		s.decryptServerFields(&result[i])
	}
	return result, nil
}

// ListAccessibleServers returns servers the agent can reach, intersected with scope context.
// Visibility = global servers UNION team-scoped (if teamID non-nil) UNION project-scoped
// (if projectID non-nil), filtered to servers where the agent has an active grant.
// Admin paths should continue using ListServers (no scope filter, no grant filter).
func (s *PGMCPServerStore) ListAccessibleServers(ctx context.Context, agentID uuid.UUID, teamID, projectID *uuid.UUID) ([]store.MCPServerData, error) {
	// mcpServerSelectCols does not include table alias — use a subquery approach
	// so column names match the struct db tags without s. prefix.
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+mcpServerSelectCols+`
		 FROM mcp_servers
		 WHERE id IN (
		   SELECT s.id FROM mcp_servers s
		   INNER JOIN mcp_agent_grants g ON g.server_id = s.id
		   WHERE g.agent_id = $1
		     AND g.enabled = TRUE
		     AND s.enabled = TRUE
		     AND (
		           (s.team_id IS NULL AND s.project_id IS NULL)
		        OR ($2::uuid IS NOT NULL AND s.team_id    = $2)
		        OR ($3::uuid IS NOT NULL AND s.project_id = $3)
		     )
		 )
		 ORDER BY name`,
		agentID, teamID, projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []store.MCPServerData
	for rows.Next() {
		var srv store.MCPServerData
		var displayName, command, url, apiKey, toolPrefix *string
		var args, headers, env, settings, metadata *[]byte
		if err := rows.Scan(
			&srv.ID, &srv.Name, &displayName, &srv.Transport, &command,
			&args, &url, &headers, &env, &apiKey, &toolPrefix,
			&srv.TimeoutSec, &settings, &srv.Enabled, &srv.CreatedBy,
			&metadata, &srv.TeamID, &srv.ProjectID, &srv.CreatedAt, &srv.UpdatedAt,
		); err != nil {
			continue
		}
		srv.DisplayName = derefStr(displayName)
		srv.Command = derefStr(command)
		srv.URL = derefStr(url)
		srv.ToolPrefix = derefStr(toolPrefix)
		srv.Args = derefBytes(args)
		srv.Settings = derefBytes(settings)
		srv.Metadata = derefBytes(metadata)
		s.decryptServerFields(&srv)
		result = append(result, srv)
	}
	return result, rows.Err()
}

func (s *PGMCPServerStore) UpdateServer(ctx context.Context, id uuid.UUID, updates map[string]any) error {
	// Encrypt api_key if present
	if key, ok := updates["api_key"]; ok {
		if keyStr, isStr := key.(string); isStr && keyStr != "" && s.encKey != "" {
			encrypted, err := crypto.Encrypt(keyStr, s.encKey)
			if err != nil {
				return fmt.Errorf("encrypt api key: %w", err)
			}
			updates["api_key"] = encrypted
		}
	}
	// Encrypt env/headers JSONB fields.
	// json.Decoder into map[string]interface{} produces map[string]interface{}
	// for nested objects, not json.RawMessage — so we must marshal any type.
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
				updates[field] = json.RawMessage(s.encryptJSONB(raw))
			}
		}
	}
	updates["updated_at"] = time.Now()
	return execMapUpdate(ctx, s.db, "mcp_servers", id, updates)
}

func (s *PGMCPServerStore) DeleteServer(ctx context.Context, id uuid.UUID) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM mcp_servers WHERE id = $1", id)
	return err
}

