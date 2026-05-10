package gateway

import (
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

// makeClient builds a minimal Client for filter tests.
// conn and server are nil — only role/userID fields are used.
func makeClient(role permissions.Role, userID string) *Client {
	c := &Client{}
	c.role = role
	c.userID = userID
	return c
}

// makeEvent builds a bus.Event for testing.
func makeEvent(name string, payload any) bus.Event {
	return bus.Event{Name: name, Payload: payload}
}

// ---- isSystemEvent ----

func TestIsSystemEvent_KnownNames(t *testing.T) {
	systemEvents := []string{
		protocol.EventHealth,
		protocol.EventPresence,
		protocol.EventVoicewakeChanged,
		protocol.EventTick,
		protocol.EventShutdown,
		protocol.EventConnectChallenge,
		protocol.EventTalkMode,
		protocol.EventHeartbeat,
	}
	for _, name := range systemEvents {
		if !isSystemEvent(name) {
			t.Errorf("isSystemEvent(%q) = false, want true", name)
		}
	}
}

func TestIsSystemEvent_UnknownName(t *testing.T) {
	if isSystemEvent("agent") {
		t.Error("isSystemEvent('agent') should return false")
	}
}

// ---- isAdminOnlyEvent ----

func TestIsAdminOnlyEvent_KnownAdminEvents(t *testing.T) {
	adminEvents := []string{
		protocol.EventNodePairRequested,
		protocol.EventDevicePairReq,
		protocol.EventAgentLinkCreated,
		protocol.EventWorkspaceFileChanged,
	}
	for _, name := range adminEvents {
		if !isAdminOnlyEvent(name) {
			t.Errorf("isAdminOnlyEvent(%q) = false, want true", name)
		}
	}
}

func TestIsAdminOnlyEvent_NonAdminEvent(t *testing.T) {
	if isAdminOnlyEvent(protocol.EventAgent) {
		t.Error("isAdminOnlyEvent('agent') should return false")
	}
}

// ---- Internal event filtering (cache., audit.log) ----

func TestClientCanReceiveEvent_InternalCacheEvent_Blocked(t *testing.T) {
	c := makeClient(permissions.RoleAdmin, "admin")
	c.role = permissions.RoleRoot // even owner shouldn't get cache events
	evt := makeEvent("cache.invalidate", nil)
	if clientCanReceiveEvent(c, evt) {
		t.Error("cache.* events must never be forwarded to any client")
	}
}

func TestClientCanReceiveEvent_AuditLog_Blocked(t *testing.T) {
	c := makeClient(permissions.RoleRoot, "admin")
	evt := makeEvent(protocol.EventAuditLog, nil)
	if clientCanReceiveEvent(c, evt) {
		t.Error("audit.log events must never be forwarded")
	}
}

// ---- System events broadcast to everyone ----

func TestClientCanReceiveEvent_SystemEvent_AllowedToAnyClient(t *testing.T) {
	c := makeClient(permissions.RoleViewer, "viewer")
	evt := makeEvent(protocol.EventHealth, nil)
	// system events bypass tenant checks and go to everyone
	if !clientCanReceiveEvent(c, evt) {
		t.Error("system events (health) should reach all clients")
	}
}

// ---- User-scoped events (agent, chat) ----

func TestClientCanReceiveEvent_AgentEvent_FilteredByUserID(t *testing.T) {
	userA := makeClient(permissions.RoleMember, "user-a")
	userB := makeClient(permissions.RoleMember, "user-b")

	evt := makeEvent(protocol.EventAgent, map[string]any{"userId": "user-a"})

	if !clientCanReceiveEvent(userA, evt) {
		t.Error("user-a should receive their own agent event")
	}
	if clientCanReceiveEvent(userB, evt) {
		t.Error("user-b should NOT receive user-a's agent event")
	}
}

func TestClientCanReceiveEvent_AgentEvent_AdminSeesAll(t *testing.T) {
	admin := makeClient(permissions.RoleAdmin, "admin")
	evt := makeEvent(protocol.EventAgent, map[string]any{"userId": "user-x"})
	if !clientCanReceiveEvent(admin, evt) {
		t.Error("admin should receive all agent events")
	}
}

// ---- Team events ----

func TestClientCanReceiveEvent_TeamEvent_FilteredByTeamID(t *testing.T) {
	c := makeClient(permissions.RoleMember, "user")
	c.SetTeamAccess([]string{"team-1"})

	evtMyTeam := makeEvent("team.task.created", map[string]any{"team_id": "team-1"})
	evtOtherTeam := makeEvent("team.task.created", map[string]any{"team_id": "team-2"})

	if !clientCanReceiveEvent(c, evtMyTeam) {
		t.Error("user should receive events for their team")
	}
	if clientCanReceiveEvent(c, evtOtherTeam) {
		t.Error("user should NOT receive events for teams they don't belong to")
	}
}

// ---- Admin-only events ----

func TestClientCanReceiveEvent_AdminOnlyEvent_BlockedForNonAdmin(t *testing.T) {
	c := makeClient(permissions.RoleMember, "user")
	// Admin-only events land in the default deny path for non-admin.
	evt := makeEvent(protocol.EventNodePairRequested, nil)
	if clientCanReceiveEvent(c, evt) {
		t.Error("admin-only events should not reach operator-role clients")
	}
}

// ---- Skill events broadcast ----

func TestClientCanReceiveEvent_SkillEvent_BroadcastToTenantClients(t *testing.T) {
	ownerClient := makeClient(permissions.RoleRoot, "owner")
	// skill.deps.checked is a skill event — should broadcast
	evt := makeEvent(protocol.EventSkillDepsChecked, nil)
	// owner receives unscoped skill events
	if !clientCanReceiveEvent(ownerClient, evt) {
		t.Error("owner should receive skill events")
	}
}

// ---- Tenant access revocation ----

func TestClientCanReceiveEvent_TenantAccessRevoked_DeliveredToCorrectUser(t *testing.T) {
	userA := makeClient(permissions.RoleMember, "user-a")
	userB := makeClient(permissions.RoleMember, "user-b")

	evt := makeEvent(protocol.EventTenantAccessRevoked, map[string]any{"user_id": "user-a"})

	if !clientCanReceiveEvent(userA, evt) {
		t.Error("revocation event should be delivered to the affected user")
	}
	if clientCanReceiveEvent(userB, evt) {
		t.Error("revocation event should NOT be delivered to a different user")
	}
}

// ---- extractMapField ----

func TestExtractMapField_StringAnyMap(t *testing.T) {
	m := map[string]any{"userId": "abc", "teamId": "team-1"}
	if got := extractMapField(m, "userId"); got != "abc" {
		t.Errorf("extractMapField = %q, want %q", got, "abc")
	}
}

func TestExtractMapField_StringStringMap(t *testing.T) {
	m := map[string]string{"user_id": "xyz"}
	if got := extractMapField(m, "user_id"); got != "xyz" {
		t.Errorf("extractMapField = %q, want %q", got, "xyz")
	}
}

func TestExtractMapField_MissingKey(t *testing.T) {
	m := map[string]any{"other": "value"}
	if got := extractMapField(m, "userId"); got != "" {
		t.Errorf("extractMapField missing key = %q, want empty", got)
	}
}

func TestExtractMapField_StructPayload_JSONFallback(t *testing.T) {
	type payload struct {
		UserID string `json:"userId"`
	}
	p := payload{UserID: "from-struct"}
	if got := extractMapField(p, "userId"); got != "from-struct" {
		t.Errorf("extractMapField JSON fallback = %q, want %q", got, "from-struct")
	}
}
