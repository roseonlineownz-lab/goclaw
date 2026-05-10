//go:build sqlite || sqliteonly

package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/hooks"
)


// ─── test setup ──────────────────────────────────────────────────────────────

func newHookTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := OpenDB(filepath.Join(t.TempDir(), "hooks_test.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	if err := EnsureSchema(db); err != nil {
		db.Close()
		t.Fatalf("EnsureSchema: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// seedHookAgent inserts a minimal agent for FK satisfaction.
func seedHookAgent(t *testing.T, db *sql.DB) uuid.UUID {
	t.Helper()
	agentID := uuid.Must(uuid.NewV7())
	_, err := db.Exec(
		`INSERT INTO agents (id, agent_key, status, provider, model, owner_id)
		 VALUES (?,'ha-'||substr(?,1,8),'active','test','test-model','owner')`,
		agentID.String(), agentID.String())
	if err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	return agentID
}

func sqliteMinimalHook(event hooks.HookEvent) hooks.HookConfig {
	return hooks.HookConfig{
		Event:       event,
		HandlerType: hooks.HandlerCommand,
		Scope:       hooks.ScopeGlobal,
		Config:      map[string]any{"cmd": "echo ok"},
		Metadata:    map[string]any{},
		TimeoutMS:   5000,
		OnTimeout:   hooks.DecisionBlock,
		Source:      "api",
		Enabled:     true,
		Priority:    0,
	}
}

// ─── CRUD ────────────────────────────────────────────────────────────────────

func TestSQLiteHookStore_CRUD(t *testing.T) {
	db := newHookTestDB(t)
	s := NewSQLiteHookStore(db)
	ctx := context.Background()

	// Create
	cfg := sqliteMinimalHook(hooks.EventPreToolUse)
	id, err := s.Create(ctx, cfg)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id == uuid.Nil {
		t.Fatal("Create returned nil UUID")
	}

	// GetByID
	got, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got == nil {
		t.Fatal("GetByID returned nil for existing hook")
	}
	if got.Event != hooks.EventPreToolUse {
		t.Errorf("event mismatch: got %q want %q", got.Event, hooks.EventPreToolUse)
	}
	if got.Version != 1 {
		t.Errorf("initial version should be 1, got %d", got.Version)
	}
	if !got.Enabled {
		t.Error("hook should be enabled")
	}
	if len(got.Config) == 0 {
		t.Error("config should not be empty")
	}

	// GetByID — not found returns (nil, nil)
	missing, err := s.GetByID(ctx, uuid.Must(uuid.NewV7()))
	if err != nil {
		t.Fatalf("GetByID(missing): unexpected error %v", err)
	}
	if missing != nil {
		t.Fatal("GetByID(missing): expected nil")
	}

	// Update — bumps version
	if err := s.Update(ctx, id, map[string]any{"priority": 10}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	updated, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID after update: %v", err)
	}
	if updated.Priority != 10 {
		t.Errorf("priority not updated: got %d want 10", updated.Priority)
	}
	if updated.Version != 2 {
		t.Errorf("version should be 2 after update, got %d", updated.Version)
	}

	// Update — reject 'version' key
	if err := s.Update(ctx, id, map[string]any{"version": 99}); err == nil {
		t.Fatal("Update with 'version' key should return error")
	}

	// Delete
	if err := s.Delete(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	afterDelete, err := s.GetByID(ctx, id)
	if err != nil || afterDelete != nil {
		t.Fatalf("GetByID after Delete: want (nil,nil), got (%v,%v)", afterDelete, err)
	}
}

// ─── Partial unique indexes ───────────────────────────────────────────────────

// Create honors caller-supplied cfg.ID so the builtin seeder's
// idempotent UUIDv5 keys survive restarts and tests get deterministic IDs.
func TestSQLiteHookStore_CreateHonorsFixedID(t *testing.T) {
	db := newHookTestDB(t)
	s := NewSQLiteHookStore(db)
	ctx := context.Background()

	fixed := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	cfg := sqliteMinimalHook(hooks.EventUserPromptSubmit)
	cfg.ID = fixed

	got, err := s.Create(ctx, cfg)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { s.Delete(ctx, got) })
	if got != fixed {
		t.Fatalf("Create returned id=%s, want %s (caller id must be honored)", got, fixed)
	}

	// A nil cfg.ID still auto-generates.
	cfg2 := sqliteMinimalHook(hooks.EventPreToolUse)
	cfg2.ID = uuid.Nil
	auto, err := s.Create(ctx, cfg2)
	if err != nil {
		t.Fatalf("Create auto: %v", err)
	}
	t.Cleanup(func() { s.Delete(ctx, auto) })
	if auto == uuid.Nil {
		t.Fatal("Create returned nil id for cfg.ID=uuid.Nil path")
	}
}

// ─── ResolveForEvent ordering ────────────────────────────────────────────────

func TestSQLiteHookStore_ResolveForEvent(t *testing.T) {
	db := newHookTestDB(t)
	agentID := seedHookAgent(t, db)
	s := NewSQLiteHookStore(db)
	ctx := context.Background()

	// Insert two enabled hooks at different priorities.
	lo := sqliteMinimalHook(hooks.EventPreToolUse)
	lo.Priority = 0
	hi := sqliteMinimalHook(hooks.EventPreToolUse)
	hi.Priority = 20
	// Make hi a different handler_type to avoid unique index conflict.
	hi.HandlerType = hooks.HandlerHTTP

	idLo, _ := s.Create(ctx, lo)
	idHi, _ := s.Create(ctx, hi)
	t.Cleanup(func() {
		s.Delete(ctx, idLo)
		s.Delete(ctx, idHi)
	})

	event := hooks.Event{
		AgentID:   agentID,
		HookEvent: hooks.EventPreToolUse,
	}
	resolved, err := s.ResolveForEvent(ctx, event)
	if err != nil {
		t.Fatalf("ResolveForEvent: %v", err)
	}
	if len(resolved) < 2 {
		t.Fatalf("expected >=2 resolved hooks, got %d", len(resolved))
	}
	// Highest priority first.
	if resolved[0].Priority < resolved[1].Priority {
		t.Errorf("wrong order: [0].priority=%d < [1].priority=%d",
			resolved[0].Priority, resolved[1].Priority)
	}

	// Disabled hook must not appear.
	dis := sqliteMinimalHook(hooks.EventPreToolUse)
	dis.Enabled = false
	dis.HandlerType = hooks.HandlerPrompt // third distinct handler_type
	idDis, _ := s.Create(ctx, dis)
	t.Cleanup(func() { s.Delete(ctx, idDis) })

	resolved2, _ := s.ResolveForEvent(ctx, event)
	for _, h := range resolved2 {
		if h.ID == idDis {
			t.Error("disabled hook appeared in ResolveForEvent result")
		}
	}
}

// ─── WriteExecution dedup ────────────────────────────────────────────────────

func TestSQLiteHookStore_WriteExecution(t *testing.T) {
	db := newHookTestDB(t)
	s := NewSQLiteHookStore(db)
	ctx := context.Background()

	// Create a parent hook for FK.
	hookID, err := s.Create(ctx, sqliteMinimalHook(hooks.EventStop))
	if err != nil {
		t.Fatalf("Create hook: %v", err)
	}

	execID := uuid.Must(uuid.NewV7())
	dedup := "sqlite-dedup-" + execID.String()[:8]
	exec := hooks.HookExecution{
		ID:         execID,
		HookID:     &hookID,
		SessionID:  "sess-sqlite-001",
		Event:      hooks.EventStop,
		InputHash:  "deadbeef",
		Decision:   hooks.DecisionAllow,
		DurationMS: 7,
		DedupKey:   dedup,
		Metadata:   map[string]any{"src": "test"},
		CreatedAt:  time.Now().UTC(),
	}

	// First insert must succeed.
	if err := s.WriteExecution(ctx, exec); err != nil {
		t.Fatalf("WriteExecution: %v", err)
	}

	// Duplicate dedup_key must be silently ignored.
	if err := s.WriteExecution(ctx, exec); err != nil {
		t.Fatalf("WriteExecution (dedup): %v", err)
	}

	// Verify exactly one row.
	var count int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM hook_executions WHERE dedup_key=?", dedup,
	).Scan(&count); err != nil {
		t.Fatalf("count executions: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 execution row, got %d", count)
	}
}

// ─── Cache invalidation ───────────────────────────────────────────────────────

func TestSQLiteHookStore_CacheInvalidatedOnWrite(t *testing.T) {
	db := newHookTestDB(t)
	agentID := seedHookAgent(t, db)
	s := NewSQLiteHookStore(db)
	ctx := context.Background()

	event := hooks.Event{
		AgentID:   agentID,
		HookEvent: hooks.EventSessionStart,
	}

	// Warm cache.
	before, err := s.ResolveForEvent(ctx, event)
	if err != nil {
		t.Fatalf("ResolveForEvent: %v", err)
	}
	beforeCount := len(before)

	// Create a new hook — must invalidate cache.
	id, err := s.Create(ctx, sqliteMinimalHook(hooks.EventSessionStart))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { s.Delete(ctx, id) })

	after, err := s.ResolveForEvent(ctx, event)
	if err != nil {
		t.Fatalf("ResolveForEvent after create: %v", err)
	}
	if len(after) <= beforeCount {
		t.Errorf("cache not invalidated: before=%d after=%d", beforeCount, len(after))
	}
}

// ─── Global-scope hooks visible ──────────────────────────────────────────────

func TestSQLiteHookStore_GlobalScopeVisible(t *testing.T) {
	db := newHookTestDB(t)
	agentID := seedHookAgent(t, db)
	s := NewSQLiteHookStore(db)
	ctx := context.Background()

	// Create a global hook.
	globalCfg := hooks.HookConfig{
		Event:       hooks.EventPostToolUse,
		HandlerType: hooks.HandlerCommand,
		Scope:       hooks.ScopeGlobal,
		Config:      map[string]any{"cmd": "audit.sh"},
		Metadata:    map[string]any{},
		TimeoutMS:   3000,
		OnTimeout:   hooks.DecisionAllow,
		Source:      "seed",
		Enabled:     true,
		Priority:    5,
	}
	globalID, err := s.Create(ctx, globalCfg)
	if err != nil {
		t.Fatalf("Create global hook: %v", err)
	}
	t.Cleanup(func() { s.Delete(ctx, globalID) })

	// ResolveForEvent must include the global hook.
	event := hooks.Event{
		AgentID:   agentID,
		HookEvent: hooks.EventPostToolUse,
	}
	resolved, err := s.ResolveForEvent(ctx, event)
	if err != nil {
		t.Fatalf("ResolveForEvent: %v", err)
	}
	found := false
	for _, h := range resolved {
		if h.ID == globalID {
			found = true
		}
	}
	if !found {
		t.Error("global hook not visible in ResolveForEvent")
	}
}

// TestSQLiteHookStore_BuiltinReadOnly: user-facing writes on source='builtin'
// rows may only toggle enabled; WithSeedBypass unlocks.
func TestSQLiteHookStore_BuiltinReadOnly(t *testing.T) {
	db := newHookTestDB(t)
	s := NewSQLiteHookStore(db)

	seedCtx := hooks.WithSeedBypass(context.Background())

	cfg := hooks.HookConfig{
		ID:          uuid.MustParse("11111111-2222-3333-4444-555555555555"),
		Event:       hooks.EventUserPromptSubmit,
		HandlerType: hooks.HandlerScript,
		Scope:       hooks.ScopeGlobal,
		Config:      map[string]any{"source": "// v1"},
		Metadata:    map[string]any{"builtin": true, "version": 1},
		TimeoutMS:   500,
		OnTimeout:   hooks.DecisionAllow,
		Source:      hooks.SourceBuiltin,
		Enabled:     true,
	}
	id, err := s.Create(seedCtx, cfg)
	if err != nil {
		t.Fatalf("seed create: %v", err)
	}
	t.Cleanup(func() { s.Delete(seedCtx, id) })

	userCtx := context.Background()

	if err := s.Update(userCtx, id, map[string]any{"matcher": "evil"}); !errors.Is(err, hooks.ErrBuiltinReadOnly) {
		t.Fatalf("Update(matcher) err=%v, want ErrBuiltinReadOnly", err)
	}

	if err := s.Update(userCtx, id, map[string]any{"enabled": false}); err != nil {
		t.Fatalf("Update(enabled) should succeed: %v", err)
	}

	if err := s.Delete(userCtx, id); !errors.Is(err, hooks.ErrBuiltinReadOnly) {
		t.Fatalf("Delete user err=%v, want ErrBuiltinReadOnly", err)
	}

	if err := s.Update(seedCtx, id, map[string]any{"matcher": "ok"}); err != nil {
		t.Fatalf("seed-bypass Update should succeed: %v", err)
	}
}
