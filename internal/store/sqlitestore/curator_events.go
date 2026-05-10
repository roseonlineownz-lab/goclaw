//go:build sqlite || sqliteonly

package sqlitestore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// SQLiteCuratorEventsStore implements store.CuratorEventsStore on SQLite.
type SQLiteCuratorEventsStore struct {
	db *sql.DB
}

// NewSQLiteCuratorEventsStore returns a CuratorEventsStore backed by SQLite.
func NewSQLiteCuratorEventsStore(db *sql.DB) *SQLiteCuratorEventsStore {
	return &SQLiteCuratorEventsStore{db: db}
}

const curatorEventsSelectColumns = `id, run_id, event_type, payload, created_at`

// Append inserts a new curator event. ID is assigned if zero.
func (s *SQLiteCuratorEventsStore) Append(ctx context.Context, e *store.CuratorEvent) error {
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
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO curator_events (id, run_id, event_type, payload)
		VALUES (?, ?, ?, ?)`,
		e.ID, e.RunID, e.EventType, string(payload),
	)
	if err != nil {
		return fmt.Errorf("curator_events insert: %w", err)
	}
	row := s.db.QueryRowContext(ctx,
		`SELECT `+curatorEventsSelectColumns+` FROM curator_events WHERE id = ?`, e.ID)
	return scanSQLiteCuratorEvent(row, e)
}

// ListByRunID returns all events for a run ordered by created_at ASC.
func (s *SQLiteCuratorEventsStore) ListByRunID(ctx context.Context, runID uuid.UUID) ([]store.CuratorEvent, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+curatorEventsSelectColumns+`
		   FROM curator_events
		  WHERE run_id = ?
		  ORDER BY created_at ASC`, runID)
	if err != nil {
		return nil, fmt.Errorf("curator_events list: %w", err)
	}
	defer rows.Close()
	var out []store.CuratorEvent
	for rows.Next() {
		var e store.CuratorEvent
		if err := scanSQLiteCuratorEventRow(rows, &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func scanSQLiteCuratorEvent(row *sql.Row, e *store.CuratorEvent) error {
	var payload []byte
	var createdAt sqliteTime
	err := row.Scan(&e.ID, &e.RunID, &e.EventType, &payload, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("scan curator_event: %w", err)
	}
	e.Payload = payload
	e.CreatedAt = createdAt.Time
	return nil
}

func scanSQLiteCuratorEventRow(r sqliteRowScanner, e *store.CuratorEvent) error {
	var payload []byte
	var createdAt sqliteTime
	err := r.Scan(&e.ID, &e.RunID, &e.EventType, &payload, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("scan curator_event: %w", err)
	}
	e.Payload = payload
	e.CreatedAt = createdAt.Time
	return nil
}
