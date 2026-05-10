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

// PGCuratorRunsStore implements store.CuratorRunsStore on PostgreSQL.
type PGCuratorRunsStore struct {
	db *sql.DB
}

// NewPGCuratorRunsStore returns a CuratorRunsStore backed by Postgres.
func NewPGCuratorRunsStore(db *sql.DB) *PGCuratorRunsStore {
	return &PGCuratorRunsStore{db: db}
}

const curatorRunsSelectColumns = `id, skill_id, status, result, error, triggered_by,
	started_at, finished_at`

// Start inserts a new run with status='running'. ID is populated DB-side.
func (s *PGCuratorRunsStore) Start(ctx context.Context, r *store.CuratorRun) error {
	if r.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("uuid v7: %w", err)
		}
		r.ID = id
	}
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO curator_runs (id, skill_id, status, triggered_by)
		VALUES ($1, $2, 'running', $3)
		RETURNING `+curatorRunsSelectColumns,
		r.ID, r.SkillID, nilStr(deref(r.TriggeredBy)),
	)
	return scanCuratorRun(row, r)
}

// Complete transitions a running run to 'completed'. ErrNotFound if id missing
// or already terminal.
func (s *PGCuratorRunsStore) Complete(ctx context.Context, id uuid.UUID, result json.RawMessage) error {
	if len(result) == 0 {
		result = []byte("{}")
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE curator_runs
		   SET status = 'completed', result = $2, finished_at = now()
		 WHERE id = $1 AND status = 'running'`, id, result)
	if err != nil {
		return fmt.Errorf("curator_runs complete: %w", err)
	}
	return rowsAffectedNotFound(res)
}

// Fail transitions a running run to 'failed' with errMsg.
func (s *PGCuratorRunsStore) Fail(ctx context.Context, id uuid.UUID, errMsg string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE curator_runs
		   SET status = 'failed', error = $2, finished_at = now()
		 WHERE id = $1 AND status = 'running'`, id, errMsg)
	if err != nil {
		return fmt.Errorf("curator_runs fail: %w", err)
	}
	return rowsAffectedNotFound(res)
}

func (s *PGCuratorRunsStore) Get(ctx context.Context, id uuid.UUID) (*store.CuratorRun, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+curatorRunsSelectColumns+` FROM curator_runs WHERE id = $1`, id)
	var r store.CuratorRun
	if err := scanCuratorRun(row, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *PGCuratorRunsStore) ListBySkillID(ctx context.Context, skillID uuid.UUID) ([]store.CuratorRun, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+curatorRunsSelectColumns+`
		   FROM curator_runs
		  WHERE skill_id = $1
		  ORDER BY started_at DESC`, skillID)
	if err != nil {
		return nil, fmt.Errorf("curator_runs list: %w", err)
	}
	defer rows.Close()
	var out []store.CuratorRun
	for rows.Next() {
		var r store.CuratorRun
		if err := scanCuratorRun(rows, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func scanCuratorRun(r rowScanner, run *store.CuratorRun) error {
	var skillID *uuid.UUID
	var result []byte
	var errMsg, triggeredBy *string
	err := r.Scan(
		&run.ID, &skillID, &run.Status, &result, &errMsg, &triggeredBy,
		&run.StartedAt, &run.FinishedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("scan curator_run: %w", err)
	}
	run.SkillID = skillID
	run.Result = result
	run.Error = errMsg
	run.TriggeredBy = triggeredBy
	return nil
}

// rowsAffectedNotFound returns store.ErrNotFound if the UPDATE matched zero rows.
func rowsAffectedNotFound(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}
