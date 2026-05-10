package pg

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// PGPendingMessageStore implements store.PendingMessageStore backed by Postgres.
type PGPendingMessageStore struct {
	db *sql.DB
}

// NewPGPendingMessageStore creates a new PGPendingMessageStore.
func NewPGPendingMessageStore(db *sql.DB) *PGPendingMessageStore {
	return &PGPendingMessageStore{db: db}
}

func (s *PGPendingMessageStore) AppendBatch(ctx context.Context, msgs []store.PendingMessage) error {
	if len(msgs) == 0 {
		return nil
	}

	// Build multi-row INSERT: VALUES ($1,$2,...,$10), ($11,$12,...,$20), ...
	const cols = 10
	placeholders := make([]string, len(msgs))
	args := make([]any, 0, len(msgs)*cols)
	now := time.Now()

	for i := range msgs {
		if msgs[i].ID == uuid.Nil {
			msgs[i].ID = uuid.Must(uuid.NewV7())
		}
		base := i * cols
		placeholders[i] = fmt.Sprintf("($%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d)",
			base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8, base+9, base+10)
		args = append(args, msgs[i].ID, msgs[i].ChannelName, msgs[i].HistoryKey,
			msgs[i].Sender, msgs[i].SenderID, msgs[i].Body, msgs[i].PlatformMsgID, msgs[i].IsSummary, now, now)
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO channel_pending_messages (id, channel_name, history_key, sender, sender_id, body, platform_msg_id, is_summary, created_at, updated_at)
		 VALUES `+strings.Join(placeholders, ","),
		args...,
	)
	return err
}

func (s *PGPendingMessageStore) ListByKey(ctx context.Context, channelName, historyKey string) ([]store.PendingMessage, error) {
	var result []store.PendingMessage
	err := pkgSqlxDB.SelectContext(ctx, &result,
		`SELECT id, channel_name, history_key, sender, sender_id, body, platform_msg_id, is_summary, created_at, updated_at
		 FROM channel_pending_messages
		 WHERE channel_name = $1 AND history_key = $2
		 ORDER BY created_at ASC`,
		channelName, historyKey,
	)
	return result, err
}

func (s *PGPendingMessageStore) DeleteByKey(ctx context.Context, channelName, historyKey string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM channel_pending_messages WHERE channel_name = $1 AND history_key = $2`,
		channelName, historyKey,
	)
	return err
}

func (s *PGPendingMessageStore) Compact(ctx context.Context, deleteIDs []uuid.UUID, summary *store.PendingMessage) error {
	if len(deleteIDs) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin compact tx: %w", err)
	}
	defer tx.Rollback()

	placeholders := make([]string, len(deleteIDs))
	args := make([]any, len(deleteIDs))
	for i, id := range deleteIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}

	res, err := tx.ExecContext(ctx,
		fmt.Sprintf("DELETE FROM channel_pending_messages WHERE id IN (%s)", strings.Join(placeholders, ",")),
		args...,
	)
	if err != nil {
		return fmt.Errorf("compact delete: %w", err)
	}

	// Guard: if another compaction already deleted these rows, skip summary insertion
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return nil // already compacted by concurrent caller
	}

	if summary.ID == uuid.Nil {
		summary.ID = uuid.Must(uuid.NewV7())
	}
	now := time.Now()
	_, err = tx.ExecContext(ctx,
		`INSERT INTO channel_pending_messages (id, channel_name, history_key, sender, sender_id, body, platform_msg_id, is_summary, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		summary.ID, summary.ChannelName, summary.HistoryKey, summary.Sender, summary.SenderID,
		summary.Body, summary.PlatformMsgID, true, now, now,
	)
	if err != nil {
		return fmt.Errorf("compact insert summary: %w", err)
	}

	return tx.Commit()
}

func (s *PGPendingMessageStore) DeleteStale(ctx context.Context, olderThan time.Duration) (int64, error) {
	cutoff := time.Now().Add(-olderThan)
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM channel_pending_messages WHERE updated_at < $1`,
		cutoff,
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *PGPendingMessageStore) ListGroups(ctx context.Context) ([]store.PendingMessageGroup, error) {
	var result []store.PendingMessageGroup
	err := pkgSqlxDB.SelectContext(ctx, &result,
		`SELECT channel_name, history_key,
		        COUNT(*) AS message_count,
		        BOOL_OR(is_summary)
		          AND NOT EXISTS (
		            SELECT 1 FROM channel_pending_messages n
		            WHERE n.channel_name = m.channel_name
		              AND n.history_key  = m.history_key
		              AND NOT n.is_summary
		              AND n.created_at > (
		                SELECT MAX(s.created_at)
		                FROM channel_pending_messages s
		                WHERE s.channel_name = m.channel_name
		                  AND s.history_key  = m.history_key
		                  AND s.is_summary
		              )
		          ) AS has_summary,
		        MAX(created_at) AS last_activity
		 FROM channel_pending_messages m
		 GROUP BY channel_name, history_key
		 ORDER BY last_activity DESC`,
	)
	return result, err
}

func (s *PGPendingMessageStore) CountAll(ctx context.Context) (int64, error) {
	var count int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM channel_pending_messages`).Scan(&count)
	return count, err
}

func (s *PGPendingMessageStore) CountByKey(ctx context.Context, channelName, historyKey string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM channel_pending_messages WHERE channel_name = $1 AND history_key = $2`,
		channelName, historyKey,
	).Scan(&count)
	return count, err
}

func (s *PGPendingMessageStore) ResolveGroupTitles(ctx context.Context, groups []store.PendingMessageGroup) (map[string]string, error) {
	if len(groups) == 0 {
		return nil, nil
	}

	// Build OR conditions: session_key LIKE '%:{channel}:group:{key}%'
	conditions := make([]string, 0, len(groups))
	args := make([]any, 0, len(groups)*2)
	for i, g := range groups {
		conditions = append(conditions, fmt.Sprintf(
			"(session_key LIKE '%%:' || $%d || ':group:' || $%d || '%%')",
			i*2+1, i*2+2,
		))
		args = append(args, g.ChannelName, g.HistoryKey)
	}

	rows, err := s.db.QueryContext(ctx,
		"SELECT session_key, metadata->>'chat_title'"+
			" FROM agent_sessions"+
			" WHERE metadata->>'chat_title' != ''"+
			" AND ("+strings.Join(conditions, " OR ")+")",
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]string)
	for rows.Next() {
		var sessionKey, title string
		if err := rows.Scan(&sessionKey, &title); err != nil {
			return nil, err
		}
		for _, g := range groups {
			pattern := ":" + g.ChannelName + ":group:" + g.HistoryKey
			if strings.Contains(sessionKey, pattern) {
				mapKey := g.ChannelName + ":" + g.HistoryKey
				if _, exists := result[mapKey]; !exists {
					result[mapKey] = title
				}
				break
			}
		}
	}
	return result, rows.Err()
}
