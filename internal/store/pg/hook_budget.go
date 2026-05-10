package pg

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/hooks/budget"
)

// PGHookBudget implements budget.Dialect over PostgreSQL.
// Uses a single atomic UPDATE with RETURNING for the hot path and an
// UPSERT fallback for first-of-month seeding. No select-then-update race possible.
// In v4 single-tenant mode the userID parameter is the per-user key.
type PGHookBudget struct {
	db *sql.DB
}

// NewPGHookBudget returns a PGHookBudget over db.
func NewPGHookBudget(db *sql.DB) *PGHookBudget {
	return &PGHookBudget{db: db}
}

// DeductAtomic implements budget.Dialect.
//
// Path A — current-month row exists and has enough balance:
//
//	UPDATE ... SET remaining = remaining - $1 WHERE user=$2 AND month=$3 AND remaining >= $1 RETURNING remaining, budget_total
//	→ one round-trip, no race.
//
// Path B — row missing or month stale: INSERT ... ON CONFLICT DO UPDATE
// with fresh budget_total, then re-run path A.
//
// Path C — row exists but remaining < cost: affected=0 after seed; return
// ErrBudgetExceeded.
func (b *PGHookBudget) DeductAtomic(
	ctx context.Context, userID uuid.UUID, cost int64, month time.Time, defaultBudget int64,
) (int64, int64, error) {
	// First try: direct deduct on an existing row for the current month.
	remaining, total, ok, err := b.tryDeduct(ctx, userID, cost, month)
	if err != nil {
		return 0, 0, err
	}
	if ok {
		return remaining, total, nil
	}

	// Seed-or-rollover: ensure a row exists for (user, month) with a
	// fresh default budget; then retry the deduct.
	if err := b.seedIfStale(ctx, userID, month, defaultBudget); err != nil {
		return 0, 0, err
	}

	remaining, total, ok, err = b.tryDeduct(ctx, userID, cost, month)
	if err != nil {
		return 0, 0, err
	}
	if !ok {
		return 0, 0, budget.ErrBudgetExceeded
	}
	return remaining, total, nil
}

// tryDeduct runs the atomic UPDATE and reports (remaining, total, rowFound).
func (b *PGHookBudget) tryDeduct(
	ctx context.Context, userID uuid.UUID, cost int64, month time.Time,
) (int64, int64, bool, error) {
	row := b.db.QueryRowContext(ctx, `
		UPDATE user_hook_budget
		SET remaining = remaining - $1,
		    updated_at = NOW()
		WHERE user_id = $2
		  AND month_start = $3::date
		  AND remaining >= $1
		RETURNING remaining, budget_total`,
		cost, userID, month.Format("2006-01-02"),
	)
	var rem, total int64
	err := row.Scan(&rem, &total)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, false, fmt.Errorf("budget deduct: %w", err)
	}
	return rem, total, true, nil
}

// seedIfStale UPSERTs the per-user row for month, resetting remaining to
// defaultBudget when month_start is older than month (rollover) OR the row
// is missing.
func (b *PGHookBudget) seedIfStale(
	ctx context.Context, userID uuid.UUID, month time.Time, defaultBudget int64,
) error {
	_, err := b.db.ExecContext(ctx, `
		INSERT INTO user_hook_budget
		  (user_id, month_start, budget_total, remaining, metadata, updated_at)
		VALUES ($1, $2::date, $3, $3, '{}', NOW())
		ON CONFLICT (user_id) DO UPDATE
		SET month_start = EXCLUDED.month_start,
		    budget_total = EXCLUDED.budget_total,
		    remaining = EXCLUDED.remaining,
		    updated_at = NOW()
		WHERE user_hook_budget.month_start < EXCLUDED.month_start`,
		userID, month.Format("2006-01-02"), defaultBudget,
	)
	if err != nil {
		return fmt.Errorf("budget seed: %w", err)
	}
	return nil
}
