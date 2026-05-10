package pg

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// MergeUserAggregate atomically merges SourceUserIDs' data into TargetUserID
// across six tables: channel_contacts, memory_documents, agent_config_permissions,
// user_context_files, agent_sessions, and traces.
// All UPDATEs share one *sql.Tx — atomic merge guarantee.
//
// Lock order to prevent deadlock with concurrent merges:
//
//	contacts (FOR UPDATE) → target user check → permissions → memory → traces → sessions
//
// Pre-checks inside the TX (concurrency-safe via SELECT FOR UPDATE):
//   - ContactIDs must all exist in channel_contacts (rejects vacuous-pass attack)
//   - source contacts must have merged_id IS NULL (no user→user merge)
//   - target user must exist and not be soft-deleted
//   - target user must not have been merged elsewhere (depth cap = 1)
//
// On success: contact-resolve cache invalidated; OnGroupContactsMerged callback
// invoked post-commit for group contacts (best-effort FS relocation by caller).
// Note: spans are NOT updated here — spans.user_id column does not exist in the
// current schema. Spans inherit user attribution via their parent trace.
func (s *PGContactStore) MergeUserAggregate(ctx context.Context, req store.MergeUserAggregateRequest) error {
	if len(req.ContactIDs) == 0 {
		return fmt.Errorf("merge: contact_ids required")
	}
	if req.TargetUserID == uuid.Nil {
		return fmt.Errorf("merge: target_user_id required")
	}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("merge: begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if err := pgMergeAssertTargetExists(ctx, tx, req.TargetUserID); err != nil {
		return err
	}
	// Reject requests where ContactIDs contain UUIDs not present in DB.
	// Without this check a caller can supply fabricated UUIDs and the UPDATE
	// silently matches zero rows — a vacuous-pass that bypasses the merge audit.
	if err := pgMergeAssertContactIDsExist(ctx, tx, req.ContactIDs); err != nil {
		return err
	}
	if err := pgMergeAssertSourceUnmerged(ctx, tx, req.ContactIDs); err != nil {
		return err
	}
	if err := pgMergeAssertTargetUnmerged(ctx, tx, req.TargetUserID); err != nil {
		return err
	}

	// Collect group contact IDs for post-commit FS relocation callback.
	// Done inside TX while rows are locked to avoid TOCTOU on peer_kind.
	var groupContactIDs []uuid.UUID
	if req.OnGroupContactsMerged != nil {
		groupContactIDs, err = pgFetchGroupContactIDs(ctx, tx, req.ContactIDs)
		if err != nil {
			return err
		}
	}

	// Step 1: stamp contacts with merged_id (lock order: contacts first).
	if _, err := tx.ExecContext(ctx,
		`UPDATE channel_contacts
		    SET merged_id = $1, merge_audit = COALESCE($2::jsonb, '{}'::jsonb)
		  WHERE id = ANY($3)`,
		req.TargetUserID, jsonbOrNil(req.MergeAudit), pq.Array(req.ContactIDs),
	); err != nil {
		return fmt.Errorf("merge: update channel_contacts: %w", err)
	}

	if len(req.SourceUserIDs) > 0 {
		sourceArr := pq.Array(req.SourceUserIDs)

		// Step 2a: memory_documents keyed by user_id.
		if _, err := tx.ExecContext(ctx,
			`UPDATE memory_documents SET user_id = $1 WHERE user_id = ANY($2)`,
			req.TargetUserID, sourceArr,
		); err != nil {
			return fmt.Errorf("merge: update memory_documents (user_id): %w", err)
		}

		// Step 3: migrate agent_config_permissions.
		// Delegated to the permissions package — single source of truth for this SQL.
		if err := permissions.MigrateConfigPermissionsForMerge(ctx, tx, req.SourceUserIDs, req.TargetUserID); err != nil {
			return fmt.Errorf("merge: %w", err)
		}

		// Step 4: migrate user_context_files.
		if _, err := tx.ExecContext(ctx,
			`UPDATE user_context_files SET user_id = $1 WHERE user_id = ANY($2)`,
			req.TargetUserID, sourceArr,
		); err != nil {
			return fmt.Errorf("merge: update user_context_files: %w", err)
		}

		// Step 5: migrate agent_sessions.
		if _, err := tx.ExecContext(ctx,
			`UPDATE agent_sessions SET user_id = $1 WHERE user_id = ANY($2)`,
			req.TargetUserID, sourceArr,
		); err != nil {
			return fmt.Errorf("merge: update agent_sessions: %w", err)
		}
	}

	if len(req.ContactIDs) > 0 {
		contactArr := pq.Array(req.ContactIDs)

		// Step 2b: memory_documents 5D scope rows keyed by contact_id only (user_id IS NULL).
		// contact_id was added to memory_documents by the 5D-scope migration (Plan #5);
		// guard with a column check so the merge TX succeeds on older DBs.
		if colExists, checkErr := pgColumnExists(ctx, tx, "memory_documents", "contact_id"); checkErr != nil {
			return fmt.Errorf("merge: check memory_documents.contact_id column: %w", checkErr)
		} else if colExists {
			if _, err := tx.ExecContext(ctx,
				`UPDATE memory_documents
				    SET user_id = $1, contact_id = NULL
				  WHERE contact_id = ANY($2) AND user_id IS NULL`,
				req.TargetUserID, contactArr,
			); err != nil {
				return fmt.Errorf("merge: update memory_documents (contact_id): %w", err)
			}
		}

		// Step 6: migrate traces (contact_id axis — independent of SourceUserIDs).
		// Traces are linked to contacts, not directly to users, so they must flip
		// even when the channel session had no registered user_id.
		// traces.user_id exists in the initial schema; no column guard required.
		if _, err := tx.ExecContext(ctx,
			`UPDATE traces SET user_id = $1 WHERE contact_id = ANY($2)`,
			req.TargetUserID, contactArr,
		); err != nil {
			return fmt.Errorf("merge: update traces: %w", err)
		}
		// spans are not updated: spans.user_id column does not exist in the current
		// schema. Spans inherit user attribution via their parent trace. Reintroduce
		// the UPDATE here when the column is added in a future migration.
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("merge: commit: %w", err)
	}
	committed = true
	s.InvalidateContactResolveCache()

	// Post-commit: invoke caller's FS relocation callback for group contacts.
	// This runs outside the TX — failure must never surface as merge error.
	if req.OnGroupContactsMerged != nil && len(groupContactIDs) > 0 {
		req.OnGroupContactsMerged(groupContactIDs)
	}

	return nil
}

// pgColumnExists reports whether the given column exists on the table. Used to
// guard schema-version-dependent UPDATEs inside the merge TX so the merge
// succeeds on DBs where optional columns have not yet been added by a migration.
func pgColumnExists(ctx context.Context, tx *sql.Tx, table, column string) (bool, error) {
	var exists bool
	err := tx.QueryRowContext(ctx,
		`SELECT EXISTS (
		    SELECT 1 FROM information_schema.columns
		     WHERE table_schema = 'public'
		       AND table_name   = $1
		       AND column_name  = $2
		)`, table, column,
	).Scan(&exists)
	return exists, err
}

// pgMergeAssertContactIDsExist checks that every UUID in contactIDs corresponds
// to an existing channel_contacts row. Detects fabricated UUIDs that would
// otherwise silently match zero rows in subsequent UPDATEs.
func pgMergeAssertContactIDsExist(ctx context.Context, tx *sql.Tx, contactIDs []uuid.UUID) error {
	var count int
	err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM channel_contacts WHERE id = ANY($1)`,
		pq.Array(contactIDs),
	).Scan(&count)
	if err != nil {
		return fmt.Errorf("merge: count contact_ids: %w", err)
	}
	if count != len(contactIDs) {
		return store.ErrContactIDNotFound
	}
	return nil
}

// pgFetchGroupContactIDs returns contact UUIDs from the input set whose
// peer_kind = 'group'. Called inside the TX while rows are locked.
func pgFetchGroupContactIDs(ctx context.Context, tx *sql.Tx, contactIDs []uuid.UUID) ([]uuid.UUID, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT id FROM channel_contacts WHERE id = ANY($1) AND peer_kind = 'group'`,
		pq.Array(contactIDs),
	)
	if err != nil {
		return nil, fmt.Errorf("merge: fetch group contacts: %w", err)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("merge: scan group contact id: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// pgMergeAssertTargetExists verifies the target user row is present and not
// soft-deleted. Without the deleted_at filter an admin could resurrect a tombstoned
// account by merging contacts into it.
func pgMergeAssertTargetExists(ctx context.Context, tx *sql.Tx, target uuid.UUID) error {
	var ok bool
	err := tx.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM users WHERE id = $1 AND deleted_at IS NULL)`, target,
	).Scan(&ok)
	if err != nil {
		return fmt.Errorf("merge: check target user: %w", err)
	}
	if !ok {
		return store.ErrMergeTargetUserNotFound
	}
	return nil
}

// pgMergeAssertSourceUnmerged ensures every source contact has merged_id IS NULL.
// SELECT FOR UPDATE locks all addressed rows unconditionally so concurrent
// merge attempts queue behind the first winner — without that, two TXes both
// see merged_id IS NULL and both succeed (last-write-wins), violating the
// atomic-merge contract.
func pgMergeAssertSourceUnmerged(ctx context.Context, tx *sql.Tx, contactIDs []uuid.UUID) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT merged_id FROM channel_contacts
		  WHERE id = ANY($1)
		  FOR UPDATE`,
		pq.Array(contactIDs),
	)
	if err != nil {
		return fmt.Errorf("merge: check source contacts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var merged sql.NullString
		if scanErr := rows.Scan(&merged); scanErr != nil {
			return fmt.Errorf("merge: scan source merged_id: %w", scanErr)
		}
		if merged.Valid && merged.String != "" {
			return store.ErrMergeSourceAlreadyMerged
		}
	}
	return rows.Err()
}

// pgMergeAssertTargetUnmerged blocks chained merges (target user must not have
// any contacts whose merged_id points to a different user).
//
// Locks all target-owned contacts FOR UPDATE so a concurrent merge cannot stage
// a chained-merge precondition between this check and our COMMIT (READ COMMITTED
// alone leaves the EXISTS check vulnerable to phantom rows committed after the
// SELECT but before our UPDATE).
func pgMergeAssertTargetUnmerged(ctx context.Context, tx *sql.Tx, target uuid.UUID) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT merged_id FROM channel_contacts
		  WHERE user_id = $1
		  FOR UPDATE`,
		target,
	)
	if err != nil {
		return fmt.Errorf("merge: check target chain: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var merged sql.NullString
		if scanErr := rows.Scan(&merged); scanErr != nil {
			return fmt.Errorf("merge: scan target chain: %w", scanErr)
		}
		if merged.Valid && merged.String != "" && merged.String != target.String() {
			return store.ErrMergeTargetAlreadyMerged
		}
	}
	return rows.Err()
}

// jsonbOrNil returns the input unchanged or "{}" when nil/empty so the
// COALESCE in the UPDATE always casts a real JSONB value.
func jsonbOrNil(b []byte) []byte {
	if len(b) == 0 {
		return []byte(`{}`)
	}
	return b
}
