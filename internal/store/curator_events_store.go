package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// CuratorEvent mirrors a row of `curator_events`.
// Events are append-only structured logs attached to a curator run.
type CuratorEvent struct {
	ID        uuid.UUID       `db:"id"`
	RunID     uuid.UUID       `db:"run_id"`
	EventType string          `db:"event_type"`
	Payload   json.RawMessage `db:"payload"`
	CreatedAt time.Time       `db:"created_at"`
}

// CuratorEventsStore manages curator run event logs.
type CuratorEventsStore interface {
	Append(ctx context.Context, e *CuratorEvent) error
	ListByRunID(ctx context.Context, runID uuid.UUID) ([]CuratorEvent, error)
}
