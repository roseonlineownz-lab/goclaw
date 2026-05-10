//go:build sqlite || sqliteonly

package sqlitestore

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	storelib "github.com/nextlevelbuilder/goclaw/internal/store"
)

// Regression test for PR #897 — fresh SQLite DBs crash on agent setting save
// when UI sends null for NOT NULL columns (other_config, tools_config,
// self_evolve, skill_evolve, is_default). Update() must coerce nil to the
// column's default value ({}, false) to satisfy NOT NULL constraints.
func TestSQLiteAgentStore_Update_CoerceNullForNotNullColumns(t *testing.T) {
	db, _, agentID := newAgentUpdateTestFixture(t)
	store := NewSQLiteAgentStore(db)
	// Use admin role so agentOwnerFilter returns empty clause (no owner_user_id check).
	ctx := storelib.WithRole(context.Background(), "admin")

	// Simulate UI sending null for every NOT NULL column that can be toggled off.
	updates := map[string]any{
		"other_config":          nil,
		"tools_config":          nil,
		"reasoning_config":      nil,
		"share_workspace":       nil,
		"share_memory":          nil,
		"chatgpt_oauth_routing": nil,
		"shell_deny_groups":     nil,
		"kg_dedup_config":       nil,
		"self_evolve":           nil,
		"skill_evolve":          nil,
		"is_default":            nil,
		"skill_nudge_interval":  nil,
		"max_tokens":            nil,
		"emoji":                 nil,
		"agent_description":     nil,
		"thinking_level":        nil,
	}

	if err := store.Update(ctx, agentID, updates); err != nil {
		t.Fatalf("Update with null NOT NULL columns: %v", err)
	}

	// Verify JSON columns became '{}', BOOL → 0, INT → 0, TEXT → ''.
	var (
		otherCfg, toolsCfg, reasoningCfg, oauthRouting, shellDeny, kgDedup string
		shareWorkspace, shareMemory                                        bool
		selfEvolve, skillEvolve, isDefault                                 bool
		skillNudge, maxTokens                                              int
		emoji, agentDesc, thinkingLvl                                      string
	)
	err := db.QueryRowContext(ctx,
		`SELECT other_config, tools_config, reasoning_config,
			share_workspace, share_memory,
			chatgpt_oauth_routing, shell_deny_groups, kg_dedup_config,
			self_evolve, skill_evolve, is_default,
			skill_nudge_interval, max_tokens,
			emoji, agent_description, thinking_level
		 FROM agents WHERE id = ?`, agentID).Scan(
		&otherCfg, &toolsCfg, &reasoningCfg,
		&shareWorkspace, &shareMemory,
		&oauthRouting, &shellDeny, &kgDedup,
		&selfEvolve, &skillEvolve, &isDefault,
		&skillNudge, &maxTokens,
		&emoji, &agentDesc, &thinkingLvl,
	)
	if err != nil {
		t.Fatalf("scan back: %v", err)
	}

	jsonCols := map[string]string{
		"other_config": otherCfg, "tools_config": toolsCfg,
		"reasoning_config":      reasoningCfg,
		"chatgpt_oauth_routing": oauthRouting, "shell_deny_groups": shellDeny,
		"kg_dedup_config": kgDedup,
	}
	for name, got := range jsonCols {
		if got != "{}" {
			t.Errorf("%s: got %q, want %q", name, got, "{}")
		}
	}

	boolCols := map[string]bool{
		"self_evolve":     selfEvolve,
		"skill_evolve":    skillEvolve,
		"is_default":      isDefault,
		"share_workspace": shareWorkspace,
		"share_memory":    shareMemory,
	}
	for name, got := range boolCols {
		if got {
			t.Errorf("%s: got true, want false", name)
		}
	}

	intCols := map[string]int{
		"skill_nudge_interval": skillNudge, "max_tokens": maxTokens,
	}
	for name, got := range intCols {
		if got != 0 {
			t.Errorf("%s: got %d, want 0", name, got)
		}
	}

	textCols := map[string]string{
		"emoji": emoji, "agent_description": agentDesc, "thinking_level": thinkingLvl,
	}
	for name, got := range textCols {
		if got != "" {
			t.Errorf("%s: got %q, want empty string", name, got)
		}
	}
}

// newAgentUpdateTestFixture opens a fresh SQLite DB with schema loaded and
// inserts a minimal agent for Update() tests.
func newAgentUpdateTestFixture(t *testing.T) (*sql.DB, uuid.UUID, uuid.UUID) {
	t.Helper()
	db, err := OpenDB(filepath.Join(t.TempDir(), "agent_update_test.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	if err := EnsureSchema(db); err != nil {
		db.Close()
		t.Fatalf("EnsureSchema: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	agentID := uuid.Must(uuid.NewV7())
	if _, err := db.Exec(
		`INSERT INTO agents (id, agent_key, status, provider, model, owner_id)
		 VALUES (?,'au-'||substr(?,1,8),'active','test','test-model','owner')`,
		agentID.String(), agentID.String()); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	return db, uuid.Nil, agentID
}
