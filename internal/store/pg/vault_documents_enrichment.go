package pg

import (
	"context"
	"fmt"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// ListUnenrichedDocs returns documents with empty summary for re-enrichment.
// limit=0 means no limit.
func (s *PGVaultStore) ListUnenrichedDocs(ctx context.Context, limit int) ([]store.VaultDocument, error) {
	q := `SELECT ` + vaultDocSelectCols + `
		FROM vault_documents
		WHERE (summary IS NULL OR summary = '')
		ORDER BY created_at ASC`
	var args []any

	if limit > 0 {
		q += " LIMIT $1"
		args = append(args, limit)
	}

	var rows []vaultDocRow
	if err := pkgSqlxDB.SelectContext(ctx, &rows, q, args...); err != nil {
		return nil, fmt.Errorf("vault.list_unenriched: %w", err)
	}
	return vaultDocRowsToDocs(rows), nil
}

// UpdateSummaryAndReembed updates the document summary and re-embeds the combined text.
func (s *PGVaultStore) UpdateSummaryAndReembed(ctx context.Context, docID, summary string) error {
	did, err := parseUUID(docID)
	if err != nil {
		return fmt.Errorf("vault update summary: doc: %w", err)
	}

	// Fetch title+path to build embed text.
	var title, path string
	err = s.db.QueryRowContext(ctx,
		`SELECT title, path FROM vault_documents WHERE id = $1`,
		did,
	).Scan(&title, &path)
	if err != nil {
		return fmt.Errorf("vault.update_summary: fetch doc: %w", err)
	}

	var embStr *string
	if s.embProvider != nil {
		embedText := title + " " + path + " " + summary
		vecs, embErr := s.embProvider.Embed(ctx, []string{embedText})
		if embErr == nil && len(vecs) > 0 {
			v := vectorToString(vecs[0])
			embStr = &v
		}
	}

	_, err = s.db.ExecContext(ctx, `
		UPDATE vault_documents
		SET summary = $1, embedding = COALESCE($2, embedding), updated_at = $3
		WHERE id = $4`,
		summary, embStr, time.Now().UTC(), did,
	)
	return err
}

// FindSimilarDocs finds documents with similar embeddings to the given docID.
// Returns top-N neighbors excluding the source doc itself.
// Empty agentID means no agent filter.
func (s *PGVaultStore) FindSimilarDocs(ctx context.Context, agentID, docID string, limit int) ([]store.VaultSearchResult, error) {
	aid, err := optAgentUUID(&agentID)
	if err != nil {
		return nil, fmt.Errorf("find similar: agent: %w", err)
	}
	did, err := parseUUID(docID)
	if err != nil {
		return nil, fmt.Errorf("find similar: doc: %w", err)
	}

	// Fetch source embedding.
	var embStr *string
	err = s.db.QueryRowContext(ctx,
		`SELECT embedding::text FROM vault_documents WHERE id = $1`,
		did,
	).Scan(&embStr)
	if err != nil || embStr == nil {
		return nil, nil // no embedding = no neighbors
	}

	q := `SELECT ` + vaultDocSelectCols + `,
			1 - (embedding <=> $1::halfvec) AS score
		FROM vault_documents
		WHERE id != $2 AND embedding IS NOT NULL`
	args := []any{*embStr, did}
	p := 3

	if aid != nil {
		q += fmt.Sprintf(" AND agent_id = $%d", p)
		args = append(args, *aid)
		p++
	}
	q += fmt.Sprintf(" ORDER BY embedding <=> $1::halfvec LIMIT $%d", p)
	args = append(args, limit)

	var scanned []vaultSearchRow
	if err := pkgSqlxDB.SelectContext(ctx, &scanned, q, args...); err != nil {
		return nil, fmt.Errorf("vault.find_similar: %w", err)
	}
	return vaultSearchRowsToResults(scanned, "vault"), nil
}
