package pg

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// --- Agent-level Context Files ---

func (s *PGAgentStore) GetAgentContextFiles(ctx context.Context, agentID uuid.UUID) ([]store.AgentContextFileData, error) {
	var result []store.AgentContextFileData
	if err := pkgSqlxDB.SelectContext(ctx, &result,
		"SELECT agent_id, file_name, content FROM agent_context_files WHERE agent_id = $1 ORDER BY file_name",
		agentID,
	); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *PGAgentStore) SetAgentContextFile(ctx context.Context, agentID uuid.UUID, fileName, content string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_context_files (id, agent_id, file_name, content, updated_at)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (agent_id, file_name) DO UPDATE SET content = EXCLUDED.content, updated_at = EXCLUDED.updated_at`,
		store.GenNewID(), agentID, fileName, content, time.Now(),
	)
	return err
}

// PropagateContextFile copies an agent-level context file to all existing user
// instances that already have that file (seeded users). Returns updated row count.
func (s *PGAgentStore) PropagateContextFile(ctx context.Context, agentID uuid.UUID, fileName string) (int, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE user_context_files
		 SET content = src.content, updated_at = $3
		 FROM (
		     SELECT content FROM agent_context_files
		     WHERE agent_id = $1 AND file_name = $2
		 ) src
		 WHERE user_context_files.agent_id = $1
		   AND user_context_files.file_name = $2`,
		agentID, fileName, time.Now(),
	)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// --- Per-user Context Files ---

func (s *PGAgentStore) GetUserContextFiles(ctx context.Context, agentID uuid.UUID, userID string) ([]store.UserContextFileData, error) {
	var result []store.UserContextFileData
	if err := pkgSqlxDB.SelectContext(ctx, &result,
		"SELECT agent_id, user_id, file_name, content FROM user_context_files WHERE agent_id = $1 AND user_id = $2 ORDER BY file_name",
		agentID, userID,
	); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *PGAgentStore) SetUserContextFile(ctx context.Context, agentID uuid.UUID, userID, fileName, content string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO user_context_files (id, agent_id, user_id, file_name, content, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (agent_id, user_id, file_name) DO UPDATE SET content = EXCLUDED.content, updated_at = EXCLUDED.updated_at`,
		store.GenNewID(), agentID, userID, fileName, content, time.Now(),
	)
	return err
}

func (s *PGAgentStore) ListUserContextFilesByName(ctx context.Context, agentID uuid.UUID, fileName string) ([]store.UserContextFileData, error) {
	var result []store.UserContextFileData
	if err := pkgSqlxDB.SelectContext(ctx, &result,
		"SELECT agent_id, user_id, file_name, content FROM user_context_files WHERE agent_id = $1 AND file_name = $2",
		agentID, fileName,
	); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *PGAgentStore) DeleteUserContextFile(ctx context.Context, agentID uuid.UUID, userID, fileName string) error {
	_, err := s.db.ExecContext(ctx,
		"DELETE FROM user_context_files WHERE agent_id = $1 AND user_id = $2 AND file_name = $3",
		agentID, userID, fileName)
	return err
}

func (s *PGAgentStore) MigrateUserDataOnMerge(ctx context.Context, oldUserIDs []string, newUserID string) error {
	if len(oldUserIDs) == 0 {
		return nil
	}

	placeholders := make([]string, len(oldUserIDs))
	baseArgs := make([]any, 0, len(oldUserIDs)+1)
	for i, id := range oldUserIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		baseArgs = append(baseArgs, id)
	}
	inClause := strings.Join(placeholders, ",")
	newP := fmt.Sprintf("$%d", len(oldUserIDs)+1)
	baseArgs = append(baseArgs, newUserID)

	// Args for delete (no newUserID param needed).
	delArgs := append([]any{}, baseArgs[:len(oldUserIDs)]...)

	// Helper: migrate + delete for one table. DO NOTHING on conflict —
	// existing user data always wins (canonical identity).
	migrate := func(insertQ, deleteQ string) {
		if _, err := s.db.ExecContext(ctx, insertQ, baseArgs...); err != nil {
			slog.Warn("merge.migrate", "error", err)
		}
		if _, err := s.db.ExecContext(ctx, deleteQ, delArgs...); err != nil {
			slog.Warn("merge.cleanup", "error", err)
		}
	}

	// 1. user_context_files: UNIQUE(agent_id, user_id, file_name)
	migrate(
		fmt.Sprintf(`INSERT INTO user_context_files (id, agent_id, user_id, file_name, content, updated_at)
			SELECT gen_random_uuid(), agent_id, %s, file_name, content, updated_at
			FROM user_context_files WHERE user_id IN (%s)
			ON CONFLICT (agent_id, user_id, file_name) DO NOTHING`, newP, inClause),
		fmt.Sprintf(`DELETE FROM user_context_files WHERE user_id IN (%s)`, inClause),
	)

	// 2. user_agent_overrides: UNIQUE(agent_id, user_id)
	migrate(
		fmt.Sprintf(`INSERT INTO user_agent_overrides (id, agent_id, user_id, provider, model, settings, created_at, updated_at)
			SELECT gen_random_uuid(), agent_id, %s, provider, model, settings, created_at, updated_at
			FROM user_agent_overrides WHERE user_id IN (%s)
			ON CONFLICT (agent_id, user_id) DO NOTHING`, newP, inClause),
		fmt.Sprintf(`DELETE FROM user_agent_overrides WHERE user_id IN (%s)`, inClause),
	)

	// 3. user_agent_profiles: PK(agent_id, user_id)
	migrate(
		fmt.Sprintf(`INSERT INTO user_agent_profiles (agent_id, user_id, workspace, first_seen_at, last_seen_at, metadata)
			SELECT agent_id, %s, workspace, first_seen_at, last_seen_at, metadata
			FROM user_agent_profiles WHERE user_id IN (%s)
			ON CONFLICT (agent_id, user_id) DO NOTHING`, newP, inClause),
		fmt.Sprintf(`DELETE FROM user_agent_profiles WHERE user_id IN (%s)`, inClause),
	)

	// 4. memory_documents: UNIQUE(agent_id, COALESCE(user_id,''), path)
	migrate(
		fmt.Sprintf(`INSERT INTO memory_documents (id, agent_id, user_id, path, content, hash, updated_at, created_at)
			SELECT gen_random_uuid(), agent_id, %s, path, content, hash, updated_at, created_at
			FROM memory_documents WHERE user_id IN (%s)
			ON CONFLICT (agent_id, COALESCE(user_id,''), path) DO NOTHING`, newP, inClause),
		fmt.Sprintf(`DELETE FROM memory_documents WHERE user_id IN (%s)`, inClause),
	)

	// 5. memory_chunks: FK on document_id — cascade from memory_documents delete handles this.
	// But orphan chunks (where document was already migrated) need cleanup.
	// Simply re-point remaining chunks whose document still has old user_id.
	repoint := fmt.Sprintf(`UPDATE memory_chunks SET user_id = %s WHERE user_id IN (%s)`, newP, inClause)
	if _, err := s.db.ExecContext(ctx, repoint, baseArgs...); err != nil {
		slog.Warn("merge.migrate_chunks", "error", err)
	}

	return nil
}

// --- User-Agent Profiles ---

func (s *PGAgentStore) GetOrCreateUserProfile(ctx context.Context, agentID uuid.UUID, userID, workspace, channel string) (bool, string, error) {
	// Build workspace with channel segment for isolation.
	// Store in portable ~ form (e.g. "~/.goclaw/agent-ws/telegram").
	effectiveWs := config.ContractHome(workspace)
	if channel != "" {
		effectiveWs = filepath.Join(effectiveWs, channel)
	}

	var isInserted bool
	var storedWorkspace sql.NullString
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO user_agent_profiles (agent_id, user_id, workspace, first_seen_at, last_seen_at)
		VALUES ($1, $2, NULLIF($3, ''), NOW(), NOW())
		ON CONFLICT (agent_id, user_id) DO UPDATE SET last_seen_at = NOW()
		RETURNING (xmax = 0), workspace
	`, agentID, userID, effectiveWs).Scan(&isInserted, &storedWorkspace)
	if err != nil {
		return false, effectiveWs, err
	}
	ws := effectiveWs
	if storedWorkspace.Valid && storedWorkspace.String != "" {
		ws = storedWorkspace.String
	}
	return isInserted, ws, nil
}

// EnsureUserProfile creates a minimal user_agent_profiles row if not exists.
// Used when admin manually adds a contact as an agent instance via the UI.
func (s *PGAgentStore) EnsureUserProfile(ctx context.Context, agentID uuid.UUID, userID string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO user_agent_profiles (agent_id, user_id, first_seen_at, last_seen_at)
		VALUES ($1, $2, NOW(), NOW())
		ON CONFLICT (agent_id, user_id) DO NOTHING
	`, agentID, userID)
	return err
}

// --- User Instances ---

func (s *PGAgentStore) ListUserInstances(ctx context.Context, agentID uuid.UUID) ([]store.UserInstanceData, error) {
	var rows []userInstanceRow
	if err := pkgSqlxDB.SelectContext(ctx, &rows, `
		SELECT p.user_id,
		       TO_CHAR(p.first_seen_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"') AS first_seen_at,
		       TO_CHAR(p.last_seen_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"') AS last_seen_at,
		       COALESCE(fc.cnt, 0) AS file_count,
		       COALESCE(p.metadata, '{}') AS metadata
		FROM user_agent_profiles p
		LEFT JOIN (
		    SELECT user_id, COUNT(*) AS cnt
		    FROM user_context_files
		    WHERE agent_id = $1
		    GROUP BY user_id
		) fc ON fc.user_id = p.user_id
		WHERE p.agent_id = $1
		ORDER BY p.last_seen_at DESC
	`, agentID); err != nil {
		return nil, err
	}
	result := make([]store.UserInstanceData, len(rows))
	for i, r := range rows {
		result[i] = r.toUserInstanceData()
	}
	return result, nil
}

func (s *PGAgentStore) UpdateUserProfileMetadata(ctx context.Context, agentID uuid.UUID, userID string, metadata map[string]string) error {
	metaJSON, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE user_agent_profiles SET metadata = COALESCE(metadata, '{}') || $3::jsonb
		 WHERE agent_id = $1 AND user_id = $2`,
		agentID, userID, metaJSON,
	)
	return err
}

// --- User Overrides ---

func (s *PGAgentStore) GetUserOverride(ctx context.Context, agentID uuid.UUID, userID string) (*store.UserAgentOverrideData, error) {
	var d store.UserAgentOverrideData
	err := pkgSqlxDB.GetContext(ctx, &d,
		"SELECT agent_id, user_id, provider, model FROM user_agent_overrides WHERE agent_id = $1 AND user_id = $2",
		agentID, userID,
	)
	if err != nil {
		return nil, nil // not found = no override
	}
	return &d, nil
}

func (s *PGAgentStore) SetUserOverride(ctx context.Context, override *store.UserAgentOverrideData) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO user_agent_overrides (id, agent_id, user_id, provider, model)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (agent_id, user_id) DO UPDATE SET provider = EXCLUDED.provider, model = EXCLUDED.model`,
		store.GenNewID(), override.AgentID, override.UserID, override.Provider, override.Model,
	)
	return err
}
