//go:build sqlite || sqliteonly

package sqlitestore

import (
	"database/sql"
	"testing"
)

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func tableSQL(t *testing.T, db *sql.DB, name string) string {
	t.Helper()
	var sqlText string
	if err := db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, name,
	).Scan(&sqlText); err != nil {
		t.Fatalf("read table sql for %q: %v", name, err)
	}
	return sqlText
}
