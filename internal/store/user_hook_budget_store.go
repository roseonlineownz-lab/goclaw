package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// UserHookBudget mirrors a row of `user_hook_budget`.
//
// One row per user (PRIMARY KEY user_id). MonthStart anchors the rolling
// monthly budget; ResetMonthly rolls the month forward and refills Remaining.
type UserHookBudget struct {
	UserID       uuid.UUID       `db:"user_id"`
	MonthStart   time.Time       `db:"month_start"`
	BudgetTotal  int             `db:"budget_total"`
	Remaining    int             `db:"remaining"`
	LastWarnedAt *time.Time      `db:"last_warned_at"`
	Metadata     json.RawMessage `db:"metadata"`
	UpdatedAt    time.Time       `db:"updated_at"`
}

// UserHookBudgetStore manages monthly hook execution budgets per user.
type UserHookBudgetStore interface {
	Get(ctx context.Context, userID uuid.UUID) (*UserHookBudget, error)
	Upsert(ctx context.Context, b *UserHookBudget) error
	Decrement(ctx context.Context, userID uuid.UUID, n int) error
	ResetMonthly(ctx context.Context, userID uuid.UUID, monthStart time.Time, budgetTotal int) error
}
