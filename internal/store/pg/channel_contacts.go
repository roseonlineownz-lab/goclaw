package pg

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)


// PGContactStore implements store.ContactStore backed by Postgres.
type PGContactStore struct {
	db           *sql.DB
	resolveCache *contactResolveCache // tenant-user resolution cache (60s TTL)
}

// NewPGContactStore creates a new PGContactStore.
func NewPGContactStore(db *sql.DB) *PGContactStore {
	return &PGContactStore{db: db, resolveCache: newContactResolveCache()}
}

func (s *PGContactStore) UpsertContact(ctx context.Context, channelType, channelInstance, senderID, userID, displayName, username, peerKind, contactType, threadID, threadType string) error {
	tenantID := store.TenantIDFromContext(ctx)
	if tenantID == uuid.Nil {
		tenantID = store.MasterTenantID
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO channel_contacts (channel_type, channel_instance, sender_id, user_id, display_name, username, peer_kind, contact_type, thread_id, thread_type, tenant_id)
		VALUES ($1, NULLIF($2,''), $3, NULLIF($4,''), NULLIF($5,''), NULLIF($6,''), NULLIF($7,''), $8, NULLIF($9,''), NULLIF($10,''), $11)
		ON CONFLICT (tenant_id, channel_type, sender_id, COALESCE(thread_id, '')) DO UPDATE SET
			display_name     = COALESCE(NULLIF($5,''), channel_contacts.display_name),
			username         = COALESCE(NULLIF($6,''), channel_contacts.username),
			user_id          = COALESCE(NULLIF($4,''), channel_contacts.user_id),
			channel_instance = COALESCE(NULLIF($2,''), channel_contacts.channel_instance),
			peer_kind        = COALESCE(NULLIF($7,''), channel_contacts.peer_kind),
			contact_type     = $8,
			thread_type      = COALESCE(NULLIF($10,''), channel_contacts.thread_type),
			last_seen_at     = NOW()`,
		channelType, channelInstance, senderID, userID, displayName, username, peerKind, contactType, threadID, threadType, tenantID,
	)
	return err
}

func contactWhereClause(ctx context.Context, opts store.ContactListOpts) (string, []any, int) {
	var conditions []string
	var args []any
	argIdx := 1

	if !store.IsCrossTenant(ctx) {
		tenantID := store.TenantIDFromContext(ctx)
		if tenantID != uuid.Nil {
			conditions = append(conditions, fmt.Sprintf("tenant_id = $%d", argIdx))
			args = append(args, tenantID)
			argIdx++
		}
	}

	if opts.ChannelType != "" {
		conditions = append(conditions, fmt.Sprintf("channel_type = $%d", argIdx))
		args = append(args, opts.ChannelType)
		argIdx++
	}
	if opts.PeerKind != "" {
		conditions = append(conditions, fmt.Sprintf("peer_kind = $%d", argIdx))
		args = append(args, opts.PeerKind)
		argIdx++
	}
	if opts.ContactType != "" {
		conditions = append(conditions, fmt.Sprintf("contact_type = $%d", argIdx))
		args = append(args, opts.ContactType)
		argIdx++
	}
	if opts.Search != "" {
		escaped := strings.NewReplacer("%", "\\%", "_", "\\_").Replace(opts.Search)
		pattern := escaped + "%"
		conditions = append(conditions, fmt.Sprintf(
			"(display_name ILIKE $%d ESCAPE '\\' OR username ILIKE $%d ESCAPE '\\' OR sender_id ILIKE $%d ESCAPE '\\')",
			argIdx, argIdx, argIdx,
		))
		args = append(args, pattern)
		argIdx++
	}

	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}
	return where, args, argIdx
}

func (s *PGContactStore) ListContacts(ctx context.Context, opts store.ContactListOpts) ([]store.ChannelContact, error) {
	where, args, argIdx := contactWhereClause(ctx, opts)

	query := `SELECT id, channel_type, channel_instance, sender_id, user_id,
		display_name, username, avatar_url, peer_kind, contact_type, thread_id, thread_type, merged_id,
		first_seen_at, last_seen_at
		FROM channel_contacts` + where + " ORDER BY last_seen_at DESC"

	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	query += fmt.Sprintf(" LIMIT $%d", argIdx)
	args = append(args, limit)
	argIdx++

	if opts.Offset > 0 {
		query += fmt.Sprintf(" OFFSET $%d", argIdx)
		args = append(args, opts.Offset)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var contacts []store.ChannelContact
	for rows.Next() {
		var c store.ChannelContact
		if err := rows.Scan(
			&c.ID, &c.ChannelType, &c.ChannelInstance, &c.SenderID, &c.UserID,
			&c.DisplayName, &c.Username, &c.AvatarURL, &c.PeerKind, &c.ContactType, &c.ThreadID, &c.ThreadType, &c.MergedID,
			&c.FirstSeenAt, &c.LastSeenAt,
		); err != nil {
			return nil, err
		}
		contacts = append(contacts, c)
	}
	return contacts, rows.Err()
}

func (s *PGContactStore) CountContacts(ctx context.Context, opts store.ContactListOpts) (int, error) {
	where, args, _ := contactWhereClause(ctx, opts)
	var count int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM channel_contacts"+where, args...).Scan(&count)
	return count, err
}

func (s *PGContactStore) GetContactsBySenderIDs(ctx context.Context, senderIDs []string) (map[string]store.ChannelContact, error) {
	if len(senderIDs) == 0 {
		return map[string]store.ChannelContact{}, nil
	}

	placeholders := make([]string, len(senderIDs))
	args := make([]any, len(senderIDs))
	for i, id := range senderIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}

	query := fmt.Sprintf(`SELECT DISTINCT ON (sender_id)
		id, channel_type, channel_instance, sender_id, user_id,
		display_name, username, avatar_url, peer_kind, contact_type, thread_id, thread_type, merged_id,
		first_seen_at, last_seen_at
		FROM channel_contacts
		WHERE sender_id IN (%s)
		ORDER BY sender_id, last_seen_at DESC`, strings.Join(placeholders, ","))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]store.ChannelContact, len(senderIDs))
	for rows.Next() {
		var c store.ChannelContact
		if err := rows.Scan(
			&c.ID, &c.ChannelType, &c.ChannelInstance, &c.SenderID, &c.UserID,
			&c.DisplayName, &c.Username, &c.AvatarURL, &c.PeerKind, &c.ContactType, &c.ThreadID, &c.ThreadType, &c.MergedID,
			&c.FirstSeenAt, &c.LastSeenAt,
		); err != nil {
			return nil, err
		}
		result[c.SenderID] = c
	}
	return result, rows.Err()
}

func (s *PGContactStore) GetContactByID(ctx context.Context, id uuid.UUID) (*store.ChannelContact, error) {
	tid := store.TenantIDFromContext(ctx)
	row := s.db.QueryRowContext(ctx,
		`SELECT id, channel_type, channel_instance, sender_id, user_id,
			display_name, username, avatar_url, peer_kind, contact_type,
			thread_id, thread_type, merged_id,
			first_seen_at, last_seen_at
		FROM channel_contacts WHERE id = $1 AND tenant_id = $2`, id, tid)
	var c store.ChannelContact
	if err := row.Scan(
		&c.ID, &c.ChannelType, &c.ChannelInstance, &c.SenderID, &c.UserID,
		&c.DisplayName, &c.Username, &c.AvatarURL, &c.PeerKind, &c.ContactType,
		&c.ThreadID, &c.ThreadType, &c.MergedID,
		&c.FirstSeenAt, &c.LastSeenAt,
	); err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *PGContactStore) GetSenderIDsByContactIDs(ctx context.Context, contactIDs []uuid.UUID) ([]string, error) {
	if len(contactIDs) == 0 {
		return nil, nil
	}
	tid := store.TenantIDFromContext(ctx)
	placeholders := make([]string, len(contactIDs))
	args := make([]any, len(contactIDs)+1)
	for i, id := range contactIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}
	args[len(contactIDs)] = tid
	q := fmt.Sprintf("SELECT sender_id FROM channel_contacts WHERE id IN (%s) AND tenant_id = $%d",
		strings.Join(placeholders, ","), len(contactIDs)+1)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var sid string
		if err := rows.Scan(&sid); err != nil {
			return nil, err
		}
		result = append(result, sid)
	}
	return result, rows.Err()
}

// GetContactByChannelAndChatID returns the channel_contacts row for (channel_type, sender_id).
// Returns store.ErrContactNotFound when no row matches.
func (s *PGContactStore) GetContactByChannelAndChatID(ctx context.Context, channelType, chatID string) (*store.ChannelContact, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+contactSelectCols+`
		FROM channel_contacts
		WHERE channel_type = $1 AND sender_id = $2
		LIMIT 1`,
		channelType, chatID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, store.ErrContactNotFound
	}
	c, err := scanPGContact(rows)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// GetCanonicalDMContact returns the most-recently-seen unmerged DM contact for the
// given user on a given channel. Used to re-route outbound replies after contact merge.
// Returns store.ErrContactIDNotFound when no qualifying row exists.
func (s *PGContactStore) GetCanonicalDMContact(ctx context.Context, userID uuid.UUID, channelType string) (*store.ChannelContact, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+contactSelectCols+`
		FROM channel_contacts
		WHERE user_id = $1 AND channel_type = $2 AND peer_kind = 'direct' AND merged_id IS NULL
		ORDER BY last_seen_at DESC
		LIMIT 1`,
		userID, channelType,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, store.ErrContactIDNotFound
	}
	c, err := scanPGContact(rows)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// Note: MergeContacts/UnmergeContacts/GetContactsByMergedID were removed in v4.
// The single MergeUserAggregate method (see merge_aggregate.go) is the only
// sanctioned merge entry point — it owns one *sql.Tx covering channel_contacts
// + agent_sessions + user_context_files + memory_documents for atomic semantics.
