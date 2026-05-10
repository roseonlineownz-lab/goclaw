package pg

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// PGUserHookBudgetStore implements store.UserHookBudgetStore on PostgreSQL.
type PGUserHookBudgetStore struct {
	db *sql.DB
}

// NewPGUserHookBudgetStore returns a UserHookBudgetStore backed by Postgres.
func NewPGUserHookBudgetStore(db *sql.DB) *PGUserHookBudgetStore {
	return &PGUserHookBudgetStore{db: db}
}

const userHookBudgetColumns = `user_id, month_start, budget_total, remaining,
	last_warned_at, metadata, updated_at`

func (s *PGUserHookBudgetStore) Get(ctx context.Context, userID uuid.UUID) (*store.UserHookBudget, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+userHookBudgetColumns+` FROM user_hook_budget WHERE user_id = $1`, userID)
	var b store.UserHookBudget
	err := row.Scan(
		&b.UserID, &b.MonthStart, &b.BudgetTotal, &b.Remaining,
		&b.LastWarnedAt, &b.Metadata, &b.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan user_hook_budget: %w", err)
	}
	return &b, nil
}

// Upsert creates or updates the per-user row. updated_at is refreshed by the
// query; metadata defaults to '{}' when caller leaves it empty.
func (s *PGUserHookBudgetStore) Upsert(ctx context.Context, b *store.UserHookBudget) error {
	if len(b.Metadata) == 0 {
		b.Metadata = []byte("{}")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO user_hook_budget (user_id, month_start, budget_total, remaining, metadata, updated_at)
		VALUES ($1, $2, $3, $4, $5, now())
		ON CONFLICT (user_id) DO UPDATE
		   SET month_start  = EXCLUDED.month_start,
		       budget_total = EXCLUDED.budget_total,
		       remaining    = EXCLUDED.remaining,
		       metadata     = EXCLUDED.metadata,
		       updated_at   = now()`,
		b.UserID, b.MonthStart, b.BudgetTotal, b.Remaining, b.Metadata,
	)
	if err != nil {
		return fmt.Errorf("user_hook_budget upsert: %w", err)
	}
	return nil
}

// Decrement subtracts n from remaining. ErrNotFound if no row for the user.
// Floor at zero; callers can detect "over budget" via remaining returned by Get.
func (s *PGUserHookBudgetStore) Decrement(ctx context.Context, userID uuid.UUID, n int) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE user_hook_budget
		   SET remaining  = GREATEST(remaining - $2, 0),
		       updated_at = now()
		 WHERE user_id = $1`, userID, n)
	if err != nil {
		return fmt.Errorf("user_hook_budget decrement: %w", err)
	}
	return rowsAffectedNotFound(res)
}

// ResetMonthly rolls month_start forward and refills remaining. Creates the row
// if missing (handles "first activity in new month" case implicitly).
func (s *PGUserHookBudgetStore) ResetMonthly(ctx context.Context, userID uuid.UUID, monthStart time.Time, budgetTotal int) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO user_hook_budget (user_id, month_start, budget_total, remaining, updated_at)
		VALUES ($1, $2, $3, $3, now())
		ON CONFLICT (user_id) DO UPDATE
		   SET month_start    = EXCLUDED.month_start,
		       budget_total   = EXCLUDED.budget_total,
		       remaining      = EXCLUDED.budget_total,
		       last_warned_at = NULL,
		       updated_at     = now()`,
		userID, monthStart, budgetTotal,
	)
	if err != nil {
		return fmt.Errorf("user_hook_budget reset: %w", err)
	}
	return nil
}
