package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// QueryScope represents the per-query isolation scope. v4 is single-tenant;
// only ProjectID is used by clause builders.
type QueryScope struct {
	ProjectID *uuid.UUID // nil = no project filter
}

// ScopeFromContext returns an empty scope; v4 has no tenant fail-closed.
// Project scoping (when introduced) will populate ProjectID here.
func ScopeFromContext(ctx context.Context) (QueryScope, error) {
	_ = ctx
	return QueryScope{}, nil
}

// WhereClause generates SQL WHERE conditions for the scope.
// v4: tenant clause omitted; only project clause emitted when set.
func (s QueryScope) WhereClause(startParam int) (clause string, args []any, nextParam int) {
	if s.ProjectID != nil {
		return fmt.Sprintf(" AND project_id = $%d", startParam),
			[]any{*s.ProjectID}, startParam + 1
	}
	return "", nil, startParam
}

// WhereClauseAlias generates SQL WHERE conditions qualified with a table alias.
// SECURITY: alias is interpolated — callers MUST pass hardcoded string literals only.
func (s QueryScope) WhereClauseAlias(startParam int, alias string) (clause string, args []any, nextParam int) {
	if s.ProjectID == nil {
		return "", nil, startParam
	}
	for _, c := range alias {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_') {
			return "", nil, startParam
		}
	}
	return fmt.Sprintf(" AND %s.project_id = $%d", alias, startParam),
		[]any{*s.ProjectID}, startParam + 1
}

// InsertValues returns column names and values for INSERT operations.
// v4: only project_id is inserted when set.
func (s QueryScope) InsertValues() (columns []string, values []any) {
	if s.ProjectID != nil {
		return []string{"project_id"}, []any{*s.ProjectID}
	}
	return nil, nil
}
