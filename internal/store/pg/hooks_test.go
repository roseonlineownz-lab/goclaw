package pg

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/nextlevelbuilder/goclaw/internal/hooks"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// ─── test DB setup ───────────────────────────────────────────────────────────

func hooksTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skipf("TEST_DATABASE_URL not set; skipping PG hook store tests")
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open DB: %v", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		t.Skipf("PG not reachable: %v", err)
	}

	m, err := migrate.New("file://../../../migrations", dsn)
	if err != nil {
		db.Close()
		t.Fatalf("migrate.New: %v", err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		db.Close()
		t.Fatalf("migrate up: %v", err)
	}
	m.Close()

	InitSqlx(db)
	t.Cleanup(func() { db.Close() })
	return db
}

// seedAgent inserts a minimal agent row and registers cleanup.
func seedAgent(t *testing.T, db *sql.DB) uuid.UUID {
	t.Helper()
	agentID := uuid.Must(uuid.NewV7())
	_, err := db.Exec(
		`INSERT INTO agents (id, agent_key, status, provider, model, owner_id)
		 VALUES ($1,$2,'active','test','test-model','owner') ON CONFLICT DO NOTHING`,
		agentID, "hook-agent-"+agentID.String())
	if err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	t.Cleanup(func() {
		db.Exec("DELETE FROM hook_executions WHERE hook_id IN (SELECT id FROM hooks WHERE agent_id=$1)", agentID)
		db.Exec("DELETE FROM hooks WHERE agent_id=$1", agentID)
		db.Exec("DELETE FROM agents WHERE id=$1", agentID)
	})
	return agentID
}

func masterCtx() context.Context {
	return context.Background()
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func minimalHook(_ uuid.UUID, event hooks.HookEvent) hooks.HookConfig {
	return hooks.HookConfig{
		Event:       event,
		HandlerType: hooks.HandlerCommand,
		Scope:       hooks.ScopeUser,
		Config:      map[string]any{"cmd": "echo test"},
		Metadata:    map[string]any{},
		TimeoutMS:   5000,
		OnTimeout:   hooks.DecisionBlock,
		Source:      "api",
		Enabled:     true,
		Priority:    0,
	}
}

// ─── CRUD ────────────────────────────────────────────────────────────────────

func TestPGHookStore_CRUD(t *testing.T) {
	db := hooksTestDB(t)
	s := NewPGHookStore(db)
	ctx := context.Background()

	// Create
	cfg := minimalHook(uuid.Nil, hooks.EventPreToolUse)
	id, err := s.Create(ctx, cfg)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id == uuid.Nil {
		t.Fatal("Create returned nil UUID")
	}
	t.Cleanup(func() { db.Exec("DELETE FROM hooks WHERE id=$1", id) })

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

	// GetByID — not found returns nil, nil
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
	updated, _ := s.GetByID(ctx, id)
	if updated.Priority != 10 {
		t.Errorf("priority not updated: got %d", updated.Priority)
	}
	if updated.Version != 2 {
		t.Errorf("version should be 2 after update, got %d", updated.Version)
	}

	// Update — reject version key
	if err := s.Update(ctx, id, map[string]any{"version": 99}); err == nil {
		t.Fatal("Update with 'version' key should return error")
	}

	// Delete
	if err := s.Delete(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	deleted, err := s.GetByID(ctx, id)
	if err != nil || deleted != nil {
		t.Fatalf("GetByID after Delete: want (nil,nil), got (%v,%v)", deleted, err)
	}
}

// ─── List isolation (single-tenant) ──────────────────────────────────────────

// TestPGHookStore_ListReturnsCreated verifies List returns hooks created in this session.
// In v4 single-tenant mode there is no per-tenant scoping in the DB.
func TestPGHookStore_ListReturnsCreated(t *testing.T) {
	db := hooksTestDB(t)
	s := NewPGHookStore(db)
	ctx := context.Background()

	cfg := minimalHook(uuid.Nil, hooks.EventStop)
	idA, err := s.Create(ctx, cfg)
	if err != nil {
		t.Fatalf("Create A: %v", err)
	}
	t.Cleanup(func() { s.Delete(masterCtx(), idA) })

	list, err := s.List(ctx, hooks.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, h := range list {
		if h.ID == idA {
			found = true
		}
	}
	if !found {
		t.Error("List did not return the created hook")
	}
}

// ─── ResolveForEvent ─────────────────────────────────────────────────────────

func TestPGHookStore_ResolveForEvent(t *testing.T) {
	db := hooksTestDB(t)
	agentID := seedAgent(t, db)
	s := NewPGHookStore(db)
	ctx := context.Background()

	// Create two enabled hooks for pre_tool_use, different priorities.
	lowPriCfg := minimalHook(uuid.Nil, hooks.EventPreToolUse)
	lowPriCfg.Priority = 0
	highPriCfg := minimalHook(uuid.Nil, hooks.EventPreToolUse)
	highPriCfg.Priority = 10

	id1, _ := s.Create(ctx, lowPriCfg)
	id2, _ := s.Create(ctx, highPriCfg)
	t.Cleanup(func() {
		s.Delete(masterCtx(), id1)
		s.Delete(masterCtx(), id2)
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
		t.Fatalf("expected >=2 hooks, got %d", len(resolved))
	}
	// First should be highest priority.
	if resolved[0].Priority < resolved[1].Priority {
		t.Errorf("hooks not ordered by priority DESC: [0]=%d [1]=%d",
			resolved[0].Priority, resolved[1].Priority)
	}

	// Disabled hook should not appear.
	disabledCfg := minimalHook(uuid.Nil, hooks.EventPreToolUse)
	disabledCfg.Enabled = false
	idDisabled, _ := s.Create(ctx, disabledCfg)
	t.Cleanup(func() { s.Delete(masterCtx(), idDisabled) })

	resolved2, _ := s.ResolveForEvent(ctx, event)
	for _, h := range resolved2 {
		if h.ID == idDisabled {
			t.Error("disabled hook appeared in ResolveForEvent")
		}
	}
}

// ─── WriteExecution ──────────────────────────────────────────────────────────

func TestPGHookStore_WriteExecution(t *testing.T) {
	db := hooksTestDB(t)
	s := NewPGHookStore(db)
	ctx := context.Background()

	// Create a hook for FK.
	cfg := minimalHook(uuid.Nil, hooks.EventStop)
	hookID, err := s.Create(ctx, cfg)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() {
		db.Exec("DELETE FROM hook_executions WHERE hook_id=$1", hookID)
		s.Delete(masterCtx(), hookID)
	})

	execID := uuid.Must(uuid.NewV7())
	dedup := "test-dedup-" + execID.String()[:8]
	exec := hooks.HookExecution{
		ID:         execID,
		HookID:     &hookID,
		SessionID:  "sess-123",
		Event:      hooks.EventStop,
		InputHash:  "aaabbbccc",
		Decision:   hooks.DecisionAllow,
		DurationMS: 42,
		DedupKey:   dedup,
		Metadata:   map[string]any{"test": true},
		CreatedAt:  time.Now().UTC(),
	}

	// First write should succeed.
	if err := s.WriteExecution(ctx, exec); err != nil {
		t.Fatalf("WriteExecution: %v", err)
	}

	// Duplicate dedup_key should be silently ignored (idempotent).
	if err := s.WriteExecution(ctx, exec); err != nil {
		t.Fatalf("WriteExecution (dedup): %v", err)
	}

	// Verify the row exists.
	var count int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM hook_executions WHERE dedup_key=$1", dedup,
	).Scan(&count); err != nil {
		t.Fatalf("count executions: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 execution row, got %d", count)
	}
}

// ─── Cache invalidation ───────────────────────────────────────────────────────

func TestPGHookStore_CacheInvalidatedOnWrite(t *testing.T) {
	db := hooksTestDB(t)
	agentID := seedAgent(t, db)
	s := NewPGHookStore(db)
	ctx := context.Background()

	event := hooks.Event{
		AgentID:   agentID,
		HookEvent: hooks.EventSessionStart,
	}

	// Resolve populates cache.
	before, err := s.ResolveForEvent(ctx, event)
	if err != nil {
		t.Fatalf("ResolveForEvent: %v", err)
	}
	beforeCount := len(before)

	// Add a hook — cache should be invalidated.
	cfg := minimalHook(uuid.Nil, hooks.EventSessionStart)
	id, err := s.Create(ctx, cfg)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { s.Delete(masterCtx(), id) })

	after, err := s.ResolveForEvent(ctx, event)
	if err != nil {
		t.Fatalf("ResolveForEvent after create: %v", err)
	}
	if len(after) <= beforeCount {
		t.Errorf("expected more hooks after create: before=%d after=%d", beforeCount, len(after))
	}
}

// TestPGHookStore_CreateHonorsFixedID verifies that a caller-supplied cfg.ID is
// preserved so the builtin seeder's idempotent UUIDv5 keys survive restarts.
func TestPGHookStore_CreateHonorsFixedID(t *testing.T) {
	db := hooksTestDB(t)
	s := NewPGHookStore(db)
	ctx := context.Background()

	fixed := uuid.MustParse("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	cfg := minimalHook(uuid.Nil, hooks.EventUserPromptSubmit)
	cfg.ID = fixed

	got, err := s.Create(ctx, cfg)
	if err != nil {
		t.Fatalf("Create fixed id: %v", err)
	}
	t.Cleanup(func() { s.Delete(masterCtx(), got) })
	if got != fixed {
		t.Fatalf("returned id=%s, want %s (caller id must be honored)", got, fixed)
	}

	// Nil cfg.ID still auto-generates a UUIDv7.
	cfg2 := minimalHook(uuid.Nil, hooks.EventPreToolUse)
	cfg2.ID = uuid.Nil
	auto, err := s.Create(ctx, cfg2)
	if err != nil {
		t.Fatalf("Create auto id: %v", err)
	}
	t.Cleanup(func() { s.Delete(masterCtx(), auto) })
	if auto == uuid.Nil {
		t.Fatal("Create returned nil id for cfg.ID=uuid.Nil path")
	}
}

// ─── builtin-row readonly protection ─────────────────────────────────────────

// TestPGHookStore_BuiltinReadOnly exercises the builtin-row guard: user-facing
// writes on a source='builtin' row may only toggle enabled; every other patch
// must surface ErrBuiltinReadOnly. The seed bypass marker unlocks full writes
// for the loader package only.
func TestPGHookStore_BuiltinReadOnly(t *testing.T) {
	db := hooksTestDB(t)
	s := NewPGHookStore(db)

	// Seed a builtin row via WithSeedBypass (the only authorized path).
	seedCtx := hooks.WithSeedBypass(store.WithRole(masterCtx(), store.RoleRoot))
	cfg := minimalHook(uuid.Nil, hooks.EventUserPromptSubmit)
	cfg.Source = hooks.SourceBuiltin
	cfg.Scope = hooks.ScopeGlobal
	cfg.HandlerType = hooks.HandlerScript
	cfg.Config = map[string]any{"source": "// v1"}
	fixedID := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	cfg.ID = fixedID
	id, err := s.Create(seedCtx, cfg)
	if err != nil {
		t.Fatalf("seed create: %v", err)
	}
	t.Cleanup(func() { s.Delete(seedCtx, id) })

	// User ctx: non-builtin-bypass. Non-enabled patch must be rejected.
	userCtx := context.Background()
	err = s.Update(userCtx, id, map[string]any{"matcher": "evil"})
	if !errors.Is(err, hooks.ErrBuiltinReadOnly) {
		t.Fatalf("Update(matcher) err=%v, want ErrBuiltinReadOnly", err)
	}

	// Enabled toggle through master context is allowed.
	if err := s.Update(masterCtx(), id, map[string]any{"enabled": false}); err != nil {
		t.Fatalf("Update(enabled) should succeed on builtin: %v", err)
	}

	// Delete blocked for users.
	if err := s.Delete(userCtx, id); !errors.Is(err, hooks.ErrBuiltinReadOnly) {
		t.Fatalf("Delete user err=%v, want ErrBuiltinReadOnly", err)
	}

	// Seed bypass unlocks full writes (round-trip to prove).
	if err := s.Update(seedCtx, id, map[string]any{"matcher": "ok"}); err != nil {
		t.Fatalf("seed-bypass Update should succeed: %v", err)
	}
}
