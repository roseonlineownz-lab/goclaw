//go:build sqlite || sqliteonly

package sqlitestore

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store/base"
)

// --- Nullable helpers (delegated to base/) ---

var (
	nilStr     = base.NilStr
	nilInt     = base.NilInt
	nilUUID    = base.NilUUID
	nilTime    = base.NilTime
	derefStr   = base.DerefStr
	derefUUID  = base.DerefUUID
	derefBytes = base.DerefBytes
)

// --- JSON helpers (delegated to base/) ---

var (
	jsonOrEmpty      = base.JsonOrEmpty
	jsonOrEmptyArray = base.JsonOrEmptyArray
	jsonOrNull       = base.JsonOrNull
)

// --- Column/table validation (delegated to base/) ---

var validColumnName = base.ValidColumnName
var tableHasUpdatedAt = base.TableHasUpdatedAt

// --- SQLite-specific helpers ---

// uuidPtrStr converts *uuid.UUID to its string representation for SQLite storage.
// Returns nil when the pointer is nil (stores NULL in the DB).
func uuidPtrStr(id *uuid.UUID) any {
	if id == nil {
		return nil
	}
	return id.String()
}

// jsonStringArray converts a Go string slice to a JSON array string for SQLite storage.
// SQLite stores arrays as JSON text (no native array type).
func jsonStringArray(arr []string) string {
	if arr == nil {
		return "[]"
	}
	data, _ := json.Marshal(arr)
	return string(data)
}

// scanJSONStringArray parses a JSON array stored as TEXT into a Go string slice.
func scanJSONStringArray(data []byte, dest *[]string) {
	if data == nil || len(data) == 0 {
		return
	}
	_ = json.Unmarshal(data, dest)
}

// sqliteVal marshals complex Go types (maps, slices) to JSON strings
// since the SQLite driver cannot serialize them directly.
func sqliteVal(v any) any {
	if v == nil {
		return nil
	}
	switch typed := v.(type) {
	case string, int, int64, float64, bool, time.Time, []byte, json.RawMessage:
		return v
	case *time.Time:
		if typed == nil {
			return nil
		}
		return *typed
	}
	// For maps, slices, etc. — marshal to JSON string.
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return string(b)
}

// --- Dynamic UPDATE helpers (using base.BuildMapUpdate) ---

// execMapUpdate builds and runs a dynamic UPDATE with ? placeholders.
func execMapUpdate(ctx context.Context, db *sql.DB, table string, id uuid.UUID, updates map[string]any) error {
	query, args, err := base.BuildMapUpdate(sqliteDialect, table, id, updates)
	if err != nil {
		slog.Warn("security.invalid_column_name", "table", table, "error", err)
		return err
	}
	if query == "" {
		return nil
	}
	_, err = db.ExecContext(ctx, query, args...)
	return err
}
