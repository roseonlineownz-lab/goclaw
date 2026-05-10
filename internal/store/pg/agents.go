package pg

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

// PGAgentStore implements store.AgentStore backed by Postgres.
type PGAgentStore struct {
	db          *sql.DB
	embProvider store.EmbeddingProvider // optional: for agent frontmatter embeddings
}

func NewPGAgentStore(db *sql.DB) *PGAgentStore {
	return &PGAgentStore{db: db}
}

// SetEmbeddingProvider sets the embedding provider for agent frontmatter vectors.
func (s *PGAgentStore) SetEmbeddingProvider(provider store.EmbeddingProvider) {
	s.embProvider = provider
}

// generateAgentEmbedding creates an embedding for an agent's displayName+frontmatter and stores it.
func (s *PGAgentStore) generateAgentEmbedding(ctx context.Context, agentID uuid.UUID, displayName, frontmatter string) {
	if s.embProvider == nil || frontmatter == "" {
		return
	}
	text := displayName
	if frontmatter != "" {
		text += ": " + frontmatter
	}
	embeddings, err := s.embProvider.Embed(ctx, []string{text})
	if err != nil || len(embeddings) == 0 || len(embeddings[0]) == 0 {
		slog.Warn("agent embedding generation failed", "agent", agentID, "error", err)
		return
	}
	vecStr := vectorToString(embeddings[0])
	if _, err := s.db.ExecContext(ctx, `UPDATE agents SET embedding = $1::halfvec WHERE id = $2`, vecStr, agentID); err != nil {
		slog.Warn("agent embedding update failed", "agent", agentID, "error", err)
	}
}

// BackfillAgentEmbeddings generates embeddings for all active agents that have frontmatter but no embedding.
func (s *PGAgentStore) BackfillAgentEmbeddings(ctx context.Context) (int, error) {
	if s.embProvider == nil {
		return 0, nil
	}
	var pending []agentBackfillRow
	if err := pkgSqlxDB.SelectContext(ctx, &pending,
		`SELECT id, COALESCE(display_name, '') AS display_name, COALESCE(frontmatter, '') AS frontmatter
		 FROM agents WHERE deleted_at IS NULL AND frontmatter IS NOT NULL AND frontmatter != '' AND embedding IS NULL`,
	); err != nil {
		return 0, err
	}
	if len(pending) == 0 {
		return 0, nil
	}

	slog.Info("backfilling agent embeddings", "count", len(pending))
	updated := 0
	for _, ag := range pending {
		text := ag.DisplayName
		if ag.Frontmatter != "" {
			text += ": " + ag.Frontmatter
		}
		embeddings, err := s.embProvider.Embed(ctx, []string{text})
		if err != nil || len(embeddings) == 0 || len(embeddings[0]) == 0 {
			continue
		}
		vecStr := vectorToString(embeddings[0])
		if _, err := s.db.ExecContext(ctx, `UPDATE agents SET embedding = $1::halfvec WHERE id = $2`, vecStr, ag.ID); err != nil {
			continue
		}
		updated++
	}
	slog.Info("agent embeddings backfill complete", "updated", updated)
	return updated, nil
}

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

func (s *PGAgentStore) Create(ctx context.Context, agent *store.AgentData) error {
	if agent.ID == uuid.Nil {
		agent.ID = store.GenNewID()
	}
	now := time.Now()
	agent.CreatedAt = now
	agent.UpdatedAt = now
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
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,
		         $19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33,$34,$35,$36,$37,$38)`,
		agent.ID, agent.AgentKey, agent.DisplayName, sql.NullString{String: agent.Frontmatter, Valid: agent.Frontmatter != ""},
		agent.OwnerID, nilUUID(agent.OwnerUserID), agent.Provider, agent.Model,
		agent.ContextWindow, agent.MaxToolIterations, agent.Workspace, agent.RestrictToWorkspace,
		jsonOrEmpty(agent.ToolsConfig), jsonOrNull(agent.SandboxConfig), jsonOrNull(agent.SubagentsConfig), jsonOrNull(agent.MemoryConfig),
		jsonOrNull(agent.CompactionConfig), jsonOrNull(agent.ContextPruning), jsonOrEmpty(agent.OtherConfig),
		agent.Emoji, agent.AgentDescription, agent.ThinkingLevel, agent.MaxTokens,
		agent.SelfEvolve, agent.SkillEvolve, agent.SkillNudgeInterval,
		jsonOrEmpty(agent.ReasoningConfig), agent.ShareWorkspace, agent.ShareMemory, jsonOrEmpty(agent.ChatGPTOAuthRouting),
		jsonOrEmpty(agent.ShellDenyGroups), jsonOrEmpty(agent.KGDedupConfig),
		agent.IsDefault, agent.Status, agent.BudgetMonthlyCents, jsonOrEmpty(agent.Metadata), now, now,
	)
	if err != nil {
		return err
	}

	// Generate embedding for new agent with frontmatter
	if agent.Frontmatter != "" && s.embProvider != nil {
		go s.generateAgentEmbedding(context.Background(), agent.ID, agent.DisplayName, agent.Frontmatter)
	}
	return nil
}

// agentOwnerFilter returns the owner_user_id WHERE-clause fragment for the caller.
// Privileged roles (owner/root/admin) bypass scoping. Otherwise the caller's UUID
// from UserIDFromContext is required; missing/invalid UUID surfaces ErrNotFound so
// queries fail closed instead of leaking unowned agents.
func agentOwnerFilter(ctx context.Context, startIdx int) (clause string, args []any, err error) {
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
	return fmt.Sprintf(" AND owner_user_id = $%d", startIdx), []any{uid}, nil
}

// isAgentAdminScope reports whether the caller's role bypasses owner_user_id scoping.
func isAgentAdminScope(ctx context.Context) bool {
	switch store.RoleFromContext(ctx) {
	case "owner", "root", "admin":
		return true
	default:
		return false
	}
}

func (s *PGAgentStore) GetByKey(ctx context.Context, agentKey string) (*store.AgentData, error) {
	clause, args, err := agentOwnerFilter(ctx, 2)
	if err != nil {
		return nil, err
	}
	q := `SELECT ` + agentSelectCols + ` FROM agents WHERE agent_key = $1 AND deleted_at IS NULL` + clause
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

func (s *PGAgentStore) GetByID(ctx context.Context, id uuid.UUID) (*store.AgentData, error) {
	clause, args, err := agentOwnerFilter(ctx, 2)
	if err != nil {
		return nil, err
	}
	q := `SELECT ` + agentSelectCols + ` FROM agents WHERE id = $1 AND deleted_at IS NULL` + clause
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

func (s *PGAgentStore) Update(ctx context.Context, id uuid.UUID, updates map[string]any) error {
	if len(updates) == 0 {
		return nil
	}

	// Immutability: agent_key is set at creation and must never change.
	// The slug couples to the FS workspace path and the router cache; renames
	// would orphan workspace dirs and break cache invalidation. Mirrors the
	// team_key strip in PGTeamStore.UpdateTeam.
	delete(updates, "agent_key")
	if len(updates) == 0 {
		return nil
	}

	// Coerce NOT NULL columns: null → default to prevent constraint violations.
	// Promoted TEXT columns (migration 000037): null → empty string.
	for _, col := range []string{"emoji", "agent_description", "thinking_level"} {
		if v, ok := updates[col]; ok && v == nil {
			updates[col] = ""
		}
	}
	// Promoted INT/BOOL columns: null → 0/false.
	for _, col := range []string{"skill_nudge_interval", "max_tokens", "self_evolve", "skill_evolve", "is_default", "share_workspace", "share_memory"} {
		if v, ok := updates[col]; ok && v == nil {
			if col == "self_evolve" || col == "skill_evolve" || col == "is_default" || col == "share_workspace" || col == "share_memory" {
				updates[col] = false
			} else {
				updates[col] = 0
			}
		}
	}
	// NOT NULL JSONB columns: null → empty object.
	for _, col := range []string{"other_config", "tools_config", "chatgpt_oauth_routing", "reasoning_config", "shell_deny_groups", "kg_dedup_config"} {
		if v, ok := updates[col]; ok && v == nil {
			updates[col] = []byte("{}")
		}
	}

	// If setting this agent as default, unset existing default first.
	// Privileged callers clear globally; regular owners only flip rows they own.
	if v, ok := updates["is_default"]; ok {
		if isDefault, _ := v.(bool); isDefault {
			clause, args, ferr := agentOwnerFilter(ctx, 2)
			if ferr == nil {
				q := "UPDATE agents SET is_default = false WHERE is_default = true AND id != $1 AND deleted_at IS NULL" + clause
				if _, err := s.db.ExecContext(ctx, q, append([]any{id}, args...)...); err != nil {
					slog.Warn("agents.unset_default", "error", err)
				}
			}
		}
	}

	updates["updated_at"] = time.Now()
	clause, args, err := agentOwnerFilter(ctx, 0) // index 0 means "fill in later"
	if err != nil {
		return err
	}
	if clause == "" {
		if err := execMapUpdateWhere(ctx, s.db, "agents", updates, "id = $IDX AND deleted_at IS NULL", id); err != nil {
			return err
		}
	} else {
		if err := execMapUpdateWhereOwner(ctx, s.db, "agents", updates, id, args[0].(uuid.UUID)); err != nil {
			return err
		}
	}

	// Regenerate embedding when frontmatter changes
	if _, hasFrontmatter := updates["frontmatter"]; hasFrontmatter && s.embProvider != nil {
		go func() {
			ag, agErr := s.GetByIDUnscoped(context.Background(), id)
			if agErr == nil {
				s.generateAgentEmbedding(context.Background(), id, ag.DisplayName, ag.Frontmatter)
			}
		}()
	}
	return nil
}

func (s *PGAgentStore) Delete(ctx context.Context, id uuid.UUID) error {
	clause, args, err := agentOwnerFilter(ctx, 2)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "DELETE FROM agents WHERE id = $1"+clause, append([]any{id}, args...)...)
	return err
}

func (s *PGAgentStore) List(ctx context.Context, ownerID string) ([]store.AgentData, error) {
	q := `SELECT ` + agentSelectCols + ` FROM agents WHERE deleted_at IS NULL`
	var args []any
	argIdx := 1

	if ownerID != "" {
		q += fmt.Sprintf(" AND owner_id = $%d", argIdx)
		args = append(args, ownerID)
		argIdx++
	}

	clause, ownArgs, err := agentOwnerFilter(ctx, argIdx)
	if err != nil {
		// Fail closed: caller has no scoping identity → empty result rather than ErrNotFound (List semantics).
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

func (s *PGAgentStore) GetDefault(ctx context.Context) (*store.AgentData, error) {
	clause, args, err := agentOwnerFilter(ctx, 1)
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

// --- Access Control ---

// CreateShare inserts a single explicit grant row (target = user XOR team).
// Caller validates role + target_mutex at the handler layer; DB enforces
// both as a defence-in-depth.
func (s *PGAgentStore) CreateShare(ctx context.Context, in store.AgentShareInput) error {
	if !store.ValidShareRole(in.Role) {
		return store.ErrInvalidShareRole
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_shares
			(id, agent_id, shared_with_user_id, shared_with_team_id, role, created_by, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, NOW(), NOW())`,
		store.GenNewID(), in.AgentID, in.SharedWithUserID, in.SharedWithTeamID, in.Role, in.CreatedBy,
	)
	return err
}

func (s *PGAgentStore) RevokeShareByUser(ctx context.Context, agentID, userID uuid.UUID) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM agent_shares WHERE agent_id = $1 AND shared_with_user_id = $2`,
		agentID, userID)
	return err
}

func (s *PGAgentStore) RevokeShareByTeam(ctx context.Context, agentID, teamID uuid.UUID) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM agent_shares WHERE agent_id = $1 AND shared_with_team_id = $2`,
		agentID, teamID)
	return err
}

func (s *PGAgentStore) ListShares(ctx context.Context, agentID uuid.UUID) ([]store.AgentShareData, error) {
	const q = `SELECT id, agent_id, shared_with_user_id, shared_with_team_id, role, metadata, created_by,
	                  created_at, updated_at
		         FROM agent_shares WHERE agent_id = $1 ORDER BY created_at`
	rows, err := s.db.QueryContext(ctx, q, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []store.AgentShareData
	for rows.Next() {
		var d store.AgentShareData
		var sharedUser, sharedTeam sql.NullString
		var meta *[]byte
		if err := rows.Scan(&d.ID, &d.AgentID, &sharedUser, &sharedTeam, &d.Role, &meta,
			&d.CreatedBy, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, err
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
		result = append(result, d)
	}
	return result, rows.Err()
}

func (s *PGAgentStore) CanAccess(ctx context.Context, agentID uuid.UUID, userID string) (bool, string, error) {
	// Ownership check uses unscoped read so admins and the owner both reach it.
	var ownerID string
	var isDefault bool
	err := s.db.QueryRowContext(ctx,
		"SELECT owner_id, is_default FROM agents WHERE id = $1 AND deleted_at IS NULL", agentID,
	).Scan(&ownerID, &isDefault)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, "", store.ErrNotFound
		}
		return false, "", err
	}
	if ownerID == userID {
		return true, "owner", nil
	}
	if isDefault {
		return true, store.ShareRoleViewer, nil
	}
	// Direct user grant lookup. Implicit team grants are computed by the
	// permissions resolver, not this raw store method.
	userUUID := parseUUIDOrNil(userID)
	if userUUID == uuid.Nil {
		return false, "", nil
	}
	var role string
	err = s.db.QueryRowContext(ctx,
		`SELECT role FROM agent_shares
		  WHERE agent_id = $1 AND shared_with_user_id = $2`, agentID, userUUID,
	).Scan(&role)
	if err != nil {
		return false, "", nil
	}
	return true, role, nil
}

func (s *PGAgentStore) ListAccessible(ctx context.Context, userID string) ([]store.AgentData, error) {
	// $1 = legacy string identity (channel handle / "system"), used for
	//      owner_id legacy column + channel_instances.allow_from match.
	// $2 = UUID identity, used for shared_with_user_id direct grants and
	//      team_user_grants → shared_with_team_id implicit grants.
	userUUID := parseUUIDOrNil(userID)
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+agentSelectCols+`
		 FROM agents
		 WHERE deleted_at IS NULL AND (
		     owner_id      = $1
		     OR owner_user_id = $2
		     OR is_default = true
		     -- Direct user grant.
		     OR id IN (
		         SELECT agent_id FROM agent_shares
		          WHERE shared_with_user_id = $2
		     )
		     -- Implicit team-membership grant: user is in team T,
		     -- and agent is shared to team T.
		     OR id IN (
		         SELECT s.agent_id FROM agent_shares s
		           JOIN team_user_grants g ON g.team_id = s.shared_with_team_id
		          WHERE g.user_id = $2
		     )
		     OR id IN (
		         SELECT agent_id FROM channel_instances ci
		         WHERE ci.enabled = true
		         AND EXISTS (
		             SELECT 1 FROM jsonb_array_elements_text(ci.config->'allow_from') af
		             WHERE af = $1
		         )
		     )
		 )
		 ORDER BY created_at DESC`, userID, userUUID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAgentRows(rows)
}

// --- Scan helpers ---

type agentRowScanner interface {
	Scan(dest ...any) error
}

func scanAgentRow(row agentRowScanner) (*store.AgentData, error) {
	var d store.AgentData
	var frontmatter sql.NullString
	var ownerUserID sql.NullString
	// pgx: scan nullable JSONB into *[]byte (NOT *json.RawMessage — pgx can't scan NULL into defined types)
	var toolsCfg, sandboxCfg, subagentsCfg, memoryCfg, compactionCfg, pruningCfg, otherCfg *[]byte
	var reasoningCfg, oauthCfg, shellCfg, kgCfg, metaCfg *[]byte
	err := row.Scan(&d.ID, &d.AgentKey, &d.DisplayName, &frontmatter, &d.OwnerID, &ownerUserID, &d.Provider, &d.Model,
		&d.ContextWindow, &d.MaxToolIterations, &d.Workspace, &d.RestrictToWorkspace,
		&toolsCfg, &sandboxCfg, &subagentsCfg, &memoryCfg, &compactionCfg, &pruningCfg, &otherCfg,
		&d.Emoji, &d.AgentDescription, &d.ThinkingLevel, &d.MaxTokens,
		&d.SelfEvolve, &d.SkillEvolve, &d.SkillNudgeInterval,
		&reasoningCfg, &d.ShareWorkspace, &d.ShareMemory, &oauthCfg, &shellCfg, &kgCfg,
		&d.IsDefault, &d.Status, &d.BudgetMonthlyCents, &metaCfg, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return nil, err
	}
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
	return result, nil
}

// execMapUpdateWhere is like execMapUpdate but with a custom WHERE clause.
// The whereClause should use $IDX as placeholder for the ID (will be replaced with the next arg index).
// Column names are validated against a strict identifier regex to prevent SQL injection.
func execMapUpdateWhere(ctx context.Context, db *sql.DB, table string, updates map[string]any, whereClause string, id uuid.UUID) error {
	if len(updates) == 0 {
		return nil
	}
	var setClauses []string
	var args []any
	i := 1
	for col, val := range updates {
		if !validColumnName.MatchString(col) {
			slog.Warn("security.invalid_column_name", "table", table, "column", col)
			return fmt.Errorf("invalid column name: %q", col)
		}
		setClauses = append(setClauses, fmt.Sprintf("%s = $%d", col, i))
		args = append(args, val)
		i++
	}
	args = append(args, id)
	idxStr := fmt.Sprintf("$%d", i)
	q := fmt.Sprintf("UPDATE %s SET %s WHERE %s",
		table,
		joinStrings(setClauses, ", "),
		replaceIDX(whereClause, idxStr))
	_, err := db.ExecContext(ctx, q, args...)
	return err
}

// execMapUpdateWhereOwner is like execMapUpdateWhere but appends `AND owner_user_id = $N`.
// Used by Update() to fail-closed when a non-admin caller tries to mutate someone else's row.
func execMapUpdateWhereOwner(ctx context.Context, db *sql.DB, table string, updates map[string]any, id, ownerUserID uuid.UUID) error {
	if len(updates) == 0 {
		return nil
	}
	var setClauses []string
	var args []any
	i := 1
	for col, val := range updates {
		if !validColumnName.MatchString(col) {
			slog.Warn("security.invalid_column_name", "table", table, "column", col)
			return fmt.Errorf("invalid column name: %q", col)
		}
		setClauses = append(setClauses, fmt.Sprintf("%s = $%d", col, i))
		args = append(args, val)
		i++
	}
	args = append(args, id, ownerUserID)
	q := fmt.Sprintf("UPDATE %s SET %s WHERE id = $%d AND owner_user_id = $%d",
		table, joinStrings(setClauses, ", "), i, i+1)
	_, err := db.ExecContext(ctx, q, args...)
	return err
}

func joinStrings(s []string, sep string) string {
	var result strings.Builder
	for i, v := range s {
		if i > 0 {
			result.WriteString(sep)
		}
		result.WriteString(v)
	}
	return result.String()
}

// ResetStuckSummoning flips rows with status='summoning' to 'summon_failed'.
// Called at startup to recover from crashes where summon goroutines died mid-flight.
func (s *PGAgentStore) ResetStuckSummoning(ctx context.Context) (int64, error) {
	const q = `UPDATE agents SET status = $1 WHERE status = $2`
	res, err := s.db.ExecContext(ctx, q, store.AgentStatusSummonFailed, store.AgentStatusSummoning)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func replaceIDX(s, replacement string) string {
	var result strings.Builder
	for i := 0; i < len(s); i++ {
		if i+4 <= len(s) && s[i:i+4] == "$IDX" {
			result.WriteString(replacement)
			i += 3
		} else {
			result.WriteString(string(s[i]))
		}
	}
	return result.String()
}
