//go:build sqlite || sqliteonly

package sqlitestore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// SQLiteCuratorRunsStore implements store.CuratorRunsStore on SQLite.
type SQLiteCuratorRunsStore struct {
	db *sql.DB
}

// NewSQLiteCuratorRunsStore returns a CuratorRunsStore backed by SQLite.
func NewSQLiteCuratorRunsStore(db *sql.DB) *SQLiteCuratorRunsStore {
	return &SQLiteCuratorRunsStore{db: db}
}

const curatorRunsSelectColumns = `id, skill_id, status, result, error, triggered_by,
	started_at, finished_at`

func (s *SQLiteCuratorRunsStore) Start(ctx context.Context, r *store.CuratorRun) error {
	if r.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("uuid v7: %w", err)
		}
		r.ID = id
	}
	if r.StartedAt.IsZero() {
		r.StartedAt = time.Now().UTC()
	}
	var skillID any
	if r.SkillID != nil {
		skillID = *r.SkillID
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO curator_runs (id, skill_id, status, triggered_by, started_at)
		VALUES (?, ?, 'running', ?, ?)`,
		r.ID, skillID, nilStr(deref(r.TriggeredBy)),
		r.StartedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("curator_runs insert: %w", err)
	}
	r.Status = "running"
	return nil
}

func (s *SQLiteCuratorRunsStore) Complete(ctx context.Context, id uuid.UUID, result json.RawMessage) error {
	if len(result) == 0 {
		result = []byte("{}")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx, `
		UPDATE curator_runs
		   SET status = 'completed', result = ?, finished_at = ?
		 WHERE id = ? AND status = 'running'`,
		string(result), now, id)
	if err != nil {
		return fmt.Errorf("curator_runs complete: %w", err)
	}
	return rowsAffectedNotFoundSQLite(res)
}

func (s *SQLiteCuratorRunsStore) Fail(ctx context.Context, id uuid.UUID, errMsg string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx, `
		UPDATE curator_runs
		   SET status = 'failed', error = ?, finished_at = ?
		 WHERE id = ? AND status = 'running'`, errMsg, now, id)
	if err != nil {
		return fmt.Errorf("curator_runs fail: %w", err)
	}
	return rowsAffectedNotFoundSQLite(res)
}

func (s *SQLiteCuratorRunsStore) Get(ctx context.Context, id uuid.UUID) (*store.CuratorRun, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+curatorRunsSelectColumns+` FROM curator_runs WHERE id = ?`, id)
	return scanSQLiteCuratorRun(row)
}

func (s *SQLiteCuratorRunsStore) ListBySkillID(ctx context.Context, skillID uuid.UUID) ([]store.CuratorRun, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+curatorRunsSelectColumns+`
		   FROM curator_runs
		  WHERE skill_id = ?
		  ORDER BY started_at DESC`, skillID)
	if err != nil {
		return nil, fmt.Errorf("curator_runs list: %w", err)
	}
	defer rows.Close()
	var out []store.CuratorRun
	for rows.Next() {
		r, err := scanSQLiteCuratorRunRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func scanSQLiteCuratorRun(row *sql.Row) (*store.CuratorRun, error) {
	return scanSQLiteCuratorRunRow(row)
}

func scanSQLiteCuratorRunRow(r sqliteRowScanner) (*store.CuratorRun, error) {
	var run store.CuratorRun
	var skillID sql.NullString
	var result []byte
	var errMsg, triggeredBy *string
	var startedAt sqliteTime
	var finishedAt nullSqliteTime
	err := r.Scan(
		&run.ID, &skillID, &run.Status, &result, &errMsg, &triggeredBy,
		&startedAt, &finishedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan curator_run: %w", err)
	}
	if skillID.Valid {
		id, parseErr := uuid.Parse(skillID.String)
		if parseErr != nil {
			return nil, fmt.Errorf("parse skill_id: %w", parseErr)
		}
		run.SkillID = &id
	}
	run.Result = result
	run.Error = errMsg
	run.TriggeredBy = triggeredBy
	run.StartedAt = startedAt.Time
	if finishedAt.Valid {
		t := finishedAt.Time
		run.FinishedAt = &t
	}
	return &run, nil
}

func rowsAffectedNotFoundSQLite(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

