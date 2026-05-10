package tools

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/workspace"
)

// ─── minimal store stubs ─────────────────────────────────────────────────────

// dispatchStubTeamStore satisfies store.TeamStore. Only UpdateTask is exercised
// by dispatchTaskToAgent; all other methods panic if accidentally called.
type dispatchStubTeamStore struct {
	store.TeamStore // panic on unimplemented methods
}

func (s *dispatchStubTeamStore) UpdateTask(_ context.Context, _ uuid.UUID, _ map[string]any) error {
	return nil
}

// dispatchStubAgentStore satisfies store.AgentStore. GetByID is the only method
// called during dispatch (via cachedGetAgentByID).
type dispatchStubAgentStore struct {
	store.AgentStore // panic on unimplemented methods
	agentID         uuid.UUID
	agentKey        string
}

func (s *dispatchStubAgentStore) GetByID(_ context.Context, id uuid.UUID) (*store.AgentData, error) {
	return &store.AgentData{
		BaseModel:   store.BaseModel{ID: id},
		AgentKey:    s.agentKey,
		DisplayName: "Member Agent",
	}, nil
}

// ─── test helpers ─────────────────────────────────────────────────────────────

// newDispatchHarness constructs a TeamToolManager wired to an in-memory bus
// and returns the bus so tests can inspect published messages.
func newDispatchHarness(memberKey string) (*TeamToolManager, *bus.MessageBus) {
	mb := bus.New()
	ts := &dispatchStubTeamStore{}
	as := &dispatchStubAgentStore{agentKey: memberKey}
	mgr := NewTeamToolManager(ts, as, mb, "/tmp/ws-test")
	return mgr, mb
}

// drainOneInbound reads the next InboundMessage from the bus with a short timeout.
func drainOneInbound(mb *bus.MessageBus) (bus.InboundMessage, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	return mb.ConsumeInbound(ctx)
}

// newDispatchTask builds a minimal TeamTaskData for dispatch tests.
func newDispatchTask(channel, chatID, peerKind string) *store.TeamTaskData {
	return &store.TeamTaskData{
		BaseModel:  store.BaseModel{ID: uuid.New()},
		TaskNumber: 1,
		Subject:    "dispatch isolation test task",
		Channel:    channel,
		ChatID:     chatID,
		Metadata: map[string]any{
			TaskMetaPeerKind: peerKind,
		},
	}
}

// newDispatchTeam builds a minimal TeamData.
func newDispatchTeam() *store.TeamData {
	return &store.TeamData{
		BaseModel:   store.BaseModel{ID: uuid.New()},
		LeadAgentID: uuid.New(),
	}
}

// ─── isolation tests ──────────────────────────────────────────────────────────

// Case 1: parent in group chat dispatches → dispatched InboundMessage.UserID == "".
// The consumer (handleTeammateMessage) re-derives the correct group scope when needed;
// the raw group chat ID must NOT be forwarded as the sub-agent's runtime UserID.
func TestDispatchIsolation_UserIDIsEmpty(t *testing.T) {
	memberID := uuid.New()
	mgr, mb := newDispatchHarness("member-agent")

	team := newDispatchTeam()
	task := newDispatchTask("telegram", "-100123456", "group")

	// Parent context has a group-scoped UserID (as set by processNormalMessage).
	ctx := store.WithUserID(context.Background(), "group:telegram:-100123456")

	mgr.dispatchTaskToAgent(ctx, task, team, memberID)

	msg, ok := drainOneInbound(mb)
	if !ok {
		t.Fatal("expected inbound message to be published")
	}
	if msg.UserID != "" {
		t.Errorf("UserID = %q, want \"\" — group identity must not leak into sub-agent runtime", msg.UserID)
	}
}

// Case 2: parent has TeamID → dispatched metadata carries MetaTeamID == team.ID.
func TestDispatchIsolation_TeamIDPropagated(t *testing.T) {
	memberID := uuid.New()
	mgr, mb := newDispatchHarness("member-agent")

	team := newDispatchTeam()
	task := newDispatchTask("telegram", "-100123456", "group")

	mgr.dispatchTaskToAgent(context.Background(), task, team, memberID)

	msg, ok := drainOneInbound(mb)
	if !ok {
		t.Fatal("expected inbound message")
	}
	if msg.Metadata[MetaTeamID] != team.ID.String() {
		t.Errorf("MetaTeamID = %q, want %q", msg.Metadata[MetaTeamID], team.ID.String())
	}
}

// Case 3: parent has ProjectID in workspace context → dispatched metadata carries MetaOriginProjectID.
func TestDispatchIsolation_ProjectIDPropagated(t *testing.T) {
	memberID := uuid.New()
	projectID := uuid.New()
	mgr, mb := newDispatchHarness("member-agent")

	team := newDispatchTeam()
	task := newDispatchTask("telegram", "42", "direct")

	// Parent's resolved workspace context with project binding.
	wc := &workspace.WorkspaceContext{
		ProjectID:   &projectID,
		ProjectSlug: "my-project",
	}
	ctx := workspace.WithContext(context.Background(), wc)

	mgr.dispatchTaskToAgent(ctx, task, team, memberID)

	msg, ok := drainOneInbound(mb)
	if !ok {
		t.Fatal("expected inbound message")
	}
	if msg.Metadata[MetaOriginProjectID] != projectID.String() {
		t.Errorf("MetaOriginProjectID = %q, want %q", msg.Metadata[MetaOriginProjectID], projectID.String())
	}
}

// Case 4: parent context carries a group ChatID — dispatched InboundMessage.UserID == "" (dropped).
// Verifies isolation regardless of whether peerKind is "group" or the raw chatID differs.
func TestDispatchIsolation_GroupChatIDNotLeakedAsUserID(t *testing.T) {
	memberID := uuid.New()
	mgr, mb := newDispatchHarness("member-agent")

	team := newDispatchTeam()
	// ChatID is a Telegram group ID — this used to leak as InboundMessage.UserID.
	task := newDispatchTask("telegram", "-100987654", "group")

	// Simulate group-scoped UserID in parent context.
	ctx := store.WithUserID(context.Background(), "group:telegram:-100987654")

	mgr.dispatchTaskToAgent(ctx, task, team, memberID)

	msg, ok := drainOneInbound(mb)
	if !ok {
		t.Fatal("expected inbound message")
	}
	if msg.UserID != "" {
		t.Errorf("UserID = %q, want \"\" — group identity must be dropped for sub-agents", msg.UserID)
	}
	// Verify group ID is NOT present in the runtime field (could be in different form too).
	if msg.UserID == "-100987654" || msg.UserID == "group:telegram:-100987654" {
		t.Errorf("UserID contains group chat ID %q — privacy isolation violated", msg.UserID)
	}
}

// Case 5 (H-4 snapshot semantics): admin flips default_project_id from P1 to P2 between
// parent session start and dispatch time. The sub-agent must receive P1 (snapshotted at
// parent turn start via workspace.FromContext), not P2 (the live DB value after the flip).
//
// The snapshot is workspace.WorkspaceContext.ProjectID — written once by injectContext/
// resolveProjectParams at the start of the parent's turn and never re-read from the
// contact store during dispatch. Any mid-dispatch contact store change is invisible.
func TestDispatchIsolation_ProjectIDSnapshotIgnoresPostSessionChange(t *testing.T) {
	memberID := uuid.New()
	projectP1 := uuid.New() // project at parent turn start
	projectP2 := uuid.New() // project after admin change (must NOT reach sub-agent)
	mgr, mb := newDispatchHarness("member-agent")

	team := newDispatchTeam()
	task := newDispatchTask("telegram", "-100111222", "group")

	// Simulate parent resolved P1 at session start → stored in workspace context.
	wc := &workspace.WorkspaceContext{
		ProjectID:   &projectP1,
		ProjectSlug: "project-p1",
	}
	ctx := workspace.WithContext(context.Background(), wc)

	// P2 represents the new DB value after the admin change. Since dispatchTaskToAgent
	// reads workspace.FromContext (not the contact store), P2 is unreachable here.
	// We assert the published message carries P1, not P2.
	_ = projectP2

	mgr.dispatchTaskToAgent(ctx, task, team, memberID)

	msg, ok := drainOneInbound(mb)
	if !ok {
		t.Fatal("expected inbound message")
	}
	got := msg.Metadata[MetaOriginProjectID]
	if got != projectP1.String() {
		t.Errorf("MetaOriginProjectID = %q, want P1=%q — snapshot must not reflect post-session DB change",
			got, projectP1.String())
	}
	if got == projectP2.String() {
		t.Errorf("MetaOriginProjectID matched P2 — snapshot semantics violated")
	}
}

// Regression: no project in parent context → MetaOriginProjectID omitted.
func TestDispatchIsolation_NoProjectIDWhenParentHasNone(t *testing.T) {
	memberID := uuid.New()
	mgr, mb := newDispatchHarness("member-agent")

	team := newDispatchTeam()
	task := newDispatchTask("telegram", "42", "direct")

	// No workspace context → no project override.
	mgr.dispatchTaskToAgent(context.Background(), task, team, memberID)

	msg, ok := drainOneInbound(mb)
	if !ok {
		t.Fatal("expected inbound message")
	}
	if pid := msg.Metadata[MetaOriginProjectID]; pid != "" {
		t.Errorf("MetaOriginProjectID = %q, want empty (no project in parent)", pid)
	}
}

// Audit trail: MetaOriginUserID must still carry the originating user ID for audit
// even though InboundMessage.UserID is empty (runtime isolation does not erase audit log).
func TestDispatchIsolation_AuditTrailPreserved(t *testing.T) {
	memberID := uuid.New()
	mgr, mb := newDispatchHarness("member-agent")

	team := newDispatchTeam()
	task := newDispatchTask("telegram", "-100333444", "group")

	originUser := "group:telegram:-100333444"
	ctx := store.WithUserID(context.Background(), originUser)

	mgr.dispatchTaskToAgent(ctx, task, team, memberID)

	msg, ok := drainOneInbound(mb)
	if !ok {
		t.Fatal("expected inbound message")
	}
	// Runtime field must be empty (sub-agent identity isolation).
	if msg.UserID != "" {
		t.Errorf("UserID = %q, want \"\" (runtime identity isolation)", msg.UserID)
	}
	// Metadata audit trail must still carry the origin so replay/debug works.
	if msg.Metadata[MetaOriginUserID] == "" {
		t.Error("MetaOriginUserID must be present in metadata for audit trail — do not clear it")
	}
}
