package consolidation

import (
	"context"
	"database/sql"
	"log/slog"
	"time"
)

// LitePruner enforces a per-agent row cap on memory tables using FIFO
// eviction (oldest created_at first). It is used by the SQLite/desktop
// edition to prevent unbounded linear-scan latency growth.
//
// Strategy: FIFO only in this release. Importance-weighted eviction is
// deferred to a post-rc1 iteration once usage data is available.
type LitePruner struct {
	db     *sql.DB
	cap    int      // max rows per agent per table; 0 = unlimited
	tables []string // table names to prune on each hourly run
}

// NewLitePruner creates a LitePruner for the given database and per-agent row
// cap. tables lists the table names that carry per-agent embedding rows.
func NewLitePruner(db *sql.DB, cap int, tables []string) *LitePruner {
	return &LitePruner{db: db, cap: cap, tables: tables}
}

// PruneAgent deletes the oldest rows from table for agentID until the row
// count is at or below the cap. Uses a single DELETE with a subquery to avoid
// loading IDs into Go memory.
func (p *LitePruner) PruneAgent(ctx context.Context, agentID, table string) error {
	if p.cap <= 0 {
		return nil // unlimited
	}

	var count int
	if err := p.db.QueryRowContext(ctx,
		"SELECT count(*) FROM "+table+" WHERE agent_id = ?", agentID,
	).Scan(&count); err != nil {
		return err
	}
	if count <= p.cap {
		return nil // under cap, nothing to do
	}

	excess := count - p.cap
	_, err := p.db.ExecContext(ctx,
		"DELETE FROM "+table+" WHERE id IN (SELECT id FROM "+table+
			" WHERE agent_id = ? ORDER BY created_at ASC LIMIT ?)",
		agentID, excess,
	)
	if err != nil {
		return err
	}
	slog.Debug("lite pruner: pruned rows", "agent_id", agentID, "table", table,
		"deleted", excess, "cap", p.cap)
	return nil
}

// UsageRatio returns the current row count, configured cap, and the ratio
// used/cap for the given agent across all tracked tables (sum of rows).
// ratio >= 0.80 is the threshold at which a UI warning should be shown.
// Returns (0, cap, 0, nil) when no rows exist.
func (p *LitePruner) UsageRatio(ctx context.Context, agentID string) (used, cap int, ratio float64, err error) {
	cap = p.cap
	if cap <= 0 {
		return 0, 0, 0, nil
	}
	var total int
	for _, table := range p.tables {
		var n int
		if scanErr := p.db.QueryRowContext(ctx,
			"SELECT count(*) FROM "+table+" WHERE agent_id = ?", agentID,
		).Scan(&n); scanErr != nil {
			continue
		}
		total += n
	}
	used = total
	if cap > 0 {
		ratio = float64(used) / float64(cap)
	}
	return used, cap, ratio, nil
}

// RunHourly starts a blocking loop that prunes all agents in all tables every
// hour. It sleeps 50ms between agents to avoid lock contention on busy
// desktops. Cancel ctx to stop.
func (p *LitePruner) RunHourly(ctx context.Context) {
	if p.cap <= 0 {
		return // nothing to enforce
	}
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()

	// Run immediately on start, then hourly.
	p.pruneAll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.pruneAll(ctx)
		}
	}
}

// pruneAll iterates all distinct agent_ids in each table and prunes each.
func (p *LitePruner) pruneAll(ctx context.Context) {
	for _, table := range p.tables {
		rows, err := p.db.QueryContext(ctx,
			"SELECT DISTINCT agent_id FROM "+table)
		if err != nil {
			slog.Warn("lite pruner: list agents failed", "table", table, "error", err)
			continue
		}
		var agents []string
		for rows.Next() {
			var aid string
			if rows.Scan(&aid) == nil {
				agents = append(agents, aid)
			}
		}
		rows.Close()

		for _, aid := range agents {
			select {
			case <-ctx.Done():
				return
			default:
			}
			if err := p.PruneAgent(ctx, aid, table); err != nil {
				slog.Warn("lite pruner: prune agent failed",
					"agent_id", aid, "table", table, "error", err)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
}
