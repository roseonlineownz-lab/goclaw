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

// upsertContactSQL performs the INSERT … ON CONFLICT upsert.
// SQLite has no DEFAULT uuid_generate_v7(); the caller must supply the id
// for the INSERT branch. The ON CONFLICT branch ignores the supplied id
// (it already exists) and only updates mutable columns.

// SQLiteContactStore implements store.ContactStore backed by SQLite.
type SQLiteContactStore struct {
	db *sql.DB
}

func NewSQLiteContactStore(db *sql.DB) *SQLiteContactStore {
	return &SQLiteContactStore{db: db}
}

func (s *SQLiteContactStore) UpsertContact(ctx context.Context, channelType, channelInstance, senderID, userID, displayName, username, peerKind, contactType, threadID, threadType string) error {
	// Generate a UUID v7 for the INSERT branch. SQLite has no server-side UUID
	// default (unlike PG's uuid_generate_v7()), so the Go layer must supply it.
	// The ON CONFLICT branch ignores this value — existing rows keep their id.
	newID := uuid.Must(uuid.NewV7()).String()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO channel_contacts (id, channel_type, channel_instance, sender_id, user_id, display_name, username, peer_kind, contact_type, thread_id, thread_type)
		VALUES (?, ?, NULLIF(?,?), ?, NULLIF(?,?), NULLIF(?,?), NULLIF(?,?), NULLIF(?,?), ?, NULLIF(?,?), NULLIF(?,?))
		ON CONFLICT (channel_type, sender_id, COALESCE(thread_id, '')) DO UPDATE SET
			display_name     = COALESCE(NULLIF(excluded.display_name,''), channel_contacts.display_name),
			username         = COALESCE(NULLIF(excluded.username,''), channel_contacts.username),
			user_id          = COALESCE(NULLIF(excluded.user_id,''), channel_contacts.user_id),
			channel_instance = COALESCE(NULLIF(excluded.channel_instance,''), channel_contacts.channel_instance),
			peer_kind        = COALESCE(NULLIF(excluded.peer_kind,''), channel_contacts.peer_kind),
			contact_type     = excluded.contact_type,
			thread_type      = COALESCE(NULLIF(excluded.thread_type,''), channel_contacts.thread_type),
			last_seen_at     = CURRENT_TIMESTAMP`,
		newID,
		channelType,
		channelInstance, "",
		senderID,
		userID, "",
		displayName, "",
		username, "",
		peerKind, "",
		contactType,
		threadID, "",
		threadType, "",
	)
	return err
}

func contactWhereSQLite(_ context.Context, opts store.ContactListOpts) (string, []any) {
	var conditions []string
	var args []any

	if opts.ChannelType != "" {
		conditions = append(conditions, "channel_type = ?")
		args = append(args, opts.ChannelType)
	}
	if opts.PeerKind != "" {
		conditions = append(conditions, "peer_kind = ?")
		args = append(args, opts.PeerKind)
	}
	if opts.ContactType != "" {
		conditions = append(conditions, "contact_type = ?")
		args = append(args, opts.ContactType)
	}
	if opts.Search != "" {
		escaped := strings.NewReplacer("%", "\\%", "_", "\\_").Replace(opts.Search)
		pattern := escaped + "%"
		conditions = append(conditions, "(display_name LIKE ? ESCAPE '\\' OR username LIKE ? ESCAPE '\\' OR sender_id LIKE ? ESCAPE '\\')")
		args = append(args, pattern, pattern, pattern)
	}

	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}
	return where, args
}

const contactSelectCols = `id, channel_type, channel_instance, sender_id, user_id,
		display_name, username, avatar_url, peer_kind, contact_type, thread_id, thread_type, merged_id,
		default_project_id, first_seen_at, last_seen_at`

func scanContact(rows *sql.Rows) (store.ChannelContact, error) {
	var c store.ChannelContact
	firstSeen, lastSeen := &sqliteTime{}, &sqliteTime{}
	err := rows.Scan(
		&c.ID, &c.ChannelType, &c.ChannelInstance, &c.SenderID, &c.UserID,
		&c.DisplayName, &c.Username, &c.AvatarURL, &c.PeerKind, &c.ContactType,
		&c.ThreadID, &c.ThreadType, &c.MergedID,
		&c.DefaultProjectID,
		firstSeen, lastSeen,
	)
	if err == nil {
		c.FirstSeenAt = firstSeen.Time
		c.LastSeenAt = lastSeen.Time
	}
	return c, err
}

func (s *SQLiteContactStore) ListContacts(ctx context.Context, opts store.ContactListOpts) ([]store.ChannelContact, error) {
	where, args := contactWhereSQLite(ctx, opts)

	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	query := `SELECT ` + contactSelectCols + ` FROM channel_contacts` + where +
		fmt.Sprintf(" ORDER BY last_seen_at DESC LIMIT %d", limit)
	if opts.Offset > 0 {
		query += fmt.Sprintf(" OFFSET %d", opts.Offset)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var contacts []store.ChannelContact
	for rows.Next() {
		c, err := scanContact(rows)
		if err != nil {
			return nil, err
		}
		contacts = append(contacts, c)
	}
	return contacts, rows.Err()
}

func (s *SQLiteContactStore) CountContacts(ctx context.Context, opts store.ContactListOpts) (int, error) {
	where, args := contactWhereSQLite(ctx, opts)
	var count int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM channel_contacts"+where, args...).Scan(&count)
	return count, err
}

func (s *SQLiteContactStore) GetContactsBySenderIDs(ctx context.Context, senderIDs []string) (map[string]store.ChannelContact, error) {
	if len(senderIDs) == 0 {
		return map[string]store.ChannelContact{}, nil
	}

	placeholders := make([]string, len(senderIDs))
	args := make([]any, len(senderIDs))
	for i, id := range senderIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	// SQLite has no DISTINCT ON; emulate with GROUP BY + MAX rowid trick via subquery
	query := `SELECT ` + contactSelectCols + `
		FROM channel_contacts
		WHERE sender_id IN (` + strings.Join(placeholders, ",") + `)
		GROUP BY sender_id
		ORDER BY sender_id, last_seen_at DESC`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]store.ChannelContact, len(senderIDs))
	for rows.Next() {
		c, err := scanContact(rows)
		if err != nil {
			return nil, err
		}
		result[c.SenderID] = c
	}
	return result, rows.Err()
}

func (s *SQLiteContactStore) GetContactByID(ctx context.Context, id uuid.UUID) (*store.ChannelContact, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+contactSelectCols+`
		FROM channel_contacts WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	c, err := scanContact(rows)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// UpdateDefaultProject sets or clears the default_project_id on a channel contact.
// Pass nil to clear the binding. Permission check is the caller's responsibility.
func (s *SQLiteContactStore) UpdateDefaultProject(ctx context.Context, contactID uuid.UUID, projectID *uuid.UUID) error {
	if projectID == nil {
		_, err := s.db.ExecContext(ctx,
			`UPDATE channel_contacts SET default_project_id = NULL WHERE id = ?`,
			contactID,
		)
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE channel_contacts SET default_project_id = ? WHERE id = ?`,
		*projectID, contactID,
	)
	return err
}

func (s *SQLiteContactStore) GetSenderIDsByContactIDs(ctx context.Context, contactIDs []uuid.UUID) ([]string, error) {
	if len(contactIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(contactIDs))
	args := make([]any, len(contactIDs))
	for i, id := range contactIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	q := fmt.Sprintf("SELECT sender_id FROM channel_contacts WHERE id IN (%s)",
		strings.Join(placeholders, ","))
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
func (s *SQLiteContactStore) GetContactByChannelAndChatID(ctx context.Context, channelType, chatID string) (*store.ChannelContact, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+contactSelectCols+`
		FROM channel_contacts
		WHERE channel_type = ? AND sender_id = ?
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
	c, err := scanContact(rows)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// GetCanonicalDMContact returns the most-recently-seen unmerged DM contact for the
// given user on a given channel. Used to re-route outbound replies after contact merge.
// Returns store.ErrContactIDNotFound when no qualifying row exists.
func (s *SQLiteContactStore) GetCanonicalDMContact(ctx context.Context, userID uuid.UUID, channelType string) (*store.ChannelContact, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+contactSelectCols+`
		FROM channel_contacts
		WHERE user_id = ? AND channel_type = ? AND peer_kind = 'direct' AND merged_id IS NULL
		ORDER BY last_seen_at DESC
		LIMIT 1`,
		userID.String(), channelType,
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
	c, err := scanContact(rows)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// ResolveTenantUserID returns the merged user UUID (as string) for a given
// (channelType, senderID) lookup, or "" if the contact is missing or unmerged.
// SQLite stores UUIDs as TEXT — direct read, no JOIN needed.
func (s *SQLiteContactStore) ResolveTenantUserID(ctx context.Context, channelType, senderID string) (string, error) {
	if channelType == "" || senderID == "" {
		return "", nil
	}
	var merged string
	err := s.db.QueryRowContext(ctx,
		`SELECT merged_id FROM channel_contacts
		  WHERE channel_type = ? AND sender_id = ? AND merged_id IS NOT NULL
		  LIMIT 1`,
		channelType, senderID,
	).Scan(&merged)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return merged, err
}
