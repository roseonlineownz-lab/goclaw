package pg

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/memory"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// PGMemoryStore implements store.MemoryStore backed by Postgres.
type PGMemoryStore struct {
	db       *sql.DB
	provider store.EmbeddingProvider
	mu       sync.RWMutex   // protects cfg from concurrent read/write
	cfg      PGMemoryConfig
	fsWriter memory.FSWriter // nil = FS-backed mode disabled (content stored in DB fallback)
}

// PGMemoryConfig configures the PG memory store.
type PGMemoryConfig struct {
	MaxChunkLen  int
	ChunkOverlap int
	MaxResults   int
	VectorWeight float64
	TextWeight   float64
}

// DefaultPGMemoryConfig returns sensible defaults.
func DefaultPGMemoryConfig() PGMemoryConfig {
	return PGMemoryConfig{
		MaxChunkLen:  1000,
		ChunkOverlap: 200,
		MaxResults:   6,
		VectorWeight: 0.7,
		TextWeight:   0.3,
	}
}

func NewPGMemoryStore(db *sql.DB, cfg PGMemoryConfig) *PGMemoryStore {
	return &PGMemoryStore{db: db, cfg: cfg}
}

// SetFSWriter attaches a filesystem writer to the store.
// When set, GetDocument reads content from FS and PutDocument writes to FS.
// Must be called before the store is used; not safe for concurrent mutation.
func (s *PGMemoryStore) SetFSWriter(w memory.FSWriter) {
	s.fsWriter = w
}

func (s *PGMemoryStore) GetDocument(ctx context.Context, agentID, userID, path string) (string, error) {
	if s.fsWriter != nil {
		scope := memory.ScopeKey{AgentID: agentID, UserID: userID}
		data, _, err := s.fsWriter.Read(ctx, scope, path)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}

	// Legacy path: no FSWriter configured — should not happen in v4 greenfield
	// but kept as a safe fallback so tests without FSWriter wiring still compile.
	aid, err := parseUUID(agentID)
	if err != nil {
		return "", fmt.Errorf("memory get document: %w", err)
	}
	var filePath string
	if store.IsSharedMemory(ctx) {
		err = s.db.QueryRowContext(ctx,
			"SELECT file_path FROM memory_documents WHERE agent_id = $1 AND path = $2 ORDER BY updated_at DESC LIMIT 1",
			aid, path).Scan(&filePath)
	} else if userID == "" {
		err = s.db.QueryRowContext(ctx,
			"SELECT file_path FROM memory_documents WHERE agent_id = $1 AND path = $2 AND user_id IS NULL",
			aid, path).Scan(&filePath)
	} else {
		err = s.db.QueryRowContext(ctx,
			"SELECT file_path FROM memory_documents WHERE agent_id = $1 AND path = $2 AND user_id = $3",
			aid, path, userID).Scan(&filePath)
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

func (s *PGMemoryStore) PutDocument(ctx context.Context, agentID, userID, path, content string) error {
	if s.fsWriter != nil {
		scope := memory.ScopeKey{AgentID: agentID, UserID: userID}
		_, err := s.fsWriter.Write(ctx, scope, path, []byte(content), -1 /* unconditional */)
		return err
	}

	// Legacy path: direct DB write without FSWriter.
	// file_path and content_hash are stored; version starts at 1.
	aid, err := parseUUID(agentID)
	if err != nil {
		return fmt.Errorf("memory put document: %w", err)
	}
	hash := memory.ContentHash(content)
	id := uuid.Must(uuid.NewV7())
	now := time.Now()

	var uid *string
	if userID != "" {
		uid = &userID
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO memory_documents (id, agent_id, user_id, path, file_path, content_hash, version, updated_at)
		 VALUES ($1, $2, $3, $4, '', $5, 1, $6)
		 ON CONFLICT (agent_id, COALESCE(team_id::text,''), COALESCE(user_id::text,''), COALESCE(contact_id::text,''), COALESCE(project_id::text,''), path)
		 DO UPDATE SET content_hash = EXCLUDED.content_hash, updated_at = EXCLUDED.updated_at`,
		id, aid, uid, path, hash, now,
	)
	return err
}

func (s *PGMemoryStore) DeleteDocument(ctx context.Context, agentID, userID, path string) error {
	aid, err := parseUUID(agentID)
	if err != nil {
		return fmt.Errorf("memory delete document: %w", err)
	}
	var res sql.Result
	if store.IsSharedMemory(ctx) {
		// Shared: delete any matching doc regardless of user_id
		res, err = s.db.ExecContext(ctx,
			"DELETE FROM memory_documents WHERE agent_id = $1 AND path = $2",
			aid, path)
	} else if userID == "" {
		res, err = s.db.ExecContext(ctx,
			"DELETE FROM memory_documents WHERE agent_id = $1 AND path = $2 AND user_id IS NULL",
			aid, path)
	} else {
		res, err = s.db.ExecContext(ctx,
			"DELETE FROM memory_documents WHERE agent_id = $1 AND path = $2 AND user_id = $3",
			aid, path, userID)
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

func (s *PGMemoryStore) ListDocuments(ctx context.Context, agentID, userID string) ([]store.DocumentInfo, error) {
	aid, err := parseUUID(agentID)
	if err != nil {
		return nil, fmt.Errorf("memory list documents: %w", err)
	}

	var q string
	var args []any
	if store.IsSharedMemory(ctx) {
		// Shared: list ALL docs for agent (global + per-user from all users)
		q = "SELECT agent_id, path, content_hash AS hash, user_id, updated_at FROM memory_documents WHERE agent_id = $1"
		args = []any{aid}
	} else if userID == "" {
		q = "SELECT agent_id, path, content_hash AS hash, user_id, updated_at FROM memory_documents WHERE agent_id = $1 AND user_id IS NULL"
		args = []any{aid}
	} else {
		q = "SELECT agent_id, path, content_hash AS hash, user_id, updated_at FROM memory_documents WHERE agent_id = $1 AND (user_id IS NULL OR user_id::text = $2)"
		args = []any{aid, userID}
	}

	var rows []documentInfoRow
	if err := pkgSqlxDB.SelectContext(ctx, &rows, q, args...); err != nil {
		return nil, err
	}
	result := make([]store.DocumentInfo, len(rows))
	for i := range rows {
		result[i] = rows[i].toDocumentInfo()
	}
	return result, nil
}

// IndexDocument chunks a document and stores chunks with embeddings.
func (s *PGMemoryStore) IndexDocument(ctx context.Context, agentID, userID, path string) error {
	aid, err := parseUUID(agentID)
	if err != nil {
		return fmt.Errorf("memory index document: %w", err)
	}

	// Get document content
	content, err := s.GetDocument(ctx, agentID, userID, path)
	if err != nil {
		return err
	}

	// Get document ID
	var docID uuid.UUID
	if store.IsSharedMemory(ctx) {
		// Shared: no user_id filter
		err = s.db.QueryRowContext(ctx,
			"SELECT id FROM memory_documents WHERE agent_id = $1 AND path = $2 ORDER BY updated_at DESC LIMIT 1",
			aid, path).Scan(&docID)
	} else if userID == "" {
		err = s.db.QueryRowContext(ctx,
			"SELECT id FROM memory_documents WHERE agent_id = $1 AND path = $2 AND user_id IS NULL",
			aid, path).Scan(&docID)
	} else {
		err = s.db.QueryRowContext(ctx,
			"SELECT id FROM memory_documents WHERE agent_id = $1 AND path = $2 AND user_id = $3",
			aid, path, userID).Scan(&docID)
	}
	if err != nil {
		return err
	}

	// Delete old chunks
	s.db.ExecContext(ctx, "DELETE FROM memory_chunks WHERE document_id = $1", docID)

	// Resolve chunk parameters: per-agent override → global default
	chunkLen, chunkOverlap := s.chunkConfig()
	if rc := store.RunContextFromCtx(ctx); rc != nil && rc.MemoryCfg != nil {
		if rc.MemoryCfg.MaxChunkLen > 0 {
			chunkLen = rc.MemoryCfg.MaxChunkLen
		}
		if rc.MemoryCfg.ChunkOverlap > 0 {
			chunkOverlap = rc.MemoryCfg.ChunkOverlap
		}
	}

	// Chunk text
	chunks := memory.ChunkText(content, chunkLen, chunkOverlap)
	if len(chunks) == 0 {
		return nil
	}

	// Generate embeddings with cache
	var embeddings [][]float32
	if s.provider != nil {
		providerName := s.provider.Name()
		providerModel := s.provider.Model()

		// Compute content hashes for all chunks
		hashes := make([]string, len(chunks))
		for i, c := range chunks {
			hashes[i] = memory.ContentHash(c.Text)
		}

		// Batch lookup cached embeddings
		cached, cacheErr := s.lookupEmbeddingCache(ctx, hashes, providerName, providerModel)
		if cacheErr != nil {
			slog.Warn("embedding cache lookup failed, falling back to full API call",
				"path", path, "error", cacheErr)
			cached = nil
		}

		// Determine which chunks need fresh embeddings
		var uncachedIdxs []int
		var uncachedTexts []string
		for i, c := range chunks {
			if cached != nil {
				if _, ok := cached[hashes[i]]; ok {
					continue
				}
			}
			uncachedIdxs = append(uncachedIdxs, i)
			uncachedTexts = append(uncachedTexts, c.Text)
		}

		if len(cached) > 0 {
			slog.Debug("embedding cache hit",
				"path", path, "cached", len(cached), "uncached", len(uncachedTexts))
		}

		// Call embedding API only for uncached texts
		var freshEmbeddings [][]float32
		if len(uncachedTexts) > 0 {
			var embErr error
			freshEmbeddings, embErr = s.provider.Embed(ctx, uncachedTexts)
			if embErr != nil {
				slog.Warn("memory embedding failed, storing chunks without vectors",
					"path", path, "chunks", len(chunks), "error", embErr)
			}
		}

		// Write fresh embeddings back to cache
		if len(freshEmbeddings) > 0 {
			if len(freshEmbeddings) != len(uncachedTexts) {
				slog.Warn("embedding API returned mismatched count",
					"expected", len(uncachedTexts), "got", len(freshEmbeddings))
			}
			var cacheEntries []embeddingCacheEntry
			for j, emb := range freshEmbeddings {
				if j < len(uncachedIdxs) {
					cacheEntries = append(cacheEntries, embeddingCacheEntry{
						Hash:      hashes[uncachedIdxs[j]],
						Embedding: emb,
					})
				}
			}
			if writeErr := s.writeEmbeddingCache(ctx, cacheEntries, providerName, providerModel); writeErr != nil {
				slog.Warn("embedding cache write failed", "path", path, "error", writeErr)
			}
		}

		// Merge cached + fresh embeddings into final slice
		if cached != nil || freshEmbeddings != nil {
			embeddings = make([][]float32, len(chunks))
			// Fill from cache
			for i, h := range hashes {
				if cached != nil {
					if emb, ok := cached[h]; ok {
						embeddings[i] = emb
					}
				}
			}
			// Fill from fresh
			for j, idx := range uncachedIdxs {
				if j < len(freshEmbeddings) {
					embeddings[idx] = freshEmbeddings[j]
				}
			}
		}
	}

	// Insert chunks
	for i, tc := range chunks {
		hash := memory.ContentHash(tc.Text)
		chunkID := uuid.Must(uuid.NewV7())
		now := time.Now()

		var uid *string
		if userID != "" {
			uid = &userID
		}

		if embeddings != nil && i < len(embeddings) && embeddings[i] != nil {
			// Insert with embedding via raw SQL (pgvector)
			s.db.ExecContext(ctx,
				`INSERT INTO memory_chunks (id, agent_id, document_id, user_id, path, start_line, end_line, hash, text, embedding, updated_at)
				 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::halfvec, $11)`,
				chunkID, aid, docID, uid, path, tc.StartLine, tc.EndLine, hash, tc.Text,
				vectorToString(embeddings[i]), now,
			)
		} else {
			s.db.ExecContext(ctx,
				`INSERT INTO memory_chunks (id, agent_id, document_id, user_id, path, start_line, end_line, hash, text, updated_at)
				 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
				 ON CONFLICT DO NOTHING`,
				chunkID, aid, docID, uid, path, tc.StartLine, tc.EndLine, hash, tc.Text, now,
			)
		}
	}

	return nil
}

func (s *PGMemoryStore) IndexAll(ctx context.Context, agentID, userID string) error {
	docs, err := s.ListDocuments(ctx, agentID, userID)
	if err != nil {
		return err
	}
	for _, doc := range docs {
		s.IndexDocument(ctx, agentID, doc.UserID, doc.Path)
	}
	return nil
}

func (s *PGMemoryStore) SetEmbeddingProvider(provider store.EmbeddingProvider) {
	s.provider = provider
}

// UpdateChunkConfig updates chunk splitting parameters at runtime (e.g. after system config change).
func (s *PGMemoryStore) UpdateChunkConfig(maxChunkLen, chunkOverlap int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if maxChunkLen > 0 {
		s.cfg.MaxChunkLen = maxChunkLen
	}
	if chunkOverlap >= 0 {
		s.cfg.ChunkOverlap = chunkOverlap
	}
}

// chunkConfig returns a snapshot of the current chunk parameters (thread-safe).
func (s *PGMemoryStore) chunkConfig() (maxLen, overlap int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.MaxChunkLen, s.cfg.ChunkOverlap
}

// BackfillEmbeddings finds all chunks without embeddings and generates them.
// Processes in batches to avoid memory spikes. Safe to call multiple times.
func (s *PGMemoryStore) BackfillEmbeddings(ctx context.Context) (int, error) {
	if s.provider == nil {
		return 0, fmt.Errorf("no embedding provider configured")
	}

	const batchSize = 50
	total := 0

	for {
		type backfillRow struct {
			ID   uuid.UUID `db:"id"`
			Text string    `db:"text"`
		}
		var chunks []backfillRow
		if err := pkgSqlxDB.SelectContext(ctx, &chunks,
			"SELECT id, text FROM memory_chunks WHERE embedding IS NULL ORDER BY id ASC LIMIT $1", batchSize); err != nil {
			return total, fmt.Errorf("query chunks without embeddings: %w", err)
		}

		if len(chunks) == 0 {
			break
		}

		texts := make([]string, len(chunks))
		for i, c := range chunks {
			texts[i] = c.Text
		}

		embeddings, err := s.provider.Embed(ctx, texts)
		if err != nil {
			return total, fmt.Errorf("generate embeddings: %w", err)
		}

		for i, chunk := range chunks {
			if i >= len(embeddings) {
				break
			}
			vecStr := vectorToString(embeddings[i])
			if _, err := s.db.ExecContext(ctx,
				"UPDATE memory_chunks SET embedding = $1::halfvec WHERE id = $2",
				vecStr, chunk.ID,
			); err != nil {
				return total, fmt.Errorf("update chunk embedding id=%s: %w", chunk.ID, err)
			}
			total++
		}

		if len(chunks) < batchSize {
			break
		}
	}

	return total, nil
}

func (s *PGMemoryStore) Close() error { return nil }

// --- Helpers ---

// parseUUID returns the parsed UUID or a descriptive error. Use for every
// INSERT/UPDATE/UPSERT/DELETE and any SELECT WHERE where silent nil would
// either corrupt data or hide bugs as empty reads / zero-row updates. FK
// constraints reject bad writes at the driver layer, but errors there come
// back as cryptic PG 23503 — parseUUID catches them upstream with a clean
// Go error. See docs/agent-identity-conventions.md.
func parseUUID(s string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, fmt.Errorf("parse uuid %q: %w", s, err)
	}
	return id, nil
}

// parseUUIDOrNil returns the parsed UUID or uuid.Nil on failure, without
// raising an error. INTENTIONALLY silent — only acceptable on read-only
// SELECT WHERE paths where a no-match (empty result) is the correct
// semantics on bad input. Do NOT use for writes, updates, deletes, or any
// SELECT where an empty result would hide a bug. Prefer parseUUID for new
// code. See docs/agent-identity-conventions.md.
func parseUUIDOrNil(s string) uuid.UUID {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil
	}
	return id
}

func vectorToString(v []float32) string {
	if len(v) == 0 {
		return ""
	}
	buf := make([]byte, 0, len(v)*10)
	buf = append(buf, '[')
	for i, f := range v {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = append(buf, fmt.Appendf(nil, "%g", f)...)
	}
	buf = append(buf, ']')
	return string(buf)
}
