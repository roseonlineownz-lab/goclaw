//go:build sqlite || sqliteonly

package sqlitestore

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/memory"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

func (s *SQLiteMemoryStore) GetDocument(ctx context.Context, agentID, userID, path string) (string, error) {
	if s.fsWriter != nil {
		scope := memory.ScopeKey{AgentID: agentID, UserID: userID}
		data, _, err := s.fsWriter.Read(ctx, scope, path)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}

	// Legacy path: no FSWriter — read via file_path column.
	var filePath string
	var err error
	if userID == "" {
		err = s.db.QueryRowContext(ctx,
			"SELECT file_path FROM memory_documents WHERE agent_id = ? AND path = ? AND user_id IS NULL",
			agentID, path).Scan(&filePath)
	} else {
		err = s.db.QueryRowContext(ctx,
			"SELECT file_path FROM memory_documents WHERE agent_id = ? AND path = ? AND user_id = ?",
			agentID, path, userID).Scan(&filePath)
	}
	if err != nil {
		return "", err
	}
	data, readErr := os.ReadFile(filePath)
	if readErr != nil {
		return "", fmt.Errorf("memory get document: read file %s: %w", filePath, readErr)
	}
	return string(data), nil
}

func (s *SQLiteMemoryStore) PutDocument(ctx context.Context, agentID, userID, path, content string) error {
	if s.fsWriter != nil {
		scope := memory.ScopeKey{AgentID: agentID, UserID: userID}
		_, err := s.fsWriter.Write(ctx, scope, path, []byte(content), -1)
		return err
	}

	// Legacy path: direct DB write (no FS backing). file_path empty, content_hash set.
	hash := memory.ContentHash(content)
	id := uuid.Must(uuid.NewV7()).String()
	now := time.Now().UTC()

	var uid *string
	if userID != "" {
		uid = &userID
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO memory_documents (id, agent_id, user_id, path, file_path, content_hash, version, updated_at)
		 VALUES (?, ?, ?, ?, '', ?, 1, ?)
		 ON CONFLICT (agent_id, COALESCE(user_id, ''), path)
		 DO UPDATE SET content_hash = excluded.content_hash,
		               updated_at = excluded.updated_at`,
		id, agentID, uid, path, hash, now,
	)
	return err
}

func (s *SQLiteMemoryStore) DeleteDocument(ctx context.Context, agentID, userID, path string) error {
	var res sql.Result
	var err error

	if userID == "" {
		res, err = s.db.ExecContext(ctx,
			"DELETE FROM memory_documents WHERE agent_id = ? AND path = ? AND user_id IS NULL",
			agentID, path)
	} else {
		res, err = s.db.ExecContext(ctx,
			"DELETE FROM memory_documents WHERE agent_id = ? AND path = ? AND user_id = ?",
			agentID, path, userID)
	}
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("document not found: %s", path)
	}
	return nil
}

func (s *SQLiteMemoryStore) ListDocuments(ctx context.Context, agentID, userID string) ([]store.DocumentInfo, error) {
	var rows *sql.Rows
	var err error

	if userID == "" {
		rows, err = s.db.QueryContext(ctx,
			"SELECT path, content_hash, user_id, updated_at FROM memory_documents WHERE agent_id = ? AND user_id IS NULL",
			agentID)
	} else {
		rows, err = s.db.QueryContext(ctx,
			"SELECT path, content_hash, user_id, updated_at FROM memory_documents WHERE agent_id = ? AND (user_id IS NULL OR user_id = ?)",
			agentID, userID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []store.DocumentInfo
	for rows.Next() {
		var path, hash string
		var uid *string
		var updatedAt time.Time
		if err := rows.Scan(&path, &hash, &uid, &updatedAt); err != nil {
			continue
		}
		result = append(result, scanDocumentRow(path, hash, uid, updatedAt))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// IndexDocument chunks a document, stores chunks, and generates halfvec BLOB
// embeddings when a provider is configured. Without a provider the BLOB
// columns are left NULL and search falls back to LIKE-based text matching.
func (s *SQLiteMemoryStore) IndexDocument(ctx context.Context, agentID, userID, path string) error {
	content, err := s.GetDocument(ctx, agentID, userID, path)
	if err != nil {
		return err
	}

	// Get document ID
	var docID string
	if userID == "" {
		err = s.db.QueryRowContext(ctx,
			"SELECT id FROM memory_documents WHERE agent_id = ? AND path = ? AND user_id IS NULL",
			agentID, path).Scan(&docID)
	} else {
		err = s.db.QueryRowContext(ctx,
			"SELECT id FROM memory_documents WHERE agent_id = ? AND path = ? AND user_id = ?",
			agentID, path, userID).Scan(&docID)
	}
	if err != nil {
		return err
	}

	// Delete old chunks
	if _, delErr := s.db.ExecContext(ctx, "DELETE FROM memory_chunks WHERE document_id = ?", docID); delErr != nil {
		return fmt.Errorf("delete old chunks: %w", delErr)
	}

	// Resolve chunk config: per-agent override → global default
	chunkLen, chunkOverlap := s.chunkConfig()
	if rc := store.RunContextFromCtx(ctx); rc != nil && rc.MemoryCfg != nil {
		if rc.MemoryCfg.MaxChunkLen > 0 {
			chunkLen = rc.MemoryCfg.MaxChunkLen
		}
		if rc.MemoryCfg.ChunkOverlap > 0 {
			chunkOverlap = rc.MemoryCfg.ChunkOverlap
		}
	}

	chunks := memory.ChunkText(content, chunkLen, chunkOverlap)
	if len(chunks) == 0 {
		return nil
	}

	var uid *string
	if userID != "" {
		uid = &userID
	}

	// Batch-embed all chunks if a provider is available.
	var embeddings [][]float32
	if s.provider != nil {
		texts := make([]string, len(chunks))
		for i, tc := range chunks {
			texts[i] = tc.Text
		}
		if embs, embErr := s.provider.Embed(ctx, texts); embErr == nil {
			embeddings = embs
		} else {
			slog.Warn("memory sqlite: embed chunks failed, storing without embedding",
				"path", path, "error", embErr)
		}
	}

	for i, tc := range chunks {
		hash := memory.ContentHash(tc.Text)
		chunkID := uuid.Must(uuid.NewV7()).String()
		now := time.Now().UTC()

		var blob []byte
		var norm *float64
		if i < len(embeddings) && embeddings[i] != nil {
			if b, encErr := EncodeHalfvec3072(embeddings[i]); encErr == nil {
				blob = b
				n := L2Norm(embeddings[i])
				norm = &n
			}
		}

		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO memory_chunks (id, agent_id, document_id, user_id, path, start_line, end_line, hash, text, embedding, embedding_norm, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT DO NOTHING`,
			chunkID, agentID, docID, uid, path, tc.StartLine, tc.EndLine, hash, tc.Text, blob, norm, now,
		); err != nil {
			slog.Warn("memory: insert chunk failed", "path", path, "error", err)
		}
	}
	return nil
}

func (s *SQLiteMemoryStore) IndexAll(ctx context.Context, agentID, userID string) error {
	docs, err := s.ListDocuments(ctx, agentID, userID)
	if err != nil {
		return err
	}
	for _, doc := range docs {
		s.IndexDocument(ctx, agentID, doc.UserID, doc.Path)
	}
	return nil
}

// ListAllDocumentsGlobal returns all documents across all agents (admin overview).
func (s *SQLiteMemoryStore) ListAllDocumentsGlobal(ctx context.Context) ([]store.DocumentInfo, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT agent_id, path, content_hash, user_id, updated_at FROM memory_documents ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []store.DocumentInfo
	for rows.Next() {
		var agentID, path, hash string
		var uid *string
		var updatedAt time.Time
		if err := rows.Scan(&agentID, &path, &hash, &uid, &updatedAt); err != nil {
			continue
		}
		info := scanDocumentRow(path, hash, uid, updatedAt)
		info.AgentID = agentID
		result = append(result, info)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// ListAllDocuments returns all documents for an agent across all users.
func (s *SQLiteMemoryStore) ListAllDocuments(ctx context.Context, agentID string) ([]store.DocumentInfo, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT agent_id, path, content_hash, user_id, updated_at
		 FROM memory_documents WHERE agent_id = ?
		 ORDER BY updated_at DESC`,
		agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []store.DocumentInfo
	for rows.Next() {
		var aID, path, hash string
		var uid *string
		var updatedAt time.Time
		if err := rows.Scan(&aID, &path, &hash, &uid, &updatedAt); err != nil {
			continue
		}
		info := scanDocumentRow(path, hash, uid, updatedAt)
		info.AgentID = aID
		result = append(result, info)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// GetDocumentDetail returns full document info with chunk count.
// EmbeddedCount is always 0 (SQLite edition has no pgvector).
// Content is loaded from the FS backing file when available.
// Scope is derived from context to prevent cross-tenant document inspection.
func (s *SQLiteMemoryStore) GetDocumentDetail(ctx context.Context, agentID, userID, path string) (*store.DocumentDetail, error) {
	q := `SELECT d.path, d.file_path, d.content_hash, d.user_id, d.created_at, d.updated_at,
			COUNT(c.id) AS chunk_count
		 FROM memory_documents d
		 LEFT JOIN memory_chunks c ON c.document_id = d.id
		 WHERE d.agent_id = ? AND d.path = ?`
	args := []any{agentID, path}

	if userID == "" {
		q += " AND d.user_id IS NULL"
	} else {
		q += " AND d.user_id = ?"
		args = append(args, userID)
	}

	// Apply 5D scope from context using d. alias for joined query.
	scope := sqliteMemoryScopeFromContext(ctx)
	if scope != nil {
		if scope.TeamID != nil {
			q += " AND d.team_id = ?"
			args = append(args, scope.TeamID.String())
		}
		if scope.ContactID != nil {
			q += " AND d.contact_id = ?"
			args = append(args, scope.ContactID.String())
		}
		if scope.ProjectID != nil {
			q += " AND d.project_id = ?"
			args = append(args, scope.ProjectID.String())
		}
	}
	q += " GROUP BY d.id"

	var detail store.DocumentDetail
	var filePath string
	var uid *string
	var createdAt, updatedAt time.Time
	err := s.db.QueryRowContext(ctx, q, args...).Scan(
		&detail.Path, &filePath, &detail.Hash, &uid,
		&createdAt, &updatedAt, &detail.ChunkCount,
	)
	if err != nil {
		return nil, err
	}
	if uid != nil {
		detail.UserID = *uid
	}
	detail.CreatedAt = createdAt.UnixMilli()
	detail.UpdatedAt = updatedAt.UnixMilli()
	// Load content from FS backing file.
	if filePath != "" {
		if data, readErr := os.ReadFile(filePath); readErr == nil {
			detail.Content = string(data)
		}
	}
	// EmbeddedCount always 0 — no embedding column in SQLite
	return &detail, nil
}

// ListChunks returns chunks for a document identified by agent, user, and path.
// Scope is derived from context to prevent cross-tenant chunk inspection.
func (s *SQLiteMemoryStore) ListChunks(ctx context.Context, agentID, userID, path string) ([]store.ChunkInfo, error) {
	q := `SELECT c.id, c.start_line, c.end_line, c.text
		 FROM memory_chunks c
		 JOIN memory_documents d ON c.document_id = d.id
		 WHERE d.agent_id = ? AND d.path = ?`
	args := []any{agentID, path}

	if userID == "" {
		q += " AND d.user_id IS NULL"
	} else {
		q += " AND d.user_id = ?"
		args = append(args, userID)
	}

	scope := sqliteMemoryScopeFromContext(ctx)
	if scope != nil {
		if scope.TeamID != nil {
			q += " AND d.team_id = ?"
			args = append(args, scope.TeamID.String())
		}
		if scope.ContactID != nil {
			q += " AND d.contact_id = ?"
			args = append(args, scope.ContactID.String())
		}
		if scope.ProjectID != nil {
			q += " AND d.project_id = ?"
			args = append(args, scope.ProjectID.String())
		}
	}
	q += " ORDER BY c.start_line"

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []store.ChunkInfo
	for rows.Next() {
		var ci store.ChunkInfo
		if err := rows.Scan(&ci.ID, &ci.StartLine, &ci.EndLine, &ci.TextPreview); err != nil {
			continue
		}
		// HasEmbedding always false — no embedding column in SQLite
		result = append(result, ci)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
