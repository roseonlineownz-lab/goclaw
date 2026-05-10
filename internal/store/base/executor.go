package base

import (
	"context"
	"database/sql"
)

// Executor is the minimal read+write surface that BOTH *sql.DB and *sql.Tx
// satisfy. It exists so cross-store helpers (e.g.
// UsersStore.ChangePasswordAndRevokeSessions) can pass an active *sql.Tx into
// another store's update method without leaking *sql.Tx through every
// interface or duplicating SQL across stores. QueryRowContext is included so
// callers can run UPDATE...RETURNING inside the shared executor (TOCTOU-free
// single-statement compare-and-set, e.g. PasswordResetStore.MarkUsed).
type Executor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}
