//go:build sqlite || sqliteonly

package sqlitestore

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// TestEnsureSchema_FreshDB verifies schema.sql + all migrations apply cleanly on a fresh DB.
func TestEnsureSchema_FreshDB(t *testing.T) {
	db := openTestDB(t)
	if err := EnsureSchema(db); err != nil {
		t.Fatalf("EnsureSchema (fresh) failed: %v", err)
	}

	// Verify schema version matches current
	var version int
	if err := db.QueryRow("SELECT version FROM schema_version LIMIT 1").Scan(&version); err != nil {
		t.Fatalf("read schema_version: %v", err)
	}
	if version != SchemaVersion {
		t.Errorf("schema version = %d, want %d", version, SchemaVersion)
	}

	// Verify vault_documents table has expected columns (team_id, custom_scope, summary)
	rows, err := db.Query("PRAGMA table_info(vault_documents)")
	if err != nil {
		t.Fatalf("PRAGMA table_info: %v", err)
	}
	defer rows.Close()

	cols := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull int
		var dflt *string
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		cols[name] = true
	}
	for _, want := range []string{"team_id", "custom_scope", "summary"} {
		if !cols[want] {
			t.Errorf("vault_documents missing column %q", want)
		}
	}
}

// TestEnsureSchema_IdempotentRerun verifies EnsureSchema can be called twice without error.
func TestEnsureSchema_IdempotentRerun(t *testing.T) {
	db := openTestDB(t)
	if err := EnsureSchema(db); err != nil {
		t.Fatalf("first EnsureSchema: %v", err)
	}
	if err := EnsureSchema(db); err != nil {
		t.Fatalf("second EnsureSchema (idempotent) failed: %v", err)
	}
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := OpenDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
