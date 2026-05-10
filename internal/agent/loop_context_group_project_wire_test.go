package agent

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// ─── resolveSessionProject (pure function, no DB) ─────────────────────────

// Case 1: group contact with default_project_id set → resolver returns that UUID.
func TestGroupProjectWire_GroupContact_WithProject(t *testing.T) {
	pid := uuid.New()
	contact := &store.ChannelContact{
		ContactType:      "group",
		DefaultProjectID: &pid,
	}
	got := resolveSessionProject(nil, contact)
	if got == nil {
		t.Fatal("expected non-nil project UUID for group contact with default_project_id")
	}
	if *got != pid {
		t.Errorf("resolved project = %s, want %s", got, pid)
	}
}

// Case 2: group contact with NULL default_project_id → resolver returns nil.
func TestGroupProjectWire_GroupContact_NullProject(t *testing.T) {
	contact := &store.ChannelContact{
		ContactType:      "group",
		DefaultProjectID: nil,
	}
	got := resolveSessionProject(nil, contact)
	if got != nil {
		t.Errorf("expected nil for group contact with no default_project_id, got %s", got)
	}
}

// Case 3: DM contact with default_project_id → resolver also returns that UUID.
// No group-only restriction: any contact type with a default_project_id resolves.
func TestGroupProjectWire_DMContact_WithProject(t *testing.T) {
	pid := uuid.New()
	contact := &store.ChannelContact{
		ContactType:      "user",
		DefaultProjectID: &pid,
	}
	got := resolveSessionProject(nil, contact)
	if got == nil {
		t.Fatal("expected non-nil project UUID for DM contact with default_project_id")
	}
	if *got != pid {
		t.Errorf("resolved project = %s, want %s", got, pid)
	}
}

// Case 4: no channel contact (web user / nil contactStore) → resolveProjectParams
// returns (nil, "") without panicking. Simulates a web UI session with no contact.
func TestGroupProjectWire_NoContact_WebUser(t *testing.T) {
	l := &Loop{
		// contactStore nil: web sessions carry no channel contact; projectStore nil for same reason.
		contactStore: nil,
		projectStore: nil,
		sessions:     &noopSessionStore{},
	}
	pid, slug := l.resolveProjectParams(context.Background(), "web-sess-1", "", "", nil)
	if pid != nil {
		t.Errorf("expected nil project ID for web user, got %s", pid)
	}
	if slug != "" {
		t.Errorf("expected empty slug for web user, got %q", slug)
	}
}

// Case 5: Layer 2 placeholder smoke — the first parameter of resolveSessionProject
// is reserved for a future session_project_override (bot /project switch command,
// deferred post-rc1). Passing a non-nil metadata map must not change the result:
// the function only reads Layer 1 (contact.DefaultProjectID).
func TestGroupProjectWire_Layer2Placeholder_Layer1Wins(t *testing.T) {
	pid := uuid.New()
	contact := &store.ChannelContact{
		ContactType:      "group",
		DefaultProjectID: &pid,
	}
	// Simulate a caller that already has a Layer 2 override value but passes it
	// through the reserved placeholder parameter. rc1 must ignore it.
	layer2Meta := map[string]string{"session_project_override": uuid.New().String()}
	got := resolveSessionProject(layer2Meta, contact)
	if got == nil {
		t.Fatal("expected Layer 1 project to be returned when Layer 2 is unimplemented")
	}
	if *got != pid {
		t.Errorf("Layer 1 project = %s, want %s; Layer 2 must not override in rc1", got, pid)
	}
}

// ─── noopSessionStore ────────────────────────────────────────────────────────

// noopSessionStore satisfies store.SessionStore with minimal no-op behaviour.
// resolveProjectParams only calls Get, so all other methods are stubs.
type noopSessionStore struct{}

// SessionCoreStore
func (n *noopSessionStore) GetOrCreate(_ context.Context, key string) *store.SessionData {
	return &store.SessionData{Key: key}
}
func (n *noopSessionStore) Get(_ context.Context, _ string) *store.SessionData { return nil }
func (n *noopSessionStore) UpdateProject(_ context.Context, _ string, _ *uuid.UUID) error {
	return nil
}
func (n *noopSessionStore) AddMessage(_ context.Context, _ string, _ providers.Message) {}
func (n *noopSessionStore) GetHistory(_ context.Context, _ string) []providers.Message  { return nil }
func (n *noopSessionStore) GetSummary(_ context.Context, _ string) string               { return "" }
func (n *noopSessionStore) SetSummary(_ context.Context, _, _ string)                   {}
func (n *noopSessionStore) GetLabel(_ context.Context, _ string) string                 { return "" }
func (n *noopSessionStore) SetLabel(_ context.Context, _, _ string)                     {}
func (n *noopSessionStore) SetAgentInfo(_ context.Context, _ string, _ uuid.UUID, _ string) {}
func (n *noopSessionStore) TruncateHistory(_ context.Context, _ string, _ int)              {}
func (n *noopSessionStore) SetHistory(_ context.Context, _ string, _ []providers.Message)   {}
func (n *noopSessionStore) Reset(_ context.Context, _ string)                               {}
func (n *noopSessionStore) Delete(_ context.Context, _ string) error                        { return nil }
func (n *noopSessionStore) Save(_ context.Context, _ string) error                          { return nil }

// SessionMetadataStore
func (n *noopSessionStore) UpdateMetadata(_ context.Context, _, _, _, _ string)          {}
func (n *noopSessionStore) AccumulateTokens(_ context.Context, _ string, _, _ int64)     {}
func (n *noopSessionStore) IncrementCompaction(_ context.Context, _ string)              {}
func (n *noopSessionStore) GetCompactionCount(_ context.Context, _ string) int           { return 0 }
func (n *noopSessionStore) GetMemoryFlushCompactionCount(_ context.Context, _ string) int { return 0 }
func (n *noopSessionStore) SetMemoryFlushDone(_ context.Context, _ string)               {}
func (n *noopSessionStore) GetSessionMetadata(_ context.Context, _ string) map[string]string {
	return nil
}
func (n *noopSessionStore) SetSessionMetadata(_ context.Context, _ string, _ map[string]string) {}
func (n *noopSessionStore) SetSpawnInfo(_ context.Context, _, _ string, _ int)                  {}
func (n *noopSessionStore) SetContextWindow(_ context.Context, _ string, _ int)                 {}
func (n *noopSessionStore) GetContextWindow(_ context.Context, _ string) int                    { return 0 }
func (n *noopSessionStore) SetLastPromptTokens(_ context.Context, _ string, _, _ int)           {}
func (n *noopSessionStore) GetLastPromptTokens(_ context.Context, _ string) (int, int)          { return 0, 0 }

// SessionListingStore
func (n *noopSessionStore) List(_ context.Context, _ string) []store.SessionInfo { return nil }
func (n *noopSessionStore) ListPaged(_ context.Context, _ store.SessionListOpts) store.SessionListResult {
	return store.SessionListResult{}
}
func (n *noopSessionStore) ListPagedRich(_ context.Context, _ store.SessionListOpts) store.SessionListRichResult {
	return store.SessionListRichResult{}
}
func (n *noopSessionStore) LastUsedChannel(_ context.Context, _ string) (string, string) {
	return "", ""
}
