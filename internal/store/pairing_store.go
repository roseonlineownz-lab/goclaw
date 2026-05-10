package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// ErrPairingBoundToDifferentUser is returned by BindUser when the paired
// device is already bound to a different authenticated user. Prevents silent
// hijack via SenderID collision across user accounts.
var ErrPairingBoundToDifferentUser = errors.New("paired device already bound to different user")

// PairingRequest represents a pending pairing code.
type PairingRequestData struct {
	Code      string            `json:"code" db:"code"`
	SenderID  string            `json:"sender_id" db:"sender_id"`
	Channel   string            `json:"channel" db:"channel"`
	ChatID    string            `json:"chat_id" db:"chat_id"`
	AccountID string            `json:"account_id" db:"account_id"`
	CreatedAt int64             `json:"created_at" db:"created_at"`
	ExpiresAt int64             `json:"expires_at" db:"expires_at"`
	Metadata  map[string]string `json:"metadata,omitempty" db:"metadata"`
}

// PairedDeviceData represents an approved pairing. v4: UserID is the
// resolved user UUID once the device has been bound to an authenticated user
// (nil pre-bind, populated after BindUser).
type PairedDeviceData struct {
	SenderID string            `json:"sender_id" db:"sender_id"`
	Channel  string            `json:"channel" db:"channel"`
	ChatID   string            `json:"chat_id" db:"chat_id"`
	UserID   *uuid.UUID        `json:"user_id,omitempty" db:"user_id"`
	PairedAt int64             `json:"paired_at" db:"paired_at"`
	PairedBy string            `json:"paired_by" db:"paired_by"`
	Metadata map[string]string `json:"metadata,omitempty" db:"metadata"`
}

// PairingStore manages device pairing. All methods operate exclusively on
// paired_devices and pairing_requests — they never read or write
// channel_contacts.merged_id. Pairing is a channel-scope access gate;
// identity merge (MergeUserAggregate) is an orthogonal admin operation.
// Do not cross-mutate between the two domains.
type PairingStore interface {
	RequestPairing(ctx context.Context, senderID, channel, chatID, accountID string, metadata map[string]string) (string, error)
	ApprovePairing(ctx context.Context, code, approvedBy string) (*PairedDeviceData, error)
	DenyPairing(ctx context.Context, code string) error
	RevokePairing(ctx context.Context, senderID, channel string) error
	IsPaired(ctx context.Context, senderID, channel string) (bool, error)
	ListPending(ctx context.Context) []PairingRequestData
	ListPaired(ctx context.Context) []PairedDeviceData
	// BindUser links an approved paired device to an authenticated user. Pre-bind
	// the row's user_id is NULL; after first message-resolved authentication the
	// channel manager calls BindUser so subsequent HTTP/WS requests carry user
	// scope. Idempotent.
	BindUser(ctx context.Context, senderID, channel string, userID uuid.UUID) error
	// MigrateGroupChatID updates all references from oldChatID to newChatID
	// across paired_devices, sessions, and channel_contacts within a transaction.
	// Idempotent (safe to call multiple times).
	MigrateGroupChatID(ctx context.Context, channel, oldChatID, newChatID string) error
}
