package agent

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
)

// stubAgent is a minimal Agent implementation for router tests.
// ID() returns a fixed agent_key; IsRunning() tracks a bool.
type stubAgent struct {
	id      string
	running bool
}

func (s *stubAgent) ID() string                                          { return s.id }
func (s *stubAgent) UUID() uuid.UUID                                     { return uuid.Nil }
func (s *stubAgent) OtherConfig() json.RawMessage                        { return nil }
func (s *stubAgent) Run(context.Context, RunRequest) (*RunResult, error) { return nil, nil }
func (s *stubAgent) IsRunning() bool                                     { return s.running }
func (s *stubAgent) Model() string                                       { return "test-model" }
func (s *stubAgent) ProviderName() string                                { return "test" }
func (s *stubAgent) Provider() providers.Provider                        { return nil }

func stubResolver(agentKey string) ResolverFunc {
	return func(_ context.Context, _ string) (Agent, error) {
		return &stubAgent{id: agentKey}, nil
	}
}

// TestRouterGet_UUIDInputStoresCanonicalKey verifies that when the caller
// passes a UUID-like string to Get(), the cache entry lands under the
// canonical agent_key (returned by ag.ID()), not the UUID input.
func TestRouterGet_UUIDInputStoresCanonicalKey(t *testing.T) {
	r := NewRouter()
	r.SetResolver(stubResolver("goctech-leader"))

	agentUUID := uuid.New().String()
	if _, err := r.Get(context.Background(), agentUUID); err != nil {
		t.Fatalf("Get: %v", err)
	}

	r.mu.RLock()
	_, canonical := r.agents["goctech-leader"]
	_, nonCanonical := r.agents[agentUUID]
	r.mu.RUnlock()

	if !canonical {
		t.Error("expected canonical key 'goctech-leader' in cache")
	}
	if nonCanonical {
		t.Errorf("unexpected non-canonical key %q in cache", agentUUID)
	}
}

// TestRouterGet_IdempotentCacheUnderKeyOrUUID verifies that calling Get()
// first with agent_key then with UUID produces exactly ONE cache entry.
func TestRouterGet_IdempotentCacheUnderKeyOrUUID(t *testing.T) {
	r := NewRouter()
	var resolveCount atomic.Int32
	r.SetResolver(func(_ context.Context, _ string) (Agent, error) {
		resolveCount.Add(1)
		return &stubAgent{id: "my-agent"}, nil
	})

	if _, err := r.Get(context.Background(), "my-agent"); err != nil {
		t.Fatalf("Get(agent_key): %v", err)
	}
	if _, err := r.Get(context.Background(), uuid.New().String()); err != nil {
		t.Fatalf("Get(uuid): %v", err)
	}

	r.mu.RLock()
	total := len(r.agents)
	r.mu.RUnlock()

	if total != 1 {
		t.Errorf("expected exactly 1 cache entry, got %d", total)
	}
}

// TestInvalidateAgent_DropsCanonicalEntry verifies a cached agent is removed
// when InvalidateAgent is called with its agent_key.
func TestInvalidateAgent_DropsCanonicalEntry(t *testing.T) {
	r := NewRouter()
	r.SetResolver(stubResolver("foo"))

	if _, err := r.Get(context.Background(), "foo"); err != nil {
		t.Fatalf("Get: %v", err)
	}

	r.InvalidateAgent("foo")

	r.mu.RLock()
	_, exists := r.agents["foo"]
	r.mu.RUnlock()

	if exists {
		t.Error("expected entry 'foo' to be removed by InvalidateAgent")
	}
}

// TestInvalidateAgent_EmptyKeyNoOp guards against accidental wildcard wipes.
func TestInvalidateAgent_EmptyKeyNoOp(t *testing.T) {
	r := NewRouter()
	r.agents["foo"] = &agentEntry{agent: &stubAgent{id: "foo"}}
	r.agents["bar"] = &agentEntry{agent: &stubAgent{id: "bar"}}

	r.InvalidateAgent("")

	r.mu.RLock()
	count := len(r.agents)
	r.mu.RUnlock()

	if count != 2 {
		t.Errorf("InvalidateAgent(\"\") should be no-op, got %d entries (want 2)", count)
	}
}
