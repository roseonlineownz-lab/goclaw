//go:build sqlite || sqliteonly

package sqlitestore

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

// testEncKey is a 32-byte AES key used for SQLite secure-cli tests.
const testEncKey = "test-key-32-bytes-aaaaaaaaaaaaaaa"

func newTestSQLiteSecureCLI(t *testing.T) (*SQLiteSecureCLIStore, *sql.DB) {
	t.Helper()
	db, err := OpenDB(filepath.Join(t.TempDir(), "secure_cli.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := EnsureSchema(db); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	return NewSQLiteSecureCLIStore(db, testEncKey), db
}

// seedBinary inserts a secure_cli_binaries row with the given fields.
func seedBinary(t *testing.T, db *sql.DB, name string, enabled, isGlobal bool) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO secure_cli_binaries
		  (id, binary_name, encrypted_env, is_global, enabled)
		 VALUES (?, ?, ?, ?, ?)`,
		uuid.New(), name, []byte("{}"), isGlobal, enabled,
	)
	if err != nil {
		t.Fatalf("seed binary %s: %v", name, err)
	}
}

func TestSQLite_IsRegisteredBinary_ReturnsTrueForEnabledNonGlobal(t *testing.T) {
	s, db := newTestSQLiteSecureCLI(t)
	seedBinary(t, db, "gh", true, false)

	got, err := s.IsRegisteredBinary(context.Background(), "gh")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !got {
		t.Fatalf("expected true for enabled non-global binary")
	}
}

// is_global=true must NOT be reported as gate-needing — those binaries are
// open to all agents without a grant.
func TestSQLite_IsRegisteredBinary_FalseForGlobalBinary(t *testing.T) {
	s, db := newTestSQLiteSecureCLI(t)
	seedBinary(t, db, "ls", true, true)

	got, err := s.IsRegisteredBinary(context.Background(), "ls")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got {
		t.Fatalf("expected false for is_global=true binary (would deny access otherwise)")
	}
}

func TestSQLite_IsRegisteredBinary_FalseForDisabled(t *testing.T) {
	s, db := newTestSQLiteSecureCLI(t)
	seedBinary(t, db, "gh", false, false)

	got, err := s.IsRegisteredBinary(context.Background(), "gh")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got {
		t.Fatalf("expected false for disabled binary")
	}
}

func TestSQLite_IsRegisteredBinary_FalseForUnknownName(t *testing.T) {
	s, _ := newTestSQLiteSecureCLI(t)
	got, err := s.IsRegisteredBinary(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got {
		t.Fatalf("expected false for unknown name")
	}
}

func TestSQLite_IsRegisteredBinary_EmptyNameReturnsFalse(t *testing.T) {
	s, _ := newTestSQLiteSecureCLI(t)
	got, err := s.IsRegisteredBinary(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got {
		t.Fatalf("expected false for empty name")
	}
}

func TestSQLite_IsRegisteredBinary_NilContextReturnsFalse(t *testing.T) {
	s, _ := newTestSQLiteSecureCLI(t)
	got, err := s.IsRegisteredBinary(context.Background(), "gh")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got {
		t.Fatalf("expected false when no binary exists")
	}
}

// Case-insensitive match — macOS/APFS resolves GH → gh.
func TestSQLite_IsRegisteredBinary_CaseInsensitive(t *testing.T) {
	s, db := newTestSQLiteSecureCLI(t)
	seedBinary(t, db, "gh", true, false)

	for _, q := range []string{"gh", "GH", "Gh", "  gh  "} {
		got, err := s.IsRegisteredBinary(context.Background(), q)
		if err != nil {
			t.Fatalf("unexpected err for %q: %v", q, err)
		}
		if !got {
			t.Fatalf("expected true for %q (case-insensitive)", q)
		}
	}
}
