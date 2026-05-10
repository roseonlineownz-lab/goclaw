package channels

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// stubContactStore implements store.ContactStore for dispatch routing tests.
// Only GetContactByChannelAndChatID and GetCanonicalDMContact are exercised.
type stubContactStore struct {
	byChannelChat  map[string]*store.ChannelContact // key: "channel:chatID"
	canonicalByUID map[string]*store.ChannelContact // key: "userID:channelType"
}

func newStubContactStore() *stubContactStore {
	return &stubContactStore{
		byChannelChat:  make(map[string]*store.ChannelContact),
		canonicalByUID: make(map[string]*store.ChannelContact),
	}
}

func (s *stubContactStore) setContact(channelType, chatID string, c *store.ChannelContact) {
	s.byChannelChat[channelType+":"+chatID] = c
}

func (s *stubContactStore) setCanonical(userID uuid.UUID, channelType string, c *store.ChannelContact) {
	s.canonicalByUID[userID.String()+":"+channelType] = c
}

func (s *stubContactStore) GetContactByChannelAndChatID(_ context.Context, channelType, chatID string) (*store.ChannelContact, error) {
	c, ok := s.byChannelChat[channelType+":"+chatID]
	if !ok {
		return nil, store.ErrContactNotFound
	}
	return c, nil
}

func (s *stubContactStore) GetCanonicalDMContact(_ context.Context, userID uuid.UUID, channelType string) (*store.ChannelContact, error) {
	c, ok := s.canonicalByUID[userID.String()+":"+channelType]
	if !ok {
		return nil, store.ErrContactIDNotFound
	}
	return c, nil
}

// Unused interface stubs — satisfy store.ContactStore.
func (s *stubContactStore) UpsertContact(_ context.Context, _, _, _, _, _, _, _, _, _, _ string) error {
	return nil
}
func (s *stubContactStore) ListContacts(_ context.Context, _ store.ContactListOpts) ([]store.ChannelContact, error) {
	return nil, nil
}
func (s *stubContactStore) CountContacts(_ context.Context, _ store.ContactListOpts) (int, error) {
	return 0, nil
}
func (s *stubContactStore) GetContactsBySenderIDs(_ context.Context, _ []string) (map[string]store.ChannelContact, error) {
	return nil, nil
}
func (s *stubContactStore) GetContactByID(_ context.Context, _ uuid.UUID) (*store.ChannelContact, error) {
	return nil, nil
}
func (s *stubContactStore) GetSenderIDsByContactIDs(_ context.Context, _ []uuid.UUID) ([]string, error) {
	return nil, nil
}
func (s *stubContactStore) MergeUserAggregate(_ context.Context, _ store.MergeUserAggregateRequest) error {
	return nil
}
func (s *stubContactStore) ResolveTenantUserID(_ context.Context, _, _ string) (string, error) {
	return "", nil
}
func (s *stubContactStore) UpdateDefaultProject(_ context.Context, _ uuid.UUID, _ *uuid.UUID) error {
	return nil
}

// helpers

func managerWithContactStore(cs store.ContactStore) *Manager {
	m := &Manager{contactStore: cs}
	return m
}

func outbound(channel, chatID string) bus.OutboundMessage {
	return bus.OutboundMessage{Channel: channel, ChatID: chatID, Content: "hello"}
}

// Case A: unmerged DM contact → original chat_id returned (regression guard).
func TestResolveTargetChatID_UnmergedDM(t *testing.T) {
	stub := newStubContactStore()
	stub.setContact("telegram", "user-1", &store.ChannelContact{
		ChannelType: "telegram",
		SenderID:    "user-1",
		PeerKind:    new("direct"),
	})

	m := managerWithContactStore(stub)
	got := m.resolveTargetChatID(context.Background(), outbound("telegram", "user-1"))
	if got != "user-1" {
		t.Errorf("unmerged DM: got %q want %q", got, "user-1")
	}
}

// Case B: DM contact merged → canonical user's same-channel DM chat_id.
func TestResolveTargetChatID_MergedDMRoutesToCanonical(t *testing.T) {
	mergedUserID := uuid.Must(uuid.NewV7())

	stub := newStubContactStore()
	stub.setContact("telegram", "old-user-1", &store.ChannelContact{
		ChannelType: "telegram",
		SenderID:    "old-user-1",
		PeerKind:    new("direct"),
		MergedID:    &mergedUserID,
	})
	stub.setCanonical(mergedUserID, "telegram", &store.ChannelContact{
		ChannelType: "telegram",
		SenderID:    "canonical-user-99",
		PeerKind:    new("direct"),
	})

	m := managerWithContactStore(stub)
	got := m.resolveTargetChatID(context.Background(), outbound("telegram", "old-user-1"))
	if got != "canonical-user-99" {
		t.Errorf("merged DM: got %q want canonical-user-99", got)
	}
}

// Case C: group contact with merged_id → reply STAYS in original group chat.
// Privacy zone applies to FS/memory only, not channel addressability.
func TestResolveTargetChatID_MergedGroupStaysInGroup(t *testing.T) {
	mergedUserID := uuid.Must(uuid.NewV7())

	stub := newStubContactStore()
	stub.setContact("telegram", "group-chat-42", &store.ChannelContact{
		ChannelType: "telegram",
		SenderID:    "group-chat-42",
		PeerKind:    new("group"),
		MergedID:    &mergedUserID,
	})
	// Even if canonical exists, group must not be re-routed.
	stub.setCanonical(mergedUserID, "telegram", &store.ChannelContact{
		ChannelType: "telegram",
		SenderID:    "canonical-user-99",
		PeerKind:    new("direct"),
	})

	m := managerWithContactStore(stub)
	got := m.resolveTargetChatID(context.Background(), outbound("telegram", "group-chat-42"))
	if got != "group-chat-42" {
		t.Errorf("merged group: got %q want original group-chat-42", got)
	}
}

// Case D: contact lookup miss (system sender, no contact row) → original chat_id.
func TestResolveTargetChatID_NoContactRow(t *testing.T) {
	stub := newStubContactStore()
	// no entry registered → GetContactByChannelAndChatID returns ErrContactNotFound

	m := managerWithContactStore(stub)
	got := m.resolveTargetChatID(context.Background(), outbound("telegram", "system-sender"))
	if got != "system-sender" {
		t.Errorf("no-contact fallback: got %q want system-sender", got)
	}
}

// Case E: DM merged but no canonical DM exists for channel → fallback original.
func TestResolveTargetChatID_MergedDMNoCanonical(t *testing.T) {
	mergedUserID := uuid.Must(uuid.NewV7())

	stub := newStubContactStore()
	stub.setContact("telegram", "old-dm-5", &store.ChannelContact{
		ChannelType: "telegram",
		SenderID:    "old-dm-5",
		PeerKind:    new("direct"),
		MergedID:    &mergedUserID,
	})
	// No canonical entry → GetCanonicalDMContact returns ErrContactIDNotFound.

	m := managerWithContactStore(stub)
	got := m.resolveTargetChatID(context.Background(), outbound("telegram", "old-dm-5"))
	if got != "old-dm-5" {
		t.Errorf("no canonical fallback: got %q want old-dm-5", got)
	}
}

// nil contactStore path: no lookup, return original (used in tests and lite build).
func TestResolveTargetChatID_NilContactStore(t *testing.T) {
	m := &Manager{contactStore: nil}
	got := m.resolveTargetChatID(context.Background(), outbound("telegram", "chat-abc"))
	if got != "chat-abc" {
		t.Errorf("nil store: got %q want chat-abc", got)
	}
}
