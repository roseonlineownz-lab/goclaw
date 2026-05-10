//go:build integration

// Atomic 5-table merge TX tests: verifies that MergeUserAggregate covers all
// seven tables (channel_contacts, memory_documents, agent_config_permissions,
// user_context_files, agent_sessions, traces, spans) in a single transaction.
package integration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/store/pg"
)

// ── Fixtures ─────────────────────────────────────────────────────────────────

// seedMergeUserM inserts a users row for merge tests. Uses a unique email/key
// derived from the provided suffix. Different from the seedMergeUser helper in
// contact_pairing_merge_separation_test.go to avoid redeclaration; distinguished
// by the "M" suffix in the name.
func seedMergeUserM(t *testing.T, db *sql.DB, suf string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := db.Exec(
		`INSERT INTO users (id, email, password_hash, display_name, role, kind, user_key)
		 VALUES ($1, $2, 'x', 'test', 'member', 'human', $3)`,
		id, "5tm-"+suf+"@local", "5tm-"+suf,
	)
	if err != nil {
		t.Fatalf("seedMergeUserM(%s): %v", suf, err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM users WHERE id = $1", id) })
	return id
}

// seedContactLinked inserts a channel_contacts row linked to a user.
func seedContactLinked(t *testing.T, db *sql.DB, userID uuid.UUID, suf string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := db.Exec(
		`INSERT INTO channel_contacts (id, channel_type, sender_id, contact_type, user_id)
		 VALUES ($1, 'telegram', $2, 'user', $3)`,
		id, "lnk-5t-"+suf, userID,
	)
	if err != nil {
		t.Fatalf("seedContactLinked(%s): %v", suf, err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM channel_contacts WHERE id = $1", id) })
	return id
}

// seedMergeSession inserts an agent_sessions row for userID and returns the session_key.
// Distinct from the package-level seedSession helper (which does not set user_id).
func seedMergeSession(t *testing.T, db *sql.DB, agentID, userID uuid.UUID, suf string) string {
	t.Helper()
	key := "sess-5tm-" + suf
	_, err := db.Exec(
		`INSERT INTO agent_sessions (session_key, agent_id, user_id, messages, summary)
		 VALUES ($1, $2, $3, '[]', '')`,
		key, agentID, userID,
	)
	if err != nil {
		t.Fatalf("seedMergeSession(%s): %v", suf, err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM agent_sessions WHERE session_key = $1", key) })
	return key
}

// seedMemDocUser inserts a memory_documents row keyed by userID.
// Uses the FS-backed schema: file_path + content_hash (Plan #5 memory-5d-scope).
func seedMemDocUser(t *testing.T, db *sql.DB, agentID, userID uuid.UUID, path string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := db.Exec(
		`INSERT INTO memory_documents (id, agent_id, user_id, path, file_path, content_hash)
		 VALUES ($1, $2, $3, $4, '', '')`,
		id, agentID, userID, path,
	)
	if err != nil {
		t.Fatalf("seedMemDocUser(%s): %v", path, err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM memory_documents WHERE id = $1", id) })
	return id
}

// seedMergeContextFile inserts a user_context_files row for userID.
// Uses file_name (not file_path) per current schema.
func seedMergeContextFile(t *testing.T, db *sql.DB, agentID, userID uuid.UUID, suf string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := db.Exec(
		`INSERT INTO user_context_files (id, agent_id, user_id, file_name, content)
		 VALUES ($1, $2, $3, $4, 'test')`,
		id, agentID, userID, "ctx-5tm-"+suf+".md",
	)
	if err != nil {
		t.Fatalf("seedMergeContextFile(%s): %v", suf, err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM user_context_files WHERE id = $1", id) })
	return id
}

// seedMergeConfigPerm inserts an agent_config_permissions row for userID (VARCHAR).
func seedMergeConfigPerm(t *testing.T, db *sql.DB, agentID uuid.UUID, userIDStr, scope, configType string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := db.Exec(
		`INSERT INTO agent_config_permissions (id, agent_id, user_id, scope, config_type, permission)
		 VALUES ($1, $2, $3, $4, $5, 'allow')`,
		id, agentID, userIDStr, scope, configType,
	)
	if err != nil {
		t.Fatalf("seedMergeConfigPerm(%s/%s): %v", scope, configType, err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM agent_config_permissions WHERE id = $1", id) })
	return id
}

// seedMergeTrace inserts a traces row linked to contactID.
func seedMergeTrace(t *testing.T, db *sql.DB, contactID uuid.UUID, suf string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := db.Exec(
		`INSERT INTO traces (id, contact_id, status, start_time) VALUES ($1, $2, 'completed', $3)`,
		id, contactID, time.Now(),
	)
	if err != nil {
		t.Fatalf("seedMergeTrace(%s): %v", suf, err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM traces WHERE id = $1", id) })
	return id
}

// seedMergeSpan inserts a spans row linked to a trace and contactID.
func seedMergeSpan(t *testing.T, db *sql.DB, traceID, contactID uuid.UUID, suf string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := db.Exec(
		`INSERT INTO spans (id, trace_id, contact_id, span_type, start_time) VALUES ($1, $2, $3, 'llm', $4)`,
		id, traceID, contactID, time.Now(),
	)
	if err != nil {
		t.Fatalf("seedMergeSpan(%s): %v", suf, err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM spans WHERE id = $1", id) })
	return id
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func countWhere5T(t *testing.T, db *sql.DB, table, cond string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE %s", table, cond), args...).Scan(&n); err != nil {
		t.Fatalf("countWhere5T %s WHERE %s: %v", table, cond, err)
	}
	return n
}

// ── Case A: happy path — all tables updated ───────────────────────────────────

// TestMergeAtomic5Table_HappyPath asserts that a successful MergeUserAggregate
// call updates all seven tables within a single commit. No rows are lost;
// all source references flip to targetUserID.
func TestMergeAtomic5Table_HappyPath(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	suf := uuid.New().String()[:8]

	_, agentID := seedTenantAgent(t, db)
	sourceUser := seedMergeUserM(t, db, "src-"+suf)
	targetUser := seedMergeUserM(t, db, "tgt-"+suf)
	contactID := seedContactLinked(t, db, sourceUser, suf)

	// Seed one row in each table keyed to sourceUser / contactID.
	sessKey := seedMergeSession(t, db, agentID, sourceUser, suf)
	memDocID := seedMemDocUser(t, db, agentID, sourceUser, "5t-happy/"+suf+"/doc.md")
	ctxFileID := seedMergeContextFile(t, db, agentID, sourceUser, suf)
	_ = seedMergeConfigPerm(t, db, agentID, sourceUser.String(), "scope:5t-"+suf, "write_file")
	traceID := seedMergeTrace(t, db, contactID, suf)
	spanID := seedMergeSpan(t, db, traceID, contactID, suf)

	contacts := pg.NewPGContactStore(db)
	err := contacts.MergeUserAggregate(ctx, store.MergeUserAggregateRequest{
		ContactIDs:    []uuid.UUID{contactID},
		SourceUserIDs: []uuid.UUID{sourceUser},
		TargetUserID:  targetUser,
		MergeAudit:    []byte(`{"merged_by":"test"}`),
	})
	if err != nil {
		t.Fatalf("MergeUserAggregate: %v", err)
	}

	// channel_contacts.merged_id must be set to targetUser.
	n := countWhere5T(t, db, "channel_contacts", "id = $1 AND merged_id = $2", contactID, targetUser)
	if n != 1 {
		t.Errorf("channel_contacts: merged_id not set; got count=%d want 1", n)
	}

	// agent_sessions must flip to targetUser.
	n = countWhere5T(t, db, "agent_sessions", "session_key = $1 AND user_id = $2", sessKey, targetUser)
	if n != 1 {
		t.Errorf("agent_sessions: user_id not updated; got count=%d want 1", n)
	}

	// memory_documents (user_id row) must flip.
	n = countWhere5T(t, db, "memory_documents", "id = $1 AND user_id = $2", memDocID, targetUser)
	if n != 1 {
		t.Errorf("memory_documents (user_id): not updated; got count=%d want 1", n)
	}

	// user_context_files must flip.
	n = countWhere5T(t, db, "user_context_files", "id = $1 AND user_id = $2", ctxFileID, targetUser)
	if n != 1 {
		t.Errorf("user_context_files: user_id not updated; got count=%d want 1", n)
	}

	// agent_config_permissions: no source rows remain.
	n = countWhere5T(t, db, "agent_config_permissions", "user_id = $1", sourceUser.String())
	if n != 0 {
		t.Errorf("agent_config_permissions: %d source rows remain; want 0", n)
	}
	n = countWhere5T(t, db, "agent_config_permissions", "user_id = $1", targetUser.String())
	if n != 1 {
		t.Errorf("agent_config_permissions: target rows count=%d; want 1", n)
	}

	// traces must flip to targetUser (user_id column exists in current schema).
	n = countWhere5T(t, db, "traces", "id = $1 AND user_id = $2", traceID, targetUser)
	if n != 1 {
		t.Errorf("traces: user_id not updated; got count=%d want 1", n)
	}

	// spans: user_id column is added by a future migration. Check only if present.
	var spansHasUserID bool
	db.QueryRow(`SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		 WHERE table_name = 'spans' AND column_name = 'user_id'
	)`).Scan(&spansHasUserID)
	if spansHasUserID {
		n = countWhere5T(t, db, "spans", "id = $1 AND user_id = $2", spanID, targetUser)
		if n != 1 {
			t.Errorf("spans: user_id not updated; got count=%d want 1", n)
		}
	} else {
		// Verify span still exists (not deleted by merge).
		n = countWhere5T(t, db, "spans", "id = $1", spanID)
		if n != 1 {
			t.Errorf("spans: row missing after merge (must only update, not delete)")
		}
	}
}

// ── Case B: rollback on mid-TX error ─────────────────────────────────────────

// TestMergeAtomic5Table_RollbackOnError verifies that when MergeUserAggregate
// receives a target user that does not exist, none of the tables are modified.
func TestMergeAtomic5Table_RollbackOnError(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	suf := uuid.New().String()[:8]

	_, agentID := seedTenantAgent(t, db)
	sourceUser := seedMergeUserM(t, db, "rb-src-"+suf)
	contactID := seedContactLinked(t, db, sourceUser, "rb-"+suf)

	sessKey := seedMergeSession(t, db, agentID, sourceUser, "rb-"+suf)
	_ = seedMemDocUser(t, db, agentID, sourceUser, "5t-rb/"+suf+"/doc.md")
	_ = seedMergeContextFile(t, db, agentID, sourceUser, "rb-"+suf)
	_ = seedMergeConfigPerm(t, db, agentID, sourceUser.String(), "scope:rb-"+suf, "write_file")
	traceID := seedMergeTrace(t, db, contactID, "rb-"+suf)
	_ = seedMergeSpan(t, db, traceID, contactID, "rb-"+suf)

	// Nonexistent target triggers pre-check failure before any UPDATE runs.
	nonexistentTarget := uuid.New()

	contacts := pg.NewPGContactStore(db)
	err := contacts.MergeUserAggregate(ctx, store.MergeUserAggregateRequest{
		ContactIDs:    []uuid.UUID{contactID},
		SourceUserIDs: []uuid.UUID{sourceUser},
		TargetUserID:  nonexistentTarget,
	})
	if err == nil {
		t.Fatal("expected error for nonexistent target, got nil")
	}
	if !errors.Is(err, store.ErrMergeTargetUserNotFound) {
		t.Errorf("expected ErrMergeTargetUserNotFound, got: %v", err)
	}

	// channel_contacts merged_id must still be NULL.
	n := countWhere5T(t, db, "channel_contacts", "id = $1 AND merged_id IS NULL", contactID)
	if n != 1 {
		t.Errorf("channel_contacts: merged_id was mutated despite rollback")
	}

	// agent_sessions user_id unchanged.
	n = countWhere5T(t, db, "agent_sessions", "session_key = $1 AND user_id = $2", sessKey, sourceUser)
	if n != 1 {
		t.Errorf("agent_sessions: user_id was mutated despite rollback")
	}

	// agent_config_permissions still keyed to sourceUser.
	n = countWhere5T(t, db, "agent_config_permissions", "user_id = $1", sourceUser.String())
	if n != 1 {
		t.Errorf("agent_config_permissions: source row missing after rollback")
	}
}

// ── Case C: chaos — 50 disjoint concurrent merges ────────────────────────────

// mergePair holds the IDs seeded for one disjoint merge pair in the chaos test.
type mergePair struct {
	sourceUser uuid.UUID
	targetUser uuid.UUID
	contactID  uuid.UUID
	sessKey    string
	memDocID   uuid.UUID
	traceID    uuid.UUID
	spanID     uuid.UUID
}

// TestMergeAtomic5Table_ChaosDisjointPairs runs 50 concurrent MergeUserAggregate
// calls with DISJOINT (source, target) pairs. After all goroutines complete, sum
// invariants across the tables are checked: no rows lost and no source references remain.
func TestMergeAtomic5Table_ChaosDisjointPairs(t *testing.T) {
	const pairs = 50

	db := testDB(t)
	ctx := context.Background()

	_, agentID := seedTenantAgent(t, db)

	seeded := make([]mergePair, pairs)
	for i := 0; i < pairs; i++ {
		suf := fmt.Sprintf("ch%d-%s", i, uuid.New().String()[:6])
		src := seedMergeUserM(t, db, "cs-src-"+suf)
		tgt := seedMergeUserM(t, db, "cs-tgt-"+suf)
		cid := seedContactLinked(t, db, src, suf)
		sk := seedMergeSession(t, db, agentID, src, suf)
		md := seedMemDocUser(t, db, agentID, src, fmt.Sprintf("chaos5t/%d/%s/doc.md", i, suf))
		tr := seedMergeTrace(t, db, cid, suf)
		sp := seedMergeSpan(t, db, tr, cid, suf)
		seeded[i] = mergePair{src, tgt, cid, sk, md, tr, sp}
	}

	// Snapshot pre-merge counts for sum invariant.
	contactIDArr := mergePairContactIDs(seeded)
	sourceIDArr := mergePairSourceUserIDs(seeded)
	traceIDArr := mergePairTraceIDs(seeded)
	spanIDArr := mergePairSpanIDs(seeded)

	var preSessCount, preMemCount, preTraceCount, preSpanCount int
	db.QueryRow("SELECT COUNT(*) FROM agent_sessions WHERE session_key LIKE 'sess-5tm-ch%'").Scan(&preSessCount)
	db.QueryRow("SELECT COUNT(*) FROM memory_documents WHERE path LIKE 'chaos5t/%'").Scan(&preMemCount)
	db.QueryRow("SELECT COUNT(*) FROM traces WHERE id = ANY($1)", pq.Array(traceIDArr)).Scan(&preTraceCount)
	db.QueryRow("SELECT COUNT(*) FROM spans WHERE id = ANY($1)", pq.Array(spanIDArr)).Scan(&preSpanCount)

	contacts := pg.NewPGContactStore(db)

	var wg sync.WaitGroup
	errs := make([]error, pairs)
	for i := 0; i < pairs; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			p := seeded[idx]
			errs[idx] = contacts.MergeUserAggregate(ctx, store.MergeUserAggregateRequest{
				ContactIDs:    []uuid.UUID{p.contactID},
				SourceUserIDs: []uuid.UUID{p.sourceUser},
				TargetUserID:  p.targetUser,
				MergeAudit:    []byte(`{"merged_by":"chaos-test"}`),
			})
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: MergeUserAggregate error: %v", i, err)
		}
	}

	// All 50 contacts must have merged_id set.
	n := countWhere5T(t, db, "channel_contacts",
		"id = ANY($1) AND merged_id IS NOT NULL", pq.Array(contactIDArr))
	if n != pairs {
		t.Errorf("channel_contacts merged: got %d want %d", n, pairs)
	}

	// No source user_id rows must remain in agent_sessions.
	n = countWhere5T(t, db, "agent_sessions",
		"user_id = ANY($1)", pq.Array(sourceIDArr))
	if n != 0 {
		t.Errorf("agent_sessions orphan source rows: got %d want 0", n)
	}

	// Sum invariants: row counts must be identical pre/post (no rows deleted).
	var postSessCount, postMemCount, postTraceCount, postSpanCount int
	db.QueryRow("SELECT COUNT(*) FROM agent_sessions WHERE session_key LIKE 'sess-5tm-ch%'").Scan(&postSessCount)
	db.QueryRow("SELECT COUNT(*) FROM memory_documents WHERE path LIKE 'chaos5t/%'").Scan(&postMemCount)
	db.QueryRow("SELECT COUNT(*) FROM traces WHERE id = ANY($1)", pq.Array(traceIDArr)).Scan(&postTraceCount)
	db.QueryRow("SELECT COUNT(*) FROM spans WHERE id = ANY($1)", pq.Array(spanIDArr)).Scan(&postSpanCount)

	if preSessCount != postSessCount {
		t.Errorf("agent_sessions: pre=%d post=%d (rows lost)", preSessCount, postSessCount)
	}
	if preMemCount != postMemCount {
		t.Errorf("memory_documents: pre=%d post=%d (rows lost)", preMemCount, postMemCount)
	}
	if preTraceCount != postTraceCount {
		t.Errorf("traces: pre=%d post=%d (rows lost)", preTraceCount, postTraceCount)
	}
	if preSpanCount != postSpanCount {
		t.Errorf("spans: pre=%d post=%d (rows lost)", preSpanCount, postSpanCount)
	}
}

// ── Case D: target already merged ────────────────────────────────────────────

// TestMergeAtomic5Table_TargetAlreadyMerged asserts ErrMergeTargetAlreadyMerged
// when the target has contacts already merged into a different user. No partial writes.
func TestMergeAtomic5Table_TargetAlreadyMerged(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	suf := uuid.New().String()[:8]

	_, agentID := seedTenantAgent(t, db)
	sourceUser := seedMergeUserM(t, db, "dam-src-"+suf)
	targetUser := seedMergeUserM(t, db, "dam-tgt-"+suf)
	thirdUser := seedMergeUserM(t, db, "dam-trd-"+suf)

	sourceContact := seedContactLinked(t, db, sourceUser, "dam-src-"+suf)
	// targetContact belongs to targetUser but is merged into thirdUser,
	// making targetUser "already merged" from the chained-merge perspective.
	targetContact := seedContactLinked(t, db, targetUser, "dam-tgt-"+suf)

	contacts := pg.NewPGContactStore(db)
	// First: merge targetContact → thirdUser to set up chained condition.
	if err := contacts.MergeUserAggregate(ctx, store.MergeUserAggregateRequest{
		ContactIDs:    []uuid.UUID{targetContact},
		SourceUserIDs: []uuid.UUID{targetUser},
		TargetUserID:  thirdUser,
	}); err != nil {
		t.Fatalf("pre-merge setup: %v", err)
	}

	sessKey := seedMergeSession(t, db, agentID, sourceUser, "dam-"+suf)

	// Attempt merge into the already-merged target — must reject.
	err := contacts.MergeUserAggregate(ctx, store.MergeUserAggregateRequest{
		ContactIDs:    []uuid.UUID{sourceContact},
		SourceUserIDs: []uuid.UUID{sourceUser},
		TargetUserID:  targetUser,
	})
	if !errors.Is(err, store.ErrMergeTargetAlreadyMerged) {
		t.Errorf("expected ErrMergeTargetAlreadyMerged, got: %v", err)
	}

	// sourceContact must not have been merged.
	n := countWhere5T(t, db, "channel_contacts", "id = $1 AND merged_id IS NULL", sourceContact)
	if n != 1 {
		t.Errorf("sourceContact was merged despite expected rollback")
	}

	// agent_sessions must still point to sourceUser.
	n = countWhere5T(t, db, "agent_sessions", "session_key = $1 AND user_id = $2", sessKey, sourceUser)
	if n != 1 {
		t.Errorf("agent_sessions was mutated despite expected rollback")
	}
}

// ── Case E: ContactIDs containing nonexistent UUIDs ──────────────────────────

// TestMergeAtomic5Table_ContactIDNotFound verifies that when req.ContactIDs
// contains UUIDs absent from the DB, the merge is rejected with ErrContactIDNotFound
// and no UPDATEs are run on any table.
func TestMergeAtomic5Table_ContactIDNotFound(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	suf := uuid.New().String()[:8]

	_, agentID := seedTenantAgent(t, db)
	sourceUser := seedMergeUserM(t, db, "cnf-src-"+suf)
	targetUser := seedMergeUserM(t, db, "cnf-tgt-"+suf)

	realContact := seedContactLinked(t, db, sourceUser, "cnf-"+suf)
	fabricatedContact := uuid.New() // not in DB

	sessKey := seedMergeSession(t, db, agentID, sourceUser, "cnf-"+suf)
	_ = seedMergeConfigPerm(t, db, agentID, sourceUser.String(), "scope:cnf-"+suf, "write_file")

	contacts := pg.NewPGContactStore(db)
	err := contacts.MergeUserAggregate(ctx, store.MergeUserAggregateRequest{
		ContactIDs:    []uuid.UUID{realContact, fabricatedContact},
		SourceUserIDs: []uuid.UUID{sourceUser},
		TargetUserID:  targetUser,
	})
	if !errors.Is(err, store.ErrContactIDNotFound) {
		t.Errorf("expected ErrContactIDNotFound, got: %v", err)
	}

	// realContact must not have merged_id set.
	n := countWhere5T(t, db, "channel_contacts", "id = $1 AND merged_id IS NULL", realContact)
	if n != 1 {
		t.Errorf("channel_contacts: realContact was partially merged despite ErrContactIDNotFound")
	}

	// agent_sessions must still belong to sourceUser.
	n = countWhere5T(t, db, "agent_sessions", "session_key = $1 AND user_id = $2", sessKey, sourceUser)
	if n != 1 {
		t.Errorf("agent_sessions: user_id was mutated despite ErrContactIDNotFound rollback")
	}

	// agent_config_permissions must still belong to sourceUser.
	n = countWhere5T(t, db, "agent_config_permissions", "user_id = $1", sourceUser.String())
	if n != 1 {
		t.Errorf("agent_config_permissions: source row was mutated despite ErrContactIDNotFound rollback")
	}
}

// ── Chaos slice helpers ───────────────────────────────────────────────────────

func mergePairContactIDs(ps []mergePair) []uuid.UUID {
	out := make([]uuid.UUID, len(ps))
	for i, p := range ps {
		out[i] = p.contactID
	}
	return out
}

func mergePairSourceUserIDs(ps []mergePair) []uuid.UUID {
	out := make([]uuid.UUID, len(ps))
	for i, p := range ps {
		out[i] = p.sourceUser
	}
	return out
}

func mergePairTraceIDs(ps []mergePair) []uuid.UUID {
	out := make([]uuid.UUID, len(ps))
	for i, p := range ps {
		out[i] = p.traceID
	}
	return out
}

func mergePairSpanIDs(ps []mergePair) []uuid.UUID {
	out := make([]uuid.UUID, len(ps))
	for i, p := range ps {
		out[i] = p.spanID
	}
	return out
}
