//go:build sqlite || sqliteonly

package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// SQLiteUserHookBudgetStore implements store.UserHookBudgetStore on SQLite.
type SQLiteUserHookBudgetStore struct {
	db *sql.DB
}

// NewSQLiteUserHookBudgetStore returns a UserHookBudgetStore backed by SQLite.
func NewSQLiteUserHookBudgetStore(db *sql.DB) *SQLiteUserHookBudgetStore {
	return &SQLiteUserHookBudgetStore{db: db}
}

const userHookBudgetColumns = `user_id, month_start, budget_total, remaining,
	last_warned_at, metadata, updated_at`

func (s *SQLiteUserHookBudgetStore) Get(ctx context.Context, userID uuid.UUID) (*store.UserHookBudget, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+userHookBudgetColumns+` FROM user_hook_budget WHERE user_id = ?`, userID)
	var b store.UserHookBudget
	var monthStart string
	var lastWarnedAt nullSqliteTime
	var metadata []byte
	var updatedAt sqliteTime
	err := row.Scan(
		&b.UserID, &monthStart, &b.BudgetTotal, &b.Remaining,
		&lastWarnedAt, &metadata, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan user_hook_budget: %w", err)
	}
	t, err := time.Parse("2006-01-02", monthStart)
	if err != nil {
		return nil, fmt.Errorf("parse month_start %q: %w", monthStart, err)
	}
	b.MonthStart = t
	if lastWarnedAt.Valid {
		x := lastWarnedAt.Time
		b.LastWarnedAt = &x
	}
	b.Metadata = metadata
	b.UpdatedAt = updatedAt.Time
	return &b, nil
}

func (s *SQLiteUserHookBudgetStore) Upsert(ctx context.Context, b *store.UserHookBudget) error {
	if len(b.Metadata) == 0 {
		b.Metadata = []byte("{}")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO user_hook_budget (user_id, month_start, budget_total, remaining, metadata, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE
		   SET month_start  = excluded.month_start,
		       budget_total = excluded.budget_total,
		       remaining    = excluded.remaining,
		       metadata     = excluded.metadata,
		       updated_at   = excluded.updated_at`,
		b.UserID, b.MonthStart.UTC().Format("2006-01-02"),
		b.BudgetTotal, b.Remaining, string(b.Metadata), now,
	)
	if err != nil {
		return fmt.Errorf("user_hook_budget upsert: %w", err)
	}
	return nil
}

func (s *SQLiteUserHookBudgetStore) Decrement(ctx context.Context, userID uuid.UUID, n int) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx, `
		UPDATE user_hook_budget
		   SET remaining  = MAX(remaining - ?, 0),
		       updated_at = ?
		 WHERE user_id = ?`, n, now, userID)
	if err != nil {
		return fmt.Errorf("user_hook_budget decrement: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *SQLiteUserHookBudgetStore) ResetMonthly(ctx context.Context, userID uuid.UUID, monthStart time.Time, budgetTotal int) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO user_hook_budget (user_id, month_start, budget_total, remaining, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE
		   SET month_start    = excluded.month_start,
		       budget_total   = excluded.budget_total,
		       remaining      = excluded.budget_total,
		       last_warned_at = NULL,
		       updated_at     = excluded.updated_at`,
		userID, monthStart.UTC().Format("2006-01-02"), budgetTotal, budgetTotal, now,
	)
	if err != nil {
		return fmt.Errorf("user_hook_budget reset: %w", err)
	}
	return nil
}
