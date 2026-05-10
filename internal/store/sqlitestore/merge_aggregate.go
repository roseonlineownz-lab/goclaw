//go:build sqlite || sqliteonly

package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// MergeUserAggregate atomically migrates SourceUserIDs' data into TargetUserID
// across six tables: channel_contacts, memory_documents, agent_config_permissions,
// user_context_files, agent_sessions, and traces.
// All UPDATEs share one *sql.Tx for atomicity. SQLite has no array type; lists
// expand to placeholder strings at query build time.
//
// SQLite write transactions are serialized via its journal lock — no deadlock risk.
// Note: spans are NOT updated here — spans.user_id column does not exist in the
// current schema. Spans inherit user attribution via their parent trace.
func (s *SQLiteContactStore) MergeUserAggregate(ctx context.Context, req store.MergeUserAggregateRequest) error {
	if len(req.ContactIDs) == 0 {
		return fmt.Errorf("merge: contact_ids required")
	}
	if req.TargetUserID == uuid.Nil {
		return fmt.Errorf("merge: target_user_id required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("merge: begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if err := sqliteMergeAssertTargetExists(ctx, tx, req.TargetUserID); err != nil {
		return err
	}
	// Reject requests where ContactIDs contain UUIDs not present in DB.
	// Without this check a caller can supply fabricated UUIDs and the UPDATE
	// silently matches zero rows — a vacuous-pass that bypasses the merge audit.
	if err := sqliteMergeAssertContactIDsExist(ctx, tx, req.ContactIDs); err != nil {
		return err
	}
	if err := sqliteMergeAssertSourceUnmerged(ctx, tx, req.ContactIDs); err != nil {
		return err
	}
	if err := sqliteMergeAssertTargetUnmerged(ctx, tx, req.TargetUserID); err != nil {
		return err
	}

	// Collect group contact IDs for post-commit FS relocation callback.
	var groupContactIDs []uuid.UUID
	if req.OnGroupContactsMerged != nil {
		groupContactIDs, err = sqliteFetchGroupContactIDs(ctx, tx, req.ContactIDs)
		if err != nil {
			return err
		}
	}

	auditBlob := req.MergeAudit
	if len(auditBlob) == 0 {
		auditBlob = []byte(`{}`)
	}

	// Step 1: stamp contacts with merged_id.
	contactQ, contactArgs := buildInListQuery(
		"UPDATE channel_contacts SET merged_id = ?, merge_audit = ? WHERE id IN (",
		req.ContactIDs,
		req.TargetUserID, string(auditBlob),
	)
	if _, err := tx.ExecContext(ctx, contactQ, contactArgs...); err != nil {
		return fmt.Errorf("merge: update channel_contacts: %w", err)
	}

	if len(req.SourceUserIDs) > 0 {
		// Step 2a: memory_documents keyed by user_id.
		memQ, memArgs := buildInListQuery(
			"UPDATE memory_documents SET user_id = ? WHERE user_id IN (",
			req.SourceUserIDs,
			req.TargetUserID,
		)
		if _, err := tx.ExecContext(ctx, memQ, memArgs...); err != nil {
			return fmt.Errorf("merge: update memory_documents (user_id): %w", err)
		}

		// Step 3: migrate agent_config_permissions.
		// SQLite stores user_id as TEXT; expand source UUIDs to string placeholders.
		sourceStrs := make([]uuid.UUID, len(req.SourceUserIDs))
		copy(sourceStrs, req.SourceUserIDs)
		if err := sqliteMigrateConfigPermissions(ctx, tx, sourceStrs, req.TargetUserID); err != nil {
			return fmt.Errorf("merge: %w", err)
		}

		// Step 4: migrate user_context_files.
		ucfQ, ucfArgs := buildInListQuery(
			"UPDATE user_context_files SET user_id = ? WHERE user_id IN (",
			req.SourceUserIDs,
			req.TargetUserID,
		)
		if _, err := tx.ExecContext(ctx, ucfQ, ucfArgs...); err != nil {
			return fmt.Errorf("merge: update user_context_files: %w", err)
		}

		// Step 5: migrate agent_sessions.
		sessQ, sessArgs := buildInListQuery(
			"UPDATE agent_sessions SET user_id = ? WHERE user_id IN (",
			req.SourceUserIDs,
			req.TargetUserID,
		)
		if _, err := tx.ExecContext(ctx, sessQ, sessArgs...); err != nil {
			return fmt.Errorf("merge: update agent_sessions: %w", err)
		}
	}

	// Step 2b: memory_documents 5D scope rows keyed by contact_id only (user_id IS NULL).
	if len(req.ContactIDs) > 0 {
		memCQ, memCArgs := buildInListQuery(
			"UPDATE memory_documents SET user_id = ?, contact_id = NULL WHERE contact_id IN (",
			req.ContactIDs,
			req.TargetUserID,
		)
		memCQ += " AND user_id IS NULL"
		if _, err := tx.ExecContext(ctx, memCQ, memCArgs...); err != nil {
			return fmt.Errorf("merge: update memory_documents (contact_id): %w", err)
		}

		// Step 6: migrate traces (contact_id axis).
		traceQ, traceArgs := buildInListQuery(
			"UPDATE traces SET user_id = ? WHERE contact_id IN (",
			req.ContactIDs,
			req.TargetUserID,
		)
		if _, err := tx.ExecContext(ctx, traceQ, traceArgs...); err != nil {
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

	// Post-commit: invoke caller's FS relocation callback for group contacts.
	if req.OnGroupContactsMerged != nil && len(groupContactIDs) > 0 {
		req.OnGroupContactsMerged(groupContactIDs)
	}

	return nil
}

// sqliteMigrateConfigPermissions re-keys agent_config_permissions rows from
// sourceUserIDs to targetUserID within the provided SQLite transaction.
// Mirrors the logic in permissions.MigrateConfigPermissionsForMerge for SQLite.
func sqliteMigrateConfigPermissions(ctx context.Context, tx *sql.Tx, sourceUserIDs []uuid.UUID, targetUserID uuid.UUID) error {
	if len(sourceUserIDs) == 0 {
		return nil
	}
	targetStr := targetUserID.String()

	// Update rows that won't collide with an existing target grant.
	updateQ, updateArgs := buildStringInListQuery(
		`UPDATE agent_config_permissions SET user_id = ? WHERE user_id IN (`,
		uuidsToStrings(sourceUserIDs),
		targetStr,
	)
	updateQ += ` AND NOT EXISTS (
		SELECT 1 FROM agent_config_permissions e
		 WHERE e.agent_id    = agent_config_permissions.agent_id
		   AND e.scope       = agent_config_permissions.scope
		   AND e.config_type = agent_config_permissions.config_type
		   AND e.user_id     = ?
	)`
	updateArgs = append(updateArgs, targetStr)
	if _, err := tx.ExecContext(ctx, updateQ, updateArgs...); err != nil {
		return fmt.Errorf("migrate config permissions: update non-conflicting: %w", err)
	}

	// Delete residual source rows that could not be re-keyed (duplicate grant exists for target).
	deleteQ, deleteArgs := buildStringInListQuery(
		`DELETE FROM agent_config_permissions WHERE user_id IN (`,
		uuidsToStrings(sourceUserIDs),
	)
	if _, err := tx.ExecContext(ctx, deleteQ, deleteArgs...); err != nil {
		return fmt.Errorf("migrate config permissions: delete residual source rows: %w", err)
	}

	return nil
}

// sqliteFetchGroupContactIDs returns contact UUIDs from the input set whose
// peer_kind = 'group'.
func sqliteFetchGroupContactIDs(ctx context.Context, tx *sql.Tx, contactIDs []uuid.UUID) ([]uuid.UUID, error) {
	q, args := buildInListQuery(
		"SELECT id FROM channel_contacts WHERE peer_kind = 'group' AND id IN (",
		contactIDs,
	)
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("merge: fetch group contacts: %w", err)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("merge: scan group contact id: %w", err)
		}
		parsed, err := uuid.Parse(id)
		if err != nil {
			return nil, fmt.Errorf("merge: parse group contact id: %w", err)
		}
		out = append(out, parsed)
	}
	return out, rows.Err()
}

func sqliteMergeAssertTargetExists(ctx context.Context, tx *sql.Tx, target uuid.UUID) error {
	var ok int
	err := tx.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM users WHERE id = ? AND deleted_at IS NULL)`, target,
	).Scan(&ok)
	if err != nil {
		return fmt.Errorf("merge: check target: %w", err)
	}
	if ok == 0 {
		return store.ErrMergeTargetUserNotFound
	}
	return nil
}

// sqliteMergeAssertContactIDsExist checks that every UUID in contactIDs corresponds
// to an existing channel_contacts row.
func sqliteMergeAssertContactIDsExist(ctx context.Context, tx *sql.Tx, contactIDs []uuid.UUID) error {
	q, args := buildInListQuery(
		"SELECT COUNT(*) FROM channel_contacts WHERE id IN (",
		contactIDs,
	)
	var count int
	if err := tx.QueryRowContext(ctx, q, args...).Scan(&count); err != nil {
		return fmt.Errorf("merge: count contact_ids: %w", err)
	}
	if count != len(contactIDs) {
		return store.ErrContactIDNotFound
	}
	return nil
}

func sqliteMergeAssertSourceUnmerged(ctx context.Context, tx *sql.Tx, contactIDs []uuid.UUID) error {
	q, args := buildInListQuery(
		"SELECT 1 FROM channel_contacts WHERE merged_id IS NOT NULL AND id IN (",
		contactIDs,
	)
	q += " LIMIT 1"
	row := tx.QueryRowContext(ctx, q, args...)
	var x int
	err := row.Scan(&x)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("merge: check source: %w", err)
	}
	return store.ErrMergeSourceAlreadyMerged
}

// sqliteMergeAssertTargetUnmerged blocks chained merges. SQLite serializes
// writes via its journal lock (BEGIN takes implicit write intent), so we don't
// need FOR UPDATE — any concurrent merge waits on the journal.
func sqliteMergeAssertTargetUnmerged(ctx context.Context, tx *sql.Tx, target uuid.UUID) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT merged_id FROM channel_contacts WHERE user_id = ?`,
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

// buildInListQuery expands a UUID slice into ?,?,? placeholders and prepends
// any leading positional args before the IN list. Final query gets a closing ")".
func buildInListQuery(prefix string, ids []uuid.UUID, leadArgs ...any) (string, []any) {
	placeholders := make([]string, len(ids))
	args := make([]any, 0, len(leadArgs)+len(ids))
	args = append(args, leadArgs...)
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	return prefix + strings.Join(placeholders, ",") + ")", args
}

// buildStringInListQuery expands a string slice into ?,?,? placeholders.
func buildStringInListQuery(prefix string, strs []string, leadArgs ...any) (string, []any) {
	placeholders := make([]string, len(strs))
	args := make([]any, 0, len(leadArgs)+len(strs))
	args = append(args, leadArgs...)
	for i, s := range strs {
		placeholders[i] = "?"
		args = append(args, s)
	}
	return prefix + strings.Join(placeholders, ",") + ")", args
}

// uuidsToStrings converts a UUID slice to string slice for string-typed DB columns.
func uuidsToStrings(ids []uuid.UUID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}
