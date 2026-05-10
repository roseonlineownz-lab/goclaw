package pg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// PGCuratorEventsStore implements store.CuratorEventsStore on PostgreSQL.
type PGCuratorEventsStore struct {
	db *sql.DB
}

// NewPGCuratorEventsStore returns a CuratorEventsStore backed by Postgres.
func NewPGCuratorEventsStore(db *sql.DB) *PGCuratorEventsStore {
	return &PGCuratorEventsStore{db: db}
}

const curatorEventsSelectColumns = `id, run_id, event_type, payload, created_at`

// Append inserts a new curator event. ID is assigned if zero.
func (s *PGCuratorEventsStore) Append(ctx context.Context, e *store.CuratorEvent) error {
	if e.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("uuid v7: %w", err)
		}
		e.ID = id
	}
	payload := e.Payload
	if len(payload) == 0 {
		payload = json.RawMessage("{}")
	}
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO curator_events (id, run_id, event_type, payload)
		VALUES ($1, $2, $3, $4)
		RETURNING `+curatorEventsSelectColumns,
		e.ID, e.RunID, e.EventType, payload,
	)
	return scanCuratorEvent(row, e)
}

// ListByRunID returns all events for a run ordered by created_at ASC.
func (s *PGCuratorEventsStore) ListByRunID(ctx context.Context, runID uuid.UUID) ([]store.CuratorEvent, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+curatorEventsSelectColumns+`
		   FROM curator_events
		  WHERE run_id = $1
		  ORDER BY created_at ASC`, runID)
	if err != nil {
		return nil, fmt.Errorf("curator_events list: %w", err)
	}
	defer rows.Close()
	var out []store.CuratorEvent
	for rows.Next() {
		var e store.CuratorEvent
		if err := scanCuratorEvent(rows, &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func scanCuratorEvent(r rowScanner, e *store.CuratorEvent) error {
	var payload []byte
	err := r.Scan(&e.ID, &e.RunID, &e.EventType, &payload, &e.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("scan curator_event: %w", err)
	}
	e.Payload = payload
	return nil
}
