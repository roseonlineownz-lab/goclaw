package pg

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// PGEpisodicStore implements store.EpisodicStore backed by PostgreSQL.
type PGEpisodicStore struct {
	db          *sql.DB
	embProvider store.EmbeddingProvider
}

// NewPGEpisodicStore creates a new PG-backed episodic store.
func NewPGEpisodicStore(db *sql.DB) *PGEpisodicStore {
	return &PGEpisodicStore{db: db}
}

func (s *PGEpisodicStore) SetEmbeddingProvider(p store.EmbeddingProvider) { s.embProvider = p }
func (s *PGEpisodicStore) Close() error                                  { return nil }

// Create inserts a new episodic summary with optional embedding.
func (s *PGEpisodicStore) Create(ctx context.Context, ep *store.EpisodicSummary) error {
	id := uuid.Must(uuid.NewV7())
	ep.ID = id

	topics := pq.Array(ep.KeyTopics)
	now := time.Now().UTC()

	var embStr *string
	if s.embProvider != nil && ep.Summary != "" {
		vecs, err := s.embProvider.Embed(ctx, []string{ep.Summary})
		if err == nil && len(vecs) > 0 {
			v := vectorToString(vecs[0])
			embStr = &v
		} else if err != nil {
			slog.Warn("episodic: embedding failed", "err", err)
		}
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO episodic_summaries
			(id, agent_id, user_id, team_id, contact_id, project_id,
			 session_key, summary, key_topics,
			 turn_count, token_count, embedding, l0_abstract, source_id,
			 source_type, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
		ON CONFLICT DO NOTHING`,
		id, ep.AgentID, ep.UserID, ep.TeamID, ep.ContactID, ep.ProjectID,
		ep.SessionKey, ep.Summary, topics, ep.TurnCount, ep.TokenCount,
		embStr, ep.L0Abstract, ep.SourceID, ep.SourceType, now, ep.ExpiresAt)
	if err != nil {
		return fmt.Errorf("episodic create: %w", err)
	}
	ep.CreatedAt = now
	return nil
}

// Get retrieves an episodic summary by ID.
func (s *PGEpisodicStore) Get(ctx context.Context, id string) (*store.EpisodicSummary, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, agent_id, user_id, session_key, summary, key_topics,
		       turn_count, token_count, l0_abstract, source_id, source_type,
		       created_at, expires_at, recall_count, recall_score, last_recalled_at
		FROM episodic_summaries WHERE id = $1`,
		id)
	return scanEpisodic(row)
}

// Delete removes an episodic summary.
func (s *PGEpisodicStore) Delete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM episodic_summaries WHERE id = $1`, id)
	return err
}

// List returns episodic summaries ordered by created_at DESC.
// When userID is empty, returns summaries for all users of the agent.
func (s *PGEpisodicStore) List(ctx context.Context, agentID, userID string, limit, offset int) ([]store.EpisodicSummary, error) {
	if limit <= 0 {
		limit = 20
	}

	var q string
	var args []any
	if userID != "" {
		q = `SELECT id, agent_id, user_id, session_key, summary, key_topics,
			       turn_count, token_count, l0_abstract, source_id, source_type,
			       created_at, expires_at,
			       recall_count, recall_score, last_recalled_at
			FROM episodic_summaries
			WHERE agent_id = $1 AND user_id = $2
			ORDER BY created_at DESC LIMIT $3 OFFSET $4`
		args = []any{agentID, userID, limit, offset}
	} else {
		q = `SELECT id, agent_id, user_id, session_key, summary, key_topics,
			       turn_count, token_count, l0_abstract, source_id, source_type,
			       created_at, expires_at,
			       recall_count, recall_score, last_recalled_at
			FROM episodic_summaries
			WHERE agent_id = $1
			ORDER BY created_at DESC LIMIT $2 OFFSET $3`
		args = []any{agentID, limit, offset}
	}

	var rows []episodicSummaryRow
	if err := pkgSqlxDB.SelectContext(ctx, &rows, q, args...); err != nil {
		return nil, err
	}
	results := make([]store.EpisodicSummary, len(rows))
	for i := range rows {
		results[i] = rows[i].toEpisodicSummary()
	}
	return results, nil
}

// Search performs hybrid FTS + vector search on episodic summaries.
func (s *PGEpisodicStore) Search(ctx context.Context, query, agentID, userID string, opts store.EpisodicSearchOptions) ([]store.EpisodicSearchResult, error) {
	maxResults := opts.MaxResults
	if maxResults <= 0 {
		maxResults = 10
	}
	vw := opts.VectorWeight
	if vw == 0 {
		vw = 0.6
	}
	tw := opts.TextWeight
	if tw == 0 {
		tw = 0.4
	}

	// FTS search.
	ftsResults := s.ftsSearch(ctx, query, agentID, userID, maxResults*2, opts.Scope)

	// Vector search (if embedding provider available).
	var vecResults []episodicScored
	if s.embProvider != nil {
		vecs, err := s.embProvider.Embed(ctx, []string{query})
		if err == nil && len(vecs) > 0 {
			vecResults = s.vectorSearch(ctx, vecs[0], agentID, userID, maxResults*2, opts.Scope)
		}
	}

	// Merge by combined score.
	merged := mergeEpisodicScores(ftsResults, vecResults, tw, vw)
	sort.Slice(merged, func(i, j int) bool { return merged[i].score > merged[j].score })

	if len(merged) > maxResults {
		merged = merged[:maxResults]
	}

	var results []store.EpisodicSearchResult
	for _, m := range merged {
		if opts.MinScore > 0 && m.score < opts.MinScore {
			continue
		}
		results = append(results, store.EpisodicSearchResult{
			EpisodicID: m.id, L0Abstract: m.l0, Score: m.score,
			CreatedAt: m.createdAt, SessionKey: m.sessionKey,
		})
	}
	return results, nil
}

// ExistsBySourceID checks if an episodic summary with the given source_id exists (idempotency).
func (s *PGEpisodicStore) ExistsBySourceID(ctx context.Context, agentID, userID, sourceID string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM episodic_summaries
		WHERE agent_id = $1 AND user_id = $2 AND source_id = $3)`,
		agentID, userID, sourceID).Scan(&exists)
	return exists, err
}

// PruneExpired deletes episodic summaries past their expiry.
func (s *PGEpisodicStore) PruneExpired(ctx context.Context) (int, error) {
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM episodic_summaries
		WHERE expires_at IS NOT NULL AND expires_at < NOW()`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// UpdateSessionProject re-tags rows for a single session_key from
// oldProjectID to newProjectID. Called by the project-switch orchestrator
// (Layer 2 /project bot command + Layer 1 admin RPC). NULL ↔ uuid both
// supported by encoding the pointer through nilUUID.
func (s *PGEpisodicStore) UpdateSessionProject(ctx context.Context, sessionKey string, oldProjectID, newProjectID *uuid.UUID) error {
	// Match NULL on the old side using IS NOT DISTINCT FROM so callers can
	// re-tag previously-unbound rows just by passing oldProjectID=nil.
	// episodic_summaries has only created_at (no updated_at), so we don't
	// touch any timestamp here.
	_, err := s.db.ExecContext(ctx, `
		UPDATE episodic_summaries
		SET project_id = $1
		WHERE session_key = $2 AND project_id IS NOT DISTINCT FROM $3`,
		nilUUID(newProjectID), sessionKey, nilUUID(oldProjectID),
	)
	return err
}

// ListUnpromoted returns episodic summaries not yet promoted to long-term memory, oldest first.
func (s *PGEpisodicStore) ListUnpromoted(ctx context.Context, agentID, userID string, limit int) ([]store.EpisodicSummary, error) {
	return s.listUnpromoted(ctx, agentID, userID, limit, "created_at ASC")
}

// ListUnpromotedScored returns unpromoted episodic summaries ordered by
// recall_score DESC (ties broken by created_at ASC so older entries with the
// same score synthesise first). Backed by idx_episodic_recall_unpromoted.
func (s *PGEpisodicStore) ListUnpromotedScored(ctx context.Context, agentID, userID string, limit int) ([]store.EpisodicSummary, error) {
	return s.listUnpromoted(ctx, agentID, userID, limit, "recall_score DESC, created_at ASC")
}

// listUnpromoted shares the query shape between the two ListUnpromoted*
// variants. `orderBy` is a static literal supplied by the caller — it is
// NEVER derived from user input, so the concatenation below is safe.
func (s *PGEpisodicStore) listUnpromoted(ctx context.Context, agentID, userID string, limit int, orderBy string) ([]store.EpisodicSummary, error) {
	if limit <= 0 {
		limit = 20
	}
	var rows []episodicSummaryRow
	query := `
		SELECT id, agent_id, user_id, session_key, summary, key_topics,
		       turn_count, token_count, l0_abstract, source_id, source_type,
		       created_at, expires_at,
		       recall_count, recall_score, last_recalled_at
		FROM episodic_summaries
		WHERE agent_id = $1 AND user_id = $2 AND promoted_at IS NULL
		ORDER BY ` + orderBy + ` LIMIT $3`
	err := pkgSqlxDB.SelectContext(ctx, &rows, query, agentID, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("episodic list_unpromoted: %w", err)
	}
	results := make([]store.EpisodicSummary, len(rows))
	for i := range rows {
		results[i] = rows[i].toEpisodicSummary()
	}
	return results, nil
}

// RecordRecall increments recall_count, folds `score` into the running
// average stored in recall_score, and sets last_recalled_at=NOW(). Uses a
// single UPDATE so the row is rewritten atomically.
func (s *PGEpisodicStore) RecordRecall(ctx context.Context, id string, score float64) error {
	if id == "" {
		return nil
	}
	// Clamp inputs so bad data from callers cannot corrupt the running average.
	if score < 0 {
		score = 0
	} else if score > 1 {
		score = 1
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE episodic_summaries
		SET recall_count = recall_count + 1,
		    recall_score = (recall_score * recall_count + $1) / (recall_count + 1),
		    last_recalled_at = NOW()
		WHERE id = $2`,
		score, id)
	if err != nil {
		return fmt.Errorf("episodic record_recall: %w", err)
	}
	return nil
}

// MarkPromoted sets promoted_at=now() for the given episodic summary IDs.
func (s *PGEpisodicStore) MarkPromoted(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE episodic_summaries SET promoted_at = NOW()
		WHERE id = ANY($1)`,
		pq.Array(ids))
	if err != nil {
		return fmt.Errorf("episodic mark_promoted: %w", err)
	}
	return nil
}

// CountUnpromoted returns the count of unpromoted episodic summaries for an agent/user.
func (s *PGEpisodicStore) CountUnpromoted(ctx context.Context, agentID, userID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM episodic_summaries
		WHERE agent_id = $1 AND user_id = $2 AND promoted_at IS NULL`,
		agentID, userID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("episodic count_unpromoted: %w", err)
	}
	return count, nil
}
