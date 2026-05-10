//go:build sqlite || sqliteonly

package sqlitestore

import (
	"context"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// appendSQLiteMemoryScopeFilter appends 5D scope WHERE clauses for SQLite memory_chunks queries.
// Each active dimension uses exact-match; nil fields are skipped.
// SQLite uses ? placeholders; p tracks positional index (unused for ? but returned for consistency).
func appendSQLiteMemoryScopeFilter(q string, args []any, scope *store.MemoryScope) (string, []any) {
	if scope == nil {
		return q, args
	}
	if scope.TeamID != nil {
		q += " AND team_id = ?"
		args = append(args, scope.TeamID.String())
	}
	if scope.ContactID != nil {
		q += " AND contact_id = ?"
		args = append(args, scope.ContactID.String())
	}
	if scope.ProjectID != nil {
		q += " AND project_id = ?"
		args = append(args, scope.ProjectID.String())
	}
	return q, args
}

// sqliteMemoryScopeFromContext extracts the 5D scope from context for SQLite queries.
// Returns nil when no dimensions are active.
func sqliteMemoryScopeFromContext(ctx context.Context) *store.MemoryScope {
	teamID := store.TeamIDFromContext(ctx)
	contactID := store.ContactIDFromContext(ctx)
	projectID := store.ProjectIDFromContext(ctx)
	if teamID == uuid.Nil && contactID == uuid.Nil && projectID == uuid.Nil {
		return nil
	}
	scope := &store.MemoryScope{}
	if teamID != uuid.Nil {
		scope.TeamID = &teamID
	}
	if contactID != uuid.Nil {
		scope.ContactID = &contactID
	}
	if projectID != uuid.Nil {
		scope.ProjectID = &projectID
	}
	return scope
}

// Search performs hybrid memory search over memory_chunks.
// When an embedding provider is configured, it attempts cosine-similarity
// search via in-memory linear scan over the halfvec BLOB column.
// Falls back to LIKE-based text search when no provider is set or embedding
// fails (e.g., provider unavailable, chunk not yet embedded).
func (s *SQLiteMemoryStore) Search(ctx context.Context, query string, agentID, userID string, opts store.MemorySearchOptions) ([]store.MemorySearchResult, error) {
	maxResults := opts.MaxResults
	if maxResults <= 0 {
		maxResults = s.cfg.MaxResults
	}

	scope := sqliteMemoryScopeFromContext(ctx)

	// Attempt vector search when provider is configured.
	if s.provider != nil {
		if vecResults, err := s.vectorSearch(ctx, query, agentID, userID, maxResults, opts, scope); err == nil {
			return vecResults, nil
		} else {
			slog.Debug("sqlite memory: vector search failed, falling back to text", "error", err)
		}
	}

	results, err := s.likeSearch(ctx, query, agentID, userID, maxResults*2, scope)
	if err != nil {
		return nil, err
	}

	// Apply filters and cap results.
	var filtered []store.MemorySearchResult
	for _, r := range results {
		if opts.MinScore > 0 && r.Score < opts.MinScore {
			continue
		}
		if opts.PathPrefix != "" && !strings.HasPrefix(r.Path, opts.PathPrefix) {
			continue
		}
		filtered = append(filtered, r)
		if len(filtered) >= maxResults {
			break
		}
	}
	return filtered, nil
}

// vectorSearch embeds the query string and runs CosineTopK, then looks up
// chunk metadata for each hit.
func (s *SQLiteMemoryStore) vectorSearch(ctx context.Context, query, agentID, userID string, maxResults int, opts store.MemorySearchOptions, scope *store.MemoryScope) ([]store.MemorySearchResult, error) {
	embs, err := s.provider.Embed(ctx, []string{query})
	if err != nil {
		return nil, err
	}
	if len(embs) == 0 || embs[0] == nil {
		return nil, ErrZeroNorm // no embedding returned
	}

	var scopeClause string
	var args []any
	if userID != "" {
		scopeClause = "agent_id = ? AND (user_id IS NULL OR user_id = ?)"
		args = []any{agentID, userID}
	} else {
		scopeClause = "agent_id = ? AND user_id IS NULL"
		args = []any{agentID}
	}
	// Append 5D scope clauses to the CosineTopK filter.
	if scope != nil {
		var extra string
		if scope.TeamID != nil {
			extra += " AND team_id = ?"
			args = append(args, scope.TeamID.String())
		}
		if scope.ContactID != nil {
			extra += " AND contact_id = ?"
			args = append(args, scope.ContactID.String())
		}
		if scope.ProjectID != nil {
			extra += " AND project_id = ?"
			args = append(args, scope.ProjectID.String())
		}
		scopeClause += extra
	}

	hits, err := CosineTopK(ctx, s.db, "memory_chunks",
		ScopeFilter{Clause: scopeClause, Args: args},
		embs[0], maxResults*2)
	if err != nil {
		return nil, err
	}

	// Fetch metadata for each hit.
	var results []store.MemorySearchResult
	for _, h := range hits {
		var path, text string
		var startLine, endLine int
		var uid *string
		if scanErr := s.db.QueryRowContext(ctx,
			`SELECT path, start_line, end_line, text, user_id FROM memory_chunks WHERE id = ?`, h.ID,
		).Scan(&path, &startLine, &endLine, &text, &uid); scanErr != nil {
			continue
		}
		if opts.PathPrefix != "" && !strings.HasPrefix(path, opts.PathPrefix) {
			continue
		}
		score := float64(1 - h.Distance) // cosine similarity
		if opts.MinScore > 0 && score < opts.MinScore {
			continue
		}
		scopeLabel := "global"
		if uid != nil && *uid != "" {
			scopeLabel = "personal"
			score *= 1.2 // personal boost
		}
		results = append(results, store.MemorySearchResult{
			Path:      path,
			StartLine: startLine,
			EndLine:   endLine,
			Score:     score,
			Snippet:   text,
			Source:    "memory",
			Scope:     scopeLabel,
		})
		if len(results) >= maxResults {
			break
		}
	}
	return results, nil
}

// likeSearch performs a case-insensitive LIKE search across chunk text.
// Returns results scored 1.0 (global) or 1.2 (personal, boosted).
func (s *SQLiteMemoryStore) likeSearch(ctx context.Context, query, agentID, userID string, limit int, scope *store.MemoryScope) ([]store.MemorySearchResult, error) {
	pattern := "%" + escapeLike(query) + "%"

	q := `SELECT path, start_line, end_line, text, user_id FROM memory_chunks WHERE agent_id = ?`
	args := []any{agentID}

	if userID != "" {
		q += " AND (user_id IS NULL OR user_id = ?)"
		args = append(args, userID)
	} else {
		q += " AND user_id IS NULL"
	}

	q, args = appendSQLiteMemoryScopeFilter(q, args, scope)
	q += " AND text LIKE ? ESCAPE '\\' ORDER BY user_id DESC LIMIT ?"
	args = append(args, pattern, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []store.MemorySearchResult
	for rows.Next() {
		var path, text string
		var startLine, endLine int
		var uid *string
		if err := rows.Scan(&path, &startLine, &endLine, &text, &uid); err != nil {
			continue
		}
		scopeLabel := "global"
		score := 1.0
		if uid != nil && *uid != "" {
			scopeLabel = "personal"
			score = 1.2 // personal boost, mirrors PG hybrid merge
		}
		results = append(results, store.MemorySearchResult{
			Path:      path,
			StartLine: startLine,
			EndLine:   endLine,
			Score:     score,
			Snippet:   text,
			Source:    "memory",
			Scope:     scopeLabel,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

// escapeLike escapes special LIKE metacharacters: % _ \
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}
