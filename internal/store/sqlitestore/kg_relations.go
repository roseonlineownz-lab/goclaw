//go:build sqlite || sqliteonly

package sqlitestore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

func (s *SQLiteKnowledgeGraphStore) UpsertRelation(ctx context.Context, relation *store.Relation) error {
	props, err := json.Marshal(relation.Properties)
	if err != nil {
		props = []byte("{}")
	}
	id := uuid.Must(uuid.NewV7()).String()
	now := time.Now().UTC().Format(time.RFC3339Nano)

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO kg_relations
			(id, agent_id, user_id, team_id, source_entity_id, relation_type, target_entity_id,
			 confidence, properties, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(agent_id, COALESCE(user_id,''), source_entity_id, relation_type, target_entity_id) DO UPDATE SET
			confidence  = excluded.confidence,
			properties  = excluded.properties`,
		id, relation.AgentID, nilStr(relation.UserID), nilStr(relation.TeamID),
		relation.SourceEntityID, relation.RelationType, relation.TargetEntityID,
		relation.Confidence, string(props), now,
	)
	return err
}

func (s *SQLiteKnowledgeGraphStore) DeleteRelation(ctx context.Context, agentID, userID, relationID string) error {
	userClause, userArgs := kgUserClauseFor(ctx, userID)
	args := append([]any{relationID, agentID}, userArgs...)
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM kg_relations WHERE id = ? AND agent_id = ?`+userClause,
		args...,
	)
	return err
}

func (s *SQLiteKnowledgeGraphStore) ListRelations(ctx context.Context, agentID, userID, entityID string) ([]store.Relation, error) {
	userClause, userArgs := kgUserClauseFor(ctx, userID)

	args := append([]any{agentID, entityID, entityID}, userArgs...)

	q := `SELECT id, agent_id, user_id, source_entity_id, relation_type, target_entity_id,
		         confidence, properties, created_at
		  FROM kg_relations
		  WHERE agent_id = ? AND valid_until IS NULL
		    AND (source_entity_id = ? OR target_entity_id = ?)` +
		userClause + `
		  ORDER BY created_at DESC`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRelationRows(rows)
}

func (s *SQLiteKnowledgeGraphStore) ListAllRelations(ctx context.Context, agentID, userID string, limit int) ([]store.Relation, error) {
	if limit <= 0 {
		limit = 200
	}

	userClause, userArgs := kgUserClauseFor(ctx, userID)

	where := "agent_id = ? AND valid_until IS NULL"
	args := []any{agentID}
	if userClause != "" {
		where += userClause
		args = append(args, userArgs...)
	}
	args = append(args, limit)

	q := fmt.Sprintf(`
		SELECT id, agent_id, user_id, source_entity_id, relation_type, target_entity_id,
		       confidence, properties, created_at
		FROM kg_relations WHERE %s
		ORDER BY created_at DESC LIMIT ?`, where)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRelationRows(rows)
}

// upsertRelationTx upserts a relation within an existing transaction.
// Used by IngestExtraction to avoid nesting transactions.
func upsertRelationTx(ctx context.Context, tx *sql.Tx, agentID, userID string, r *store.Relation, now string) error {
	props, _ := json.Marshal(r.Properties)
	id := uuid.Must(uuid.NewV7()).String()
	_, err := tx.ExecContext(ctx, `
		INSERT INTO kg_relations
			(id, agent_id, user_id, team_id, source_entity_id, relation_type, target_entity_id,
			 confidence, properties, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(agent_id, COALESCE(user_id,''), source_entity_id, relation_type, target_entity_id) DO UPDATE SET
			confidence  = excluded.confidence,
			properties  = excluded.properties`,
		id, agentID, nilStr(userID), nilStr(r.TeamID),
		r.SourceEntityID, r.RelationType, r.TargetEntityID,
		r.Confidence, string(props), now,
	)
	return err
}

// scanRelationRows iterates sql.Rows and scans each into a store.Relation.
func scanRelationRows(rows *sql.Rows) ([]store.Relation, error) {
	var result []store.Relation
	for rows.Next() {
		r, err := scanRelation(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}
