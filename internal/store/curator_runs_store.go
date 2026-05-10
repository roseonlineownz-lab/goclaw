package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// CuratorRun mirrors a row of `curator_runs`.
//
// Lifecycle: Start (status='running', started_at) → Complete (status='completed',
// finished_at, result) OR Fail (status='failed', finished_at, error). Status
// transitions are enforced at the store layer: only running rows can advance.
type CuratorRun struct {
	ID          uuid.UUID       `db:"id"`
	SkillID     *uuid.UUID      `db:"skill_id"`
	Status      string          `db:"status"`
	Result      json.RawMessage `db:"result"`
	Error       *string         `db:"error"`
	TriggeredBy *string         `db:"triggered_by"`
	StartedAt   time.Time       `db:"started_at"`
	FinishedAt  *time.Time      `db:"finished_at"`
}

// CuratorRunsStore tracks skill curator runs (lint/compile/publish).
type CuratorRunsStore interface {
	Start(ctx context.Context, r *CuratorRun) error
	Complete(ctx context.Context, id uuid.UUID, result json.RawMessage) error
	Fail(ctx context.Context, id uuid.UUID, errMsg string) error
	Get(ctx context.Context, id uuid.UUID) (*CuratorRun, error)
	ListBySkillID(ctx context.Context, skillID uuid.UUID) ([]CuratorRun, error)
}
