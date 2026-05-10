//go:build sqlite || sqliteonly

package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// SQLiteAgentStore implements store.AgentStore backed by SQLite.
type SQLiteAgentStore struct {
	db *sql.DB
	// embProvider is intentionally omitted — vector search not supported in SQLite lite edition.
}

func NewSQLiteAgentStore(db *sql.DB) *SQLiteAgentStore {
	return &SQLiteAgentStore{db: db}
}

// SetEmbeddingProvider is a no-op for SQLite — vector not supported.
func (s *SQLiteAgentStore) SetEmbeddingProvider(_ store.EmbeddingProvider) {}

// agentSelectCols is the column list for all agent SELECT queries.
const agentSelectCols = `id, agent_key, display_name, frontmatter, owner_id, owner_user_id, provider, model,
	 context_window, max_tool_iterations, workspace, restrict_to_workspace,
	 tools_config, sandbox_config, subagents_config, memory_config,
	 compaction_config, context_pruning, other_config,
	 emoji, agent_description, thinking_level, max_tokens,
	 self_evolve, skill_evolve, skill_nudge_interval,
	 reasoning_config, share_workspace, share_memory, chatgpt_oauth_routing,
	 shell_deny_groups, kg_dedup_config,
	 is_default, status, budget_monthly_cents, metadata, created_at, updated_at`

// agentOwnerFilter mirrors the PG version: privileged roles (owner/root/admin)
// see all agents; everyone else is scoped to their own owner_user_id. Missing or
// non-UUID UserID surfaces ErrNotFound so non-admin callers fail closed.
func agentOwnerFilter(ctx context.Context) (clause string, args []any, err error) {
	if isAgentAdminScope(ctx) {
		return "", nil, nil
	}
	uidStr := store.UserIDFromContext(ctx)
	if uidStr == "" {
		return "", nil, store.ErrNotFound
	}
	uid, parseErr := uuid.Parse(uidStr)
	if parseErr != nil {
		return "", nil, store.ErrNotFound
	}
	return " AND owner_user_id = ?", []any{uid.String()}, nil
}

func isAgentAdminScope(ctx context.Context) bool {
	switch store.RoleFromContext(ctx) {
	case "owner", "root", "admin":
		return true
	default:
		return false
	}
}

func (s *SQLiteAgentStore) Create(ctx context.Context, agent *store.AgentData) error {
	if agent.ID == uuid.Nil {
		agent.ID = store.GenNewID()
	}
	now := time.Now()
	agent.CreatedAt = now
	agent.UpdatedAt = now

	var ownerUserID any
	if agent.OwnerUserID != nil && *agent.OwnerUserID != uuid.Nil {
		ownerUserID = agent.OwnerUserID.String()
	}

	meta := agent.Metadata
	if len(meta) == 0 {
		meta = []byte("{}")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agents (id, agent_key, display_name, frontmatter, owner_id, owner_user_id, provider, model,
		 context_window, max_tool_iterations, workspace, restrict_to_workspace,
		 tools_config, sandbox_config, subagents_config, memory_config,
		 compaction_config, context_pruning, other_config,
		 emoji, agent_description, thinking_level, max_tokens,
		 self_evolve, skill_evolve, skill_nudge_interval,
		 reasoning_config, share_workspace, share_memory, chatgpt_oauth_routing,
		 shell_deny_groups, kg_dedup_config,
		 is_default, status, budget_monthly_cents, metadata, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		agent.ID, agent.AgentKey,
		agent.DisplayName,
		sql.NullString{String: agent.Frontmatter, Valid: agent.Frontmatter != ""},
		agent.OwnerID, ownerUserID, agent.Provider, agent.Model,
		agent.ContextWindow, agent.MaxToolIterations, agent.Workspace, agent.RestrictToWorkspace,
		jsonOrEmpty(agent.ToolsConfig), jsonOrNull(agent.SandboxConfig), jsonOrNull(agent.SubagentsConfig), jsonOrNull(agent.MemoryConfig),
		jsonOrNull(agent.CompactionConfig), jsonOrNull(agent.ContextPruning), jsonOrEmpty(agent.OtherConfig),
		agent.Emoji, agent.AgentDescription, agent.ThinkingLevel, agent.MaxTokens,
		agent.SelfEvolve, agent.SkillEvolve, agent.SkillNudgeInterval,
		jsonOrEmpty(agent.ReasoningConfig), agent.ShareWorkspace, agent.ShareMemory, jsonOrEmpty(agent.ChatGPTOAuthRouting),
		jsonOrEmpty(agent.ShellDenyGroups), jsonOrEmpty(agent.KGDedupConfig),
		agent.IsDefault, agent.Status, agent.BudgetMonthlyCents,
		meta, now, now,
	)
	return err
}

func (s *SQLiteAgentStore) GetByKey(ctx context.Context, agentKey string) (*store.AgentData, error) {
	clause, args, err := agentOwnerFilter(ctx)
	if err != nil {
		return nil, err
	}
	q := `SELECT ` + agentSelectCols + ` FROM agents WHERE agent_key = ? AND deleted_at IS NULL` + clause
	row := s.db.QueryRowContext(ctx, q, append([]any{agentKey}, args...)...)
	d, err := scanAgentRow(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	return d, nil
}

func (s *SQLiteAgentStore) GetByID(ctx context.Context, id uuid.UUID) (*store.AgentData, error) {
	clause, args, err := agentOwnerFilter(ctx)
	if err != nil {
		return nil, err
	}
	q := `SELECT ` + agentSelectCols + ` FROM agents WHERE id = ? AND deleted_at IS NULL` + clause
	row := s.db.QueryRowContext(ctx, q, append([]any{id}, args...)...)
	d, err := scanAgentRow(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	return d, nil
}

func (s *SQLiteAgentStore) GetByIDUnscoped(ctx context.Context, id uuid.UUID) (*store.AgentData, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+agentSelectCols+` FROM agents WHERE id = ? AND deleted_at IS NULL`, id)
	d, err := scanAgentRow(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	return d, nil
}

func (s *SQLiteAgentStore) Update(ctx context.Context, id uuid.UUID, updates map[string]any) error {
	if len(updates) == 0 {
		return nil
	}

	// Immutability: agent_key is set at creation and must never change.
	// Mirrors PGAgentStore.Update + PGTeamStore.UpdateTeam pattern.
	delete(updates, "agent_key")
	if len(updates) == 0 {
		return nil
	}

	for _, col := range []string{"emoji", "agent_description", "thinking_level"} {
		if v, ok := updates[col]; ok && v == nil {
			updates[col] = ""
		}
	}
	for _, col := range []string{"skill_nudge_interval", "max_tokens", "self_evolve", "skill_evolve", "is_default", "share_workspace", "share_memory"} {
		if v, ok := updates[col]; ok && v == nil {
			if col == "self_evolve" || col == "skill_evolve" || col == "is_default" || col == "share_workspace" || col == "share_memory" {
				updates[col] = false
			} else {
				updates[col] = 0
			}
		}
	}
	for _, col := range []string{"other_config", "tools_config", "reasoning_config", "chatgpt_oauth_routing", "shell_deny_groups", "kg_dedup_config"} {
		if v, ok := updates[col]; ok && v == nil {
			updates[col] = []byte("{}")
		}
	}

	// Unset existing default before setting a new one. Privileged callers clear
	// globally; non-admin callers only clear within their owner_user_id scope.
	if v, ok := updates["is_default"]; ok {
		if isDefault, _ := v.(bool); isDefault {
			clause, args, ferr := agentOwnerFilter(ctx)
			if ferr == nil {
				q := "UPDATE agents SET is_default = 0 WHERE is_default = 1 AND id != ? AND deleted_at IS NULL" + clause
				if _, err := s.db.ExecContext(ctx, q, append([]any{id}, args...)...); err != nil {
					slog.Warn("agents.unset_default", "error", err)
				}
			}
		}
	}

	updates["updated_at"] = time.Now()
	clause, args, err := agentOwnerFilter(ctx)
	if err != nil {
		return err
	}
	if clause == "" {
		return execMapUpdate(ctx, s.db, "agents", id, updates)
	}
	return execMapUpdateWhereOwner(ctx, s.db, "agents", updates, id, args[0])
}

func (s *SQLiteAgentStore) Delete(ctx context.Context, id uuid.UUID) error {
	clause, args, err := agentOwnerFilter(ctx)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "DELETE FROM agents WHERE id = ?"+clause, append([]any{id}, args...)...)
	return err
}

func (s *SQLiteAgentStore) List(ctx context.Context, ownerID string) ([]store.AgentData, error) {
	q := `SELECT ` + agentSelectCols + ` FROM agents WHERE deleted_at IS NULL`
	var args []any

	if ownerID != "" {
		q += " AND owner_id = ?"
		args = append(args, ownerID)
	}

	clause, ownArgs, err := agentOwnerFilter(ctx)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return []store.AgentData{}, nil
		}
		return nil, err
	}
	if clause != "" {
		q += clause
		args = append(args, ownArgs...)
	}

	q += " ORDER BY created_at DESC"
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAgentRows(rows)
}

func (s *SQLiteAgentStore) GetDefault(ctx context.Context) (*store.AgentData, error) {
	clause, args, err := agentOwnerFilter(ctx)
	if err != nil {
		return nil, err
	}
	q := `SELECT ` + agentSelectCols + ` FROM agents WHERE deleted_at IS NULL` + clause +
		` ORDER BY is_default DESC, created_at ASC LIMIT 1`
	row := s.db.QueryRowContext(ctx, q, args...)
	d, err := scanAgentRow(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	return d, nil
}

// execMapUpdateWhereOwner runs UPDATE ... WHERE id = ? AND owner_user_id = ?.
// Used by Update() for non-admin callers so a foreign agent's row is left untouched.
func execMapUpdateWhereOwner(ctx context.Context, db *sql.DB, table string, updates map[string]any, id uuid.UUID, ownerUserID any) error {
	if len(updates) == 0 {
		return nil
	}
	var setClauses []string
	args := make([]any, 0, len(updates)+2)
	for col, val := range updates {
		if !validColumnName.MatchString(col) {
			slog.Warn("security.invalid_column_name", "table", table, "column", col)
			return fmt.Errorf("invalid column name: %q", col)
		}
		setClauses = append(setClauses, col+" = ?")
		args = append(args, val)
	}
	args = append(args, id, ownerUserID)
	q := fmt.Sprintf("UPDATE %s SET %s WHERE id = ? AND owner_user_id = ?",
		table, strings.Join(setClauses, ", "))
	_, err := db.ExecContext(ctx, q, args...)
	return err
}

// --- Scan helpers ---

type agentRowScanner interface {
	Scan(dest ...any) error
}

func scanAgentRow(row agentRowScanner) (*store.AgentData, error) {
	var d store.AgentData
	var frontmatter sql.NullString
	var ownerUserID sql.NullString
	var toolsCfg, sandboxCfg, subagentsCfg, memoryCfg, compactionCfg, pruningCfg, otherCfg *[]byte
	var reasoningCfg, oauthCfg, shellCfg, kgCfg *[]byte
	var metaCfg *[]byte
	createdAt, updatedAt := scanTimePair()
	err := row.Scan(
		&d.ID, &d.AgentKey, &d.DisplayName, &frontmatter, &d.OwnerID, &ownerUserID, &d.Provider, &d.Model,
		&d.ContextWindow, &d.MaxToolIterations, &d.Workspace, &d.RestrictToWorkspace,
		&toolsCfg, &sandboxCfg, &subagentsCfg, &memoryCfg, &compactionCfg, &pruningCfg, &otherCfg,
		&d.Emoji, &d.AgentDescription, &d.ThinkingLevel, &d.MaxTokens,
		&d.SelfEvolve, &d.SkillEvolve, &d.SkillNudgeInterval,
		&reasoningCfg, &d.ShareWorkspace, &d.ShareMemory, &oauthCfg, &shellCfg, &kgCfg,
		&d.IsDefault, &d.Status, &d.BudgetMonthlyCents,
		&metaCfg, createdAt, updatedAt,
	)
	if err != nil {
		return nil, err
	}
	d.CreatedAt = createdAt.Time
	d.UpdatedAt = updatedAt.Time
	if frontmatter.Valid {
		d.Frontmatter = frontmatter.String
	}
	if ownerUserID.Valid {
		if u, perr := uuid.Parse(ownerUserID.String); perr == nil {
			d.OwnerUserID = &u
		}
	}
	if toolsCfg != nil {
		d.ToolsConfig = *toolsCfg
	}
	if sandboxCfg != nil {
		d.SandboxConfig = *sandboxCfg
	}
	if subagentsCfg != nil {
		d.SubagentsConfig = *subagentsCfg
	}
	if memoryCfg != nil {
		d.MemoryConfig = *memoryCfg
	}
	if compactionCfg != nil {
		d.CompactionConfig = *compactionCfg
	}
	if pruningCfg != nil {
		d.ContextPruning = *pruningCfg
	}
	if otherCfg != nil {
		d.OtherConfig = *otherCfg
	}
	if reasoningCfg != nil {
		d.ReasoningConfig = *reasoningCfg
	}
	if oauthCfg != nil {
		d.ChatGPTOAuthRouting = *oauthCfg
	}
	if shellCfg != nil {
		d.ShellDenyGroups = *shellCfg
	}
	if kgCfg != nil {
		d.KGDedupConfig = *kgCfg
	}
	if metaCfg != nil {
		d.Metadata = *metaCfg
	}
	return &d, nil
}

func scanAgentRows(rows *sql.Rows) ([]store.AgentData, error) {
	var result []store.AgentData
	for rows.Next() {
		d, err := scanAgentRow(rows)
		if err != nil {
			continue
		}
		result = append(result, *d)
	}
	return result, rows.Err()
}

// ResetStuckSummoning flips rows with status='summoning' to 'summon_failed'.
// Called at startup to recover from crashes where summon goroutines died mid-flight.
func (s *SQLiteAgentStore) ResetStuckSummoning(ctx context.Context) (int64, error) {
	const q = `UPDATE agents SET status = ? WHERE status = ?`
	res, err := s.db.ExecContext(ctx, q, store.AgentStatusSummonFailed, store.AgentStatusSummoning)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}
