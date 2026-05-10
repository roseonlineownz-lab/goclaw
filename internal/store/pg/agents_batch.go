package pg

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// GetByKeys returns agents matching the given keys in a single query.
func (s *PGAgentStore) GetByKeys(ctx context.Context, keys []string) ([]store.AgentData, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+agentSelectCols+`
		 FROM agents WHERE agent_key = ANY($1) AND deleted_at IS NULL`,
		pq.Array(keys))
	if err != nil {
		return nil, fmt.Errorf("batch agent key lookup: %w", err)
	}
	defer rows.Close()
	return scanAgentRows(rows)
}

// GetByIDs returns agents matching the given UUIDs in a single query.
func (s *PGAgentStore) GetByIDs(ctx context.Context, ids []uuid.UUID) ([]store.AgentData, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+agentSelectCols+`
		 FROM agents WHERE id = ANY($1) AND deleted_at IS NULL`,
		pq.Array(ids))
	if err != nil {
		return nil, fmt.Errorf("batch agent ID lookup: %w", err)
	}
	defer rows.Close()
	return scanAgentRows(rows)
}
