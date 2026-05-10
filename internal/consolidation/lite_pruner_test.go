package consolidation

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// openPrunerDB opens a fresh in-memory SQLite DB with a minimal test table
// that mimics the memory_chunks schema required by LitePruner.
func openPrunerDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE test_chunks (
		id         TEXT PRIMARY KEY,
		agent_id   TEXT NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// seedRows inserts n rows for agentID with monotonically increasing
// created_at timestamps (1ms apart) so FIFO ordering is deterministic.
func seedRows(t *testing.T, db *sql.DB, table, agentID string, n int) {
	t.Helper()
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range n {
		ts := base.Add(time.Duration(i) * time.Millisecond).Format("2006-01-02T15:04:05.000")
		_, err := db.Exec(
			fmt.Sprintf("INSERT INTO %s (id, agent_id, created_at) VALUES (?, ?, ?)", table),
			fmt.Sprintf("%s-row%03d", agentID, i), agentID, ts,
		)
		if err != nil {
			t.Fatalf("seed row %d: %v", i, err)
		}
	}
}

func countRows(db *sql.DB, table, agentID string) int {
	var n int
	db.QueryRow(fmt.Sprintf("SELECT count(*) FROM %s WHERE agent_id = ?", table), agentID).Scan(&n) //nolint:errcheck
	return n
}

// TestRetentionFIFO: seed 100 rows, prune to cap=50, assert oldest 50 deleted.
func TestRetentionFIFO(t *testing.T) {
	db := openPrunerDB(t)
	seedRows(t, db, "test_chunks", "ag1", 100)

	p := NewLitePruner(db, 50, []string{"test_chunks"})
	if err := p.PruneAgent(context.Background(), "ag1", "test_chunks"); err != nil {
		t.Fatalf("PruneAgent: %v", err)
	}

	if got := countRows(db, "test_chunks", "ag1"); got != 50 {
		t.Errorf("row count after prune: got %d, want 50", got)
	}

	// Oldest rows (row000..row049) must be gone; newest (row050..row099) remain.
	var minID string
	db.QueryRow("SELECT id FROM test_chunks WHERE agent_id = ? ORDER BY created_at ASC LIMIT 1", "ag1").Scan(&minID) //nolint:errcheck
	if minID != "ag1-row050" {
		t.Errorf("oldest remaining row: got %q, want %q", minID, "ag1-row050")
	}
}

// TestPrunerNoOpWhenUnderCap: 30 rows with cap=50 → nothing deleted.
func TestPrunerNoOpWhenUnderCap(t *testing.T) {
	db := openPrunerDB(t)
	seedRows(t, db, "test_chunks", "ag2", 30)

	p := NewLitePruner(db, 50, []string{"test_chunks"})
	if err := p.PruneAgent(context.Background(), "ag2", "test_chunks"); err != nil {
		t.Fatalf("PruneAgent: %v", err)
	}
	if got := countRows(db, "test_chunks", "ag2"); got != 30 {
		t.Errorf("row count after no-op prune: got %d, want 30", got)
	}
}

// TestPrunerScopeIsolation: 60 rows each for agentA and agentB, cap=50 →
// each pruned to 50 independently (no cross-agent deletion).
func TestPrunerScopeIsolation(t *testing.T) {
	db := openPrunerDB(t)
	seedRows(t, db, "test_chunks", "agA", 60)
	seedRows(t, db, "test_chunks", "agB", 60)

	p := NewLitePruner(db, 50, []string{"test_chunks"})
	if err := p.PruneAgent(context.Background(), "agA", "test_chunks"); err != nil {
		t.Fatalf("PruneAgent agA: %v", err)
	}
	if err := p.PruneAgent(context.Background(), "agB", "test_chunks"); err != nil {
		t.Fatalf("PruneAgent agB: %v", err)
	}

	gotA := countRows(db, "test_chunks", "agA")
	gotB := countRows(db, "test_chunks", "agB")
	if gotA != 50 {
		t.Errorf("agA count: got %d, want 50", gotA)
	}
	if gotB != 50 {
		t.Errorf("agB count: got %d, want 50", gotB)
	}
}

// TestUsageRatioReporting: 80 rows with cap=100 → ratio = 0.80.
func TestUsageRatioReporting(t *testing.T) {
	db := openPrunerDB(t)
	seedRows(t, db, "test_chunks", "ag3", 80)

	p := NewLitePruner(db, 100, []string{"test_chunks"})
	used, cap, ratio, err := p.UsageRatio(context.Background(), "ag3")
	if err != nil {
		t.Fatalf("UsageRatio: %v", err)
	}
	if used != 80 {
		t.Errorf("used: got %d, want 80", used)
	}
	if cap != 100 {
		t.Errorf("cap: got %d, want 100", cap)
	}
	const wantRatio = 0.80
	if ratio < wantRatio-0.001 || ratio > wantRatio+0.001 {
		t.Errorf("ratio: got %.4f, want %.4f", ratio, wantRatio)
	}
}

// TestMemoryNcapEnforcement verifies that inserting cap+1 rows and then
// pruning leaves exactly cap rows with the oldest deleted.
func TestMemoryNcapEnforcement(t *testing.T) {
	db := openPrunerDB(t)
	const cap = 10
	seedRows(t, db, "test_chunks", "ag4", cap+1) // 11 rows

	p := NewLitePruner(db, cap, []string{"test_chunks"})
	if err := p.PruneAgent(context.Background(), "ag4", "test_chunks"); err != nil {
		t.Fatalf("PruneAgent: %v", err)
	}
	if got := countRows(db, "test_chunks", "ag4"); got != cap {
		t.Errorf("after prune(cap=%d, inserted=%d): got %d rows, want %d",
			cap, cap+1, got, cap)
	}
	// Oldest row (row000) must be gone.
	var exists int
	db.QueryRow("SELECT count(*) FROM test_chunks WHERE id = ?", "ag4-row000").Scan(&exists) //nolint:errcheck
	if exists != 0 {
		t.Error("row000 (oldest) should have been deleted by FIFO pruner")
	}
}

// TestPrunerUnlimited verifies that cap=0 is treated as unlimited (no pruning).
func TestPrunerUnlimited(t *testing.T) {
	db := openPrunerDB(t)
	seedRows(t, db, "test_chunks", "ag5", 200)

	p := NewLitePruner(db, 0, []string{"test_chunks"})
	if err := p.PruneAgent(context.Background(), "ag5", "test_chunks"); err != nil {
		t.Fatalf("PruneAgent: %v", err)
	}
	if got := countRows(db, "test_chunks", "ag5"); got != 200 {
		t.Errorf("unlimited cap: got %d rows, want 200", got)
	}
}
