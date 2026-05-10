package base

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Dialect abstracts SQL differences between PostgreSQL and SQLite.
type Dialect interface {
	// Placeholder returns a positional parameter placeholder.
	// PG: "$1", "$2", ... SQLite: "?", "?", ...
	Placeholder(n int) string
	// TransformValue converts a Go value for the dialect.
	// PG: identity. SQLite: marshals maps/slices to JSON strings.
	TransformValue(v any) any
	// SupportsReturning indicates whether the dialect supports RETURNING clauses.
	SupportsReturning() bool
}

// QueryScope mirrors store.QueryScope without importing store/.
// Callers extract scope from context and convert to this struct.
type QueryScope struct {
	ProjectID *uuid.UUID
}

// BuildMapUpdate builds a dynamic UPDATE query from a column->value map.
// Column names and table name are validated against ValidColumnName to prevent SQL injection.
// Auto-sets updated_at for tables listed in TablesWithUpdatedAt.
//
// Returns: query string, args slice, error.
// The WHERE clause is: WHERE id = <placeholder>.
func BuildMapUpdate(d Dialect, table string, id uuid.UUID, updates map[string]any) (string, []any, error) {
	if len(updates) == 0 {
		return "", nil, nil
	}
	if !ValidColumnName.MatchString(table) {
		return "", nil, fmt.Errorf("invalid table name: %q", table)
	}
	var setClauses []string
	var args []any
	i := 1
	for col, val := range updates {
		if !ValidColumnName.MatchString(col) {
			return "", nil, fmt.Errorf("invalid column name: %q", col)
		}
		setClauses = append(setClauses, fmt.Sprintf("%s = %s", col, d.Placeholder(i)))
		args = append(args, d.TransformValue(val))
		i++
	}
	if _, ok := updates["updated_at"]; !ok && TableHasUpdatedAt(table) {
		setClauses = append(setClauses, fmt.Sprintf("updated_at = %s", d.Placeholder(i)))
		args = append(args, time.Now().UTC())
		i++
	}
	args = append(args, id)
	q := fmt.Sprintf("UPDATE %s SET %s WHERE id = %s",
		table, strings.Join(setClauses, ", "), d.Placeholder(i))
	return q, args, nil
}


// BuildScopeClause is a no-op in v4 (single-tenant). Returns empty clause and
// the same startParam so callers chaining placeholder indices stay consistent.
// Project scoping is preserved.
func BuildScopeClause(d Dialect, scope QueryScope, startParam int) (string, []any, int) {
	if scope.ProjectID != nil {
		return fmt.Sprintf(" AND project_id = %s", d.Placeholder(startParam)),
			[]any{*scope.ProjectID}, startParam + 1
	}
	return "", nil, startParam
}

// BuildScopeClauseAlias is a no-op in v4 unless ProjectID set.
// SECURITY: alias is interpolated — callers MUST pass hardcoded string literals only.
func BuildScopeClauseAlias(d Dialect, scope QueryScope, startParam int, alias string) (string, []any, int) {
	if scope.ProjectID == nil {
		return "", nil, startParam
	}
	for _, c := range alias {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_') {
			return "", nil, startParam
		}
	}
	return fmt.Sprintf(" AND %s.project_id = %s", alias, d.Placeholder(startParam)),
		[]any{*scope.ProjectID}, startParam + 1
}
