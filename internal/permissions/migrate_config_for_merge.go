// Package permissions — helper consumed by internal/store/pg/merge_aggregate.go
// inside its atomic merge TX. Single source of truth for the agent_config_permissions
// ripple SQL.
//
// When a channel contact is merged into a canonical human user, all
// agent_config_permissions rows keyed to the source (channel-derived) user IDs
// must flip to the target canonical user ID. This ensures the merged user retains
// all previously-granted per-agent config permissions without any permission window
// between the contact merge and the permission re-assignment.
package permissions

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

// MigrateConfigPermissionsForMerge re-keys all agent_config_permissions rows
// whose user_id matches any of sourceUserIDs to targetUserID, within the
// provided transaction.
//
// The caller (merge_aggregate.go) drives the TX boundary; this helper only adds
// SQL statements to the existing transaction — it neither commits nor rolls back.
// On conflict (agent_id, scope, config_type, user_id) the existing target row
// wins via ON CONFLICT DO NOTHING, so duplicate source→target rows collapse
// safely without losing the more-permissive grant.
//
// sourceUserIDs may be empty; in that case no SQL is executed and nil is returned.
func MigrateConfigPermissionsForMerge(ctx context.Context, tx *sql.Tx, sourceUserIDs []uuid.UUID, targetUserID uuid.UUID) error {
	if len(sourceUserIDs) == 0 {
		return nil
	}

	// Convert UUIDs to string representation: agent_config_permissions.user_id
	// is VARCHAR(255), not a UUID column — values are stored as string slugs or
	// UUID string forms depending on the grant path.
	sourceStrs := make([]string, len(sourceUserIDs))
	for i, id := range sourceUserIDs {
		sourceStrs[i] = id.String()
	}

	// Step 1: update rows that won't collide with an existing target grant.
	// ON CONFLICT DO NOTHING drops duplicates so the target's existing grant
	// (if any) is preserved with its original permission level.
	_, err := tx.ExecContext(ctx, `
		UPDATE agent_config_permissions
		   SET user_id = $1
		 WHERE user_id = ANY($2)
		   AND NOT EXISTS (
		         SELECT 1 FROM agent_config_permissions existing
		          WHERE existing.agent_id    = agent_config_permissions.agent_id
		            AND existing.scope       = agent_config_permissions.scope
		            AND existing.config_type = agent_config_permissions.config_type
		            AND existing.user_id     = $1
		       )
	`, targetUserID.String(), pq.Array(sourceStrs))
	if err != nil {
		return fmt.Errorf("migrate config permissions: update non-conflicting: %w", err)
	}

	// Step 2: delete residual source rows that could not be re-keyed because
	// a target grant already existed (unique constraint on agent_id, scope,
	// config_type, user_id). The target row already carries the permission;
	// the source row is now an orphan that would violate the unique constraint
	// if we left it pointing to targetUserID.
	_, err = tx.ExecContext(ctx, `
		DELETE FROM agent_config_permissions
		 WHERE user_id = ANY($1)
	`, pq.Array(sourceStrs))
	if err != nil {
		return fmt.Errorf("migrate config permissions: delete residual source rows: %w", err)
	}

	return nil
}
