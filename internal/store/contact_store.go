package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ChannelContact represents a user discovered through channel interactions.
// Global (not per-agent): same person on the same platform = one row.
type ChannelContact struct {
	ID               uuid.UUID  `json:"id" db:"id"`
	ChannelType      string     `json:"channel_type" db:"channel_type"`
	ChannelInstance  *string    `json:"channel_instance,omitempty" db:"channel_instance"`
	SenderID         string     `json:"sender_id" db:"sender_id"`
	UserID           *string    `json:"user_id,omitempty" db:"user_id"`
	DisplayName      *string    `json:"display_name,omitempty" db:"display_name"`
	Username         *string    `json:"username,omitempty" db:"username"`
	AvatarURL        *string    `json:"avatar_url,omitempty" db:"avatar_url"`
	PeerKind         *string    `json:"peer_kind,omitempty" db:"peer_kind"`
	ContactType      string     `json:"contact_type" db:"contact_type"` // "user", "group", or "topic"
	ThreadID         *string    `json:"thread_id,omitempty" db:"thread_id"`
	ThreadType       *string    `json:"thread_type,omitempty" db:"thread_type"`
	MergedID         *uuid.UUID `json:"merged_id,omitempty" db:"merged_id"`
	DefaultProjectID *uuid.UUID `json:"default_project_id,omitempty" db:"default_project_id"`
	FirstSeenAt      time.Time  `json:"first_seen_at" db:"first_seen_at"`
	LastSeenAt       time.Time  `json:"last_seen_at" db:"last_seen_at"`
}

// ContactListOpts holds pagination and filter options for listing contacts.
type ContactListOpts struct {
	Search      string // ILIKE on display_name, username, sender_id
	ChannelType string // filter by platform (telegram, discord, etc.)
	PeerKind    string // "direct" or "group"
	ContactType string // "user" or "group"
	Limit       int
	Offset      int
}

// MergeUserAggregateRequest carries the parameters for an atomic merge.
//
// The merge consolidates `SourceUserIDs`' data into `TargetUserID` across six tables:
//
//  1. UPDATE channel_contacts SET merged_id = TargetUserID, merge_audit = MergeAudit
//     WHERE id = ANY(ContactIDs).
//  2. UPDATE memory_documents SET user_id = TargetUserID
//     WHERE user_id = ANY(SourceUserIDs) OR (contact_id = ANY(ContactIDs) AND user_id IS NULL).
//  3. agent_config_permissions: user_id rows for SourceUserIDs flip to TargetUserID
//     (delegated to permissions.MigrateConfigPermissionsForMerge — no inline SQL).
//  4. UPDATE user_context_files SET user_id = TargetUserID
//     WHERE user_id = ANY(SourceUserIDs).
//  5. UPDATE agent_sessions SET user_id = TargetUserID
//     WHERE user_id = ANY(SourceUserIDs).
//  6. UPDATE traces SET user_id = TargetUserID WHERE contact_id = ANY(ContactIDs).
//
// spans are NOT updated: spans.user_id does not exist in the current schema.
// Spans inherit user attribution via their parent trace.
//
// All UPDATEs share a single *sql.Tx to guarantee atomicity.
//
// OnGroupContactsMerged, if non-nil, is invoked post-commit (outside the TX) with
// the UUIDs of contacts whose peer_kind = 'group'. Callers use this hook to trigger
// best-effort FS workspace relocation — failure must never fail the merge itself.
type MergeUserAggregateRequest struct {
	ContactIDs    []uuid.UUID // channel_contacts.id rows whose merged_id must flip to TargetUserID
	SourceUserIDs []uuid.UUID // users.id values whose data must move to TargetUserID
	TargetUserID  uuid.UUID   // destination user (must exist + must NOT be a chained merge)
	MergeAudit    []byte      // JSONB blob {merged_by_user_id, merged_at, from_channel_id, ...}

	// OnGroupContactsMerged is an optional post-commit callback for group-contact
	// FS workspace relocation. Called after TX.COMMIT with UUIDs of group contacts
	// (peer_kind = 'group') from ContactIDs. Never called on rollback.
	// Implementation must be best-effort and must not panic.
	OnGroupContactsMerged func(groupContactIDs []uuid.UUID)
}

// Sentinel errors for merge pre-check failures (security).
var (
	ErrMergeSourceAlreadyMerged = errors.New("source contact already merged — user→user merge forbidden")
	ErrMergeTargetAlreadyMerged = errors.New("target user already merged into another — chained merges forbidden")
	ErrMergeTargetUserNotFound  = errors.New("target user not found")
)

// Sentinel errors for contact lookup failures.
var (
	// ErrContactNotFound is returned when no channel_contacts row matches the lookup key.
	ErrContactNotFound = errors.New("contact not found")
	// ErrContactIDNotFound is returned when a contact exists but has no canonical DM for the requested channel.
	ErrContactIDNotFound = errors.New("contact canonical DM not found")
)

// ContactStore manages channel contacts (auto-collected user info).
type ContactStore interface {
	// UpsertContact creates or updates a contact. On conflict (channel_type, sender_id, thread_id),
	// updates display_name, username, user_id, channel_instance, peer_kind, thread_type, last_seen_at.
	UpsertContact(ctx context.Context, channelType, channelInstance, senderID, userID, displayName, username, peerKind, contactType, threadID, threadType string) error

	// ListContacts searches contacts with pagination and filters.
	ListContacts(ctx context.Context, opts ContactListOpts) ([]ChannelContact, error)

	// CountContacts returns total matching contacts for the given filters.
	CountContacts(ctx context.Context, opts ContactListOpts) (int, error)

	// GetContactsBySenderIDs returns contacts matching the given sender IDs.
	GetContactsBySenderIDs(ctx context.Context, senderIDs []string) (map[string]ChannelContact, error)

	// GetContactByID returns a single contact by primary key.
	GetContactByID(ctx context.Context, id uuid.UUID) (*ChannelContact, error)

	// GetSenderIDsByContactIDs returns sender_id strings for the given contact UUIDs in one query.
	GetSenderIDsByContactIDs(ctx context.Context, contactIDs []uuid.UUID) ([]string, error)

	// MergeUserAggregate atomically migrates all data from SourceUserIDs to TargetUserID
	// across six tables: channel_contacts, memory_documents, agent_config_permissions,
	// user_context_files, agent_sessions, traces — all in a single TX.
	// Returns ErrContactIDNotFound when any ContactID is absent from the DB.
	// Returns ErrMergeSourceAlreadyMerged / ErrMergeTargetAlreadyMerged / ErrMergeTargetUserNotFound
	// on pre-check failures.
	//
	// Separation invariant: this method operates exclusively on the tables listed above.
	// It never reads or writes paired_devices. Device binding (PairingStore.BindUser)
	// is an orthogonal, per-device operation that admins control independently.
	MergeUserAggregate(ctx context.Context, req MergeUserAggregateRequest) error

	// ResolveTenantUserID looks up a contact by (channelType, senderID) and, if
	// the contact has been merged, returns the linked user's UUID as string.
	// Returns ("", nil) when the contact is not found or not merged.
	ResolveTenantUserID(ctx context.Context, channelType, senderID string) (string, error)

	// UpdateDefaultProject sets or clears the default project binding for a channel contact.
	// Pass nil projectID to clear the binding. Callers must verify permission before calling.
	UpdateDefaultProject(ctx context.Context, contactID uuid.UUID, projectID *uuid.UUID) error

	// GetContactByChannelAndChatID returns the contact matching (channel_type, sender_id).
	// Returns ErrContactNotFound when no row exists — caller falls back to msg.ChatID.
	// Composite-key lookup: no ContactID on OutboundMessage; lookup at dispatch time costs +1 DB read.
	GetContactByChannelAndChatID(ctx context.Context, channelType, chatID string) (*ChannelContact, error)

	// GetCanonicalDMContact returns the active (unmerged) DM contact for a user on a given channel.
	// Used to re-route outbound replies when the original sender contact has been merged.
	// Returns ErrContactIDNotFound when no canonical DM exists — caller falls back to original chat_id.
	GetCanonicalDMContact(ctx context.Context, userID uuid.UUID, channelType string) (*ChannelContact, error)
}
