package store

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/cache"
)

// mockContactStore records every UpsertContact call for assertion.
// Only implements the methods used by ContactCollector (store.ContactStore).
type mockContactStore struct {
	mu      sync.Mutex
	upserts []mockUpsertCall
}

type mockUpsertCall struct {
	channelType     string
	channelInstance string
	senderID        string
	userID          string
	displayName     string
	username        string
	peerKind        string
	contactType     string
	threadID        string
	threadType      string
}

func (m *mockContactStore) UpsertContact(ctx context.Context, channelType, channelInstance, senderID, userID, displayName, username, peerKind, contactType, threadID, threadType string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.upserts = append(m.upserts, mockUpsertCall{
		channelType:     channelType,
		channelInstance: channelInstance,
		senderID:        senderID,
		userID:          userID,
		displayName:     displayName,
		username:        username,
		peerKind:        peerKind,
		contactType:     contactType,
		threadID:        threadID,
		threadType:      threadType,
	})
	return nil
}

func (m *mockContactStore) ResolveTenantUserID(_ context.Context, _, _ string) (string, error) {
	return "", nil
}

// Stub methods to satisfy ContactStore interface (not used in these tests).
func (m *mockContactStore) ListContacts(_ context.Context, _ ContactListOpts) ([]ChannelContact, error) {
	return nil, nil
}
func (m *mockContactStore) CountContacts(_ context.Context, _ ContactListOpts) (int, error) {
	return 0, nil
}
func (m *mockContactStore) GetContactsBySenderIDs(_ context.Context, _ []string) (map[string]ChannelContact, error) {
	return nil, nil
}
func (m *mockContactStore) GetContactByID(_ context.Context, _ uuid.UUID) (*ChannelContact, error) {
	return nil, nil
}
func (m *mockContactStore) GetSenderIDsByContactIDs(_ context.Context, _ []uuid.UUID) ([]string, error) {
	return nil, nil
}
func (m *mockContactStore) MergeUserAggregate(_ context.Context, _ MergeUserAggregateRequest) error {
	return nil
}
func (m *mockContactStore) UpdateDefaultProject(_ context.Context, _ uuid.UUID, _ *uuid.UUID) error {
	return nil
}
func (m *mockContactStore) GetContactByChannelAndChatID(_ context.Context, _, _ string) (*ChannelContact, error) {
	return nil, ErrContactNotFound
}
func (m *mockContactStore) GetCanonicalDMContact(_ context.Context, _ uuid.UUID, _ string) (*ChannelContact, error) {
	return nil, ErrContactIDNotFound
}

func (m *mockContactStore) upsertCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.upserts)
}

// TestContactCollector_SameSenderDedup verifies that repeated calls for the
// same (channel, sender, thread) only hit the store once.
func TestContactCollector_SameSenderDedup(t *testing.T) {
	mock := &mockContactStore{}
	c := NewContactCollector(mock, cache.NewInMemoryCache[bool]())

	ctx := context.Background()

	for range 5 {
		c.EnsureContact(ctx, "telegram", "tg-main", "user-123", "uid-1", "Alice", "alice", "user", "user", "", "")
	}

	if got := mock.upsertCount(); got != 1 {
		t.Errorf("same-sender dedup broken: got %d upserts, want 1", got)
	}
}

// TestContactCollector_DifferentInstances verifies same sender in different
// channel instances (different bots) produces separate upserts.
func TestContactCollector_DifferentInstances(t *testing.T) {
	mock := &mockContactStore{}
	c := NewContactCollector(mock, cache.NewInMemoryCache[bool]())

	ctx := context.Background()

	c.EnsureContact(ctx, "telegram", "bot-A", "user-123", "uid-1", "Alice", "alice", "user", "user", "", "")
	c.EnsureContact(ctx, "telegram", "bot-B", "user-123", "uid-1", "Alice", "alice", "user", "user", "", "")

	if got := mock.upsertCount(); got != 2 {
		t.Errorf("different-instance isolation broken: got %d upserts, want 2", got)
	}
}

// TestContactCollector_SingleTenant verifies dedup works in single-user (desktop) mode.
func TestContactCollector_SingleTenant(t *testing.T) {
	mock := &mockContactStore{}
	c := NewContactCollector(mock, cache.NewInMemoryCache[bool]())

	ctx := context.Background()

	c.EnsureContact(ctx, "telegram", "tg", "user-1", "uid", "", "", "user", "user", "", "")
	c.EnsureContact(ctx, "telegram", "tg", "user-1", "uid", "", "", "user", "user", "", "") // dup

	if got := mock.upsertCount(); got != 1 {
		t.Errorf("zero-tenant dedup broken: got %d upserts, want 1", got)
	}
}

// TestContactCollector_DifferentThreads verifies same sender in different
// threads produces separate upserts.
func TestContactCollector_DifferentThreads(t *testing.T) {
	mock := &mockContactStore{}
	c := NewContactCollector(mock, cache.NewInMemoryCache[bool]())

	ctx := context.Background()

	c.EnsureContact(ctx, "slack", "ws-1", "user-1", "uid", "", "", "user", "user", "thread-A", "channel")
	c.EnsureContact(ctx, "slack", "ws-1", "user-1", "uid", "", "", "user", "user", "thread-B", "channel")

	if got := mock.upsertCount(); got != 2 {
		t.Errorf("different-thread isolation broken: got %d upserts, want 2", got)
	}
}
