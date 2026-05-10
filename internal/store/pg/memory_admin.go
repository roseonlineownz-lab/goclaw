package pg

import (
	"context"
	"fmt"
	"os"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// memoryScopeFromContext builds a MemoryScope from context values.
// Returns nil when no 5D dimensions are active.
func memoryScopeFromContext(ctx context.Context) *store.MemoryScope {
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

// ListAllDocumentsGlobal returns all documents across all agents (for admin overview).
func (s *PGMemoryStore) ListAllDocumentsGlobal(ctx context.Context) ([]store.DocumentInfo, error) {
	var rows []documentInfoRow
	if err := pkgSqlxDB.SelectContext(ctx, &rows,
		`SELECT agent_id, path, content_hash AS hash, user_id, updated_at
		 FROM memory_documents
		 ORDER BY updated_at DESC`); err != nil {
		return nil, err
	}
	result := make([]store.DocumentInfo, len(rows))
	for i := range rows {
		result[i] = rows[i].toDocumentInfo()
	}
	return result, nil
}

// ListAllDocuments returns all documents for an agent across all users (global + personal).
func (s *PGMemoryStore) ListAllDocuments(ctx context.Context, agentID string) ([]store.DocumentInfo, error) {
	aid, err := parseUUID(agentID)
	if err != nil {
		return nil, fmt.Errorf("memory list all documents: %w", err)
	}

	var rows []documentInfoRow
	if err := pkgSqlxDB.SelectContext(ctx, &rows,
		`SELECT agent_id, path, content_hash AS hash, user_id, updated_at
		 FROM memory_documents WHERE agent_id = $1
		 ORDER BY updated_at DESC`, aid); err != nil {
		return nil, err
	}
	result := make([]store.DocumentInfo, len(rows))
	for i := range rows {
		result[i] = rows[i].toDocumentInfo()
	}
	return result, nil
}

// GetDocumentDetail returns full document info with chunk and embedding counts.
// Content is loaded from the FS backing file when available; falls back to empty
// string when the file_path is not set (legacy row or FSWriter not configured).
// Scope is derived from context (team_id, contact_id, project_id) to prevent
// cross-tenant document inspection.
func (s *PGMemoryStore) GetDocumentDetail(ctx context.Context, agentID, userID, path string) (*store.DocumentDetail, error) {
	aid, err := parseUUID(agentID)
	if err != nil {
		return nil, fmt.Errorf("memory get document detail: %w", err)
	}

	q := `SELECT d.path, d.file_path, d.content_hash AS hash, d.user_id, d.created_at, d.updated_at,
			COUNT(c.id) AS chunk_count,
			COUNT(c.embedding) AS embedded_count
		 FROM memory_documents d
		 LEFT JOIN memory_chunks c ON c.document_id = d.id
		 WHERE d.agent_id = $1 AND d.path = $2`
	args := []any{aid, path}
	p := 3

	if userID == "" {
		q += " AND d.user_id IS NULL"
	} else {
		q += fmt.Sprintf(" AND d.user_id = $%d", p)
		args = append(args, userID)
		p++
	}

	scope := memoryScopeFromContext(ctx)
	if scope != nil {
		// Prefix scope filter columns with "d." for the joined query alias.
		if scope.TeamID != nil {
			q += fmt.Sprintf(" AND d.team_id = $%d", p)
			args = append(args, *scope.TeamID)
			p++
		}
		if scope.ContactID != nil {
			q += fmt.Sprintf(" AND d.contact_id = $%d", p)
			args = append(args, *scope.ContactID)
			p++
		}
		if scope.ProjectID != nil {
			q += fmt.Sprintf(" AND d.project_id = $%d", p)
			args = append(args, *scope.ProjectID)
			p++
		}
	}
	_ = p // final p unused after loop
	q += " GROUP BY d.id"

	var row documentDetailRow
	if err := pkgSqlxDB.GetContext(ctx, &row, q, args...); err != nil {
		return nil, err
	}
	detail := row.toDocumentDetail()

	// Load content from FS when a file_path is recorded.
	if row.FilePath != "" {
		if data, readErr := os.ReadFile(row.FilePath); readErr == nil {
			detail.Content = string(data)
		}
	}
	return &detail, nil
}

// ListChunks returns chunks for a document identified by agent, user, and path.
// Scope is derived from context (team_id, contact_id, project_id) to prevent
// cross-tenant chunk inspection.
func (s *PGMemoryStore) ListChunks(ctx context.Context, agentID, userID, path string) ([]store.ChunkInfo, error) {
	aid, err := parseUUID(agentID)
	if err != nil {
		return nil, fmt.Errorf("memory list chunks: %w", err)
	}

	q := `SELECT c.id, c.start_line, c.end_line,
			c.text AS text_preview,
			(c.embedding IS NOT NULL) AS has_embedding
		 FROM memory_chunks c
		 JOIN memory_documents d ON c.document_id = d.id
		 WHERE d.agent_id = $1 AND d.path = $2`
	args := []any{aid, path}
	p := 3

	if userID == "" {
		q += " AND d.user_id IS NULL"
	} else {
		q += fmt.Sprintf(" AND d.user_id = $%d", p)
		args = append(args, userID)
		p++
	}

	scope := memoryScopeFromContext(ctx)
	if scope != nil {
		if scope.TeamID != nil {
			q += fmt.Sprintf(" AND d.team_id = $%d", p)
			args = append(args, *scope.TeamID)
			p++
		}
		if scope.ContactID != nil {
			q += fmt.Sprintf(" AND d.contact_id = $%d", p)
			args = append(args, *scope.ContactID)
			p++
		}
		if scope.ProjectID != nil {
			q += fmt.Sprintf(" AND d.project_id = $%d", p)
			args = append(args, *scope.ProjectID)
			p++
		}
	}
	_ = p
	q += " ORDER BY c.start_line"

	var rows []chunkInfoRow
	if err := pkgSqlxDB.SelectContext(ctx, &rows, q, args...); err != nil {
		return nil, err
	}
	result := make([]store.ChunkInfo, len(rows))
	for i := range rows {
		result[i] = rows[i].toChunkInfo()
	}
	return result, nil
}
