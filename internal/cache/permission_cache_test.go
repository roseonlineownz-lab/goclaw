package cache

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/bus"
)

func TestPermissionCache_AgentAccess(t *testing.T) {
	pc := NewPermissionCache()
	defer pc.Close()

	ctx := context.Background()
	agentID := uuid.New()
	userID := "user-2"

	if _, _, ok := pc.GetAgentAccess(ctx, agentID, userID); ok {
		t.Fatal("expected cache miss before SetAgentAccess")
	}

	pc.SetAgentAccess(ctx, agentID, userID, true, "editor")

	allowed, role, ok := pc.GetAgentAccess(ctx, agentID, userID)
	if !ok {
		t.Fatal("expected cache hit after SetAgentAccess")
	}
	if !allowed || role != "editor" {
		t.Errorf("expected allowed=true role=editor, got allowed=%v role=%q", allowed, role)
	}
}

func TestPermissionCache_TeamAccess(t *testing.T) {
	pc := NewPermissionCache()
	defer pc.Close()

	ctx := context.Background()
	teamID := uuid.New()
	userID := "user-3"

	if _, ok := pc.GetTeamAccess(ctx, teamID, userID); ok {
		t.Fatal("expected cache miss before SetTeamAccess")
	}

	pc.SetTeamAccess(ctx, teamID, userID, true)

	allowed, ok := pc.GetTeamAccess(ctx, teamID, userID)
	if !ok || !allowed {
		t.Fatalf("expected cache hit allowed=true, got allowed=%v ok=%v", allowed, ok)
	}
}

func TestPermissionCache_Close_Idempotent(t *testing.T) {
	pc := NewPermissionCache()
	pc.Close()
	pc.Close() // must not panic
}

func TestPermissionCache_HandleInvalidation_AgentAccess_WithKey(t *testing.T) {
	pc := NewPermissionCache()
	defer pc.Close()

	ctx := context.Background()
	agentID1 := uuid.New()
	agentID2 := uuid.New()

	pc.SetAgentAccess(ctx, agentID1, "u1", true, "admin")
	pc.SetAgentAccess(ctx, agentID1, "u2", true, "viewer")
	pc.SetAgentAccess(ctx, agentID2, "u1", true, "admin")

	pc.HandleInvalidation(bus.CacheInvalidatePayload{Kind: bus.CacheKindAgentAccess, Key: agentID1.String()})

	if _, _, ok := pc.GetAgentAccess(ctx, agentID1, "u1"); ok {
		t.Error("agentID1:u1 access should be cleared")
	}
	if _, _, ok := pc.GetAgentAccess(ctx, agentID1, "u2"); ok {
		t.Error("agentID1:u2 access should be cleared")
	}
	if _, _, ok := pc.GetAgentAccess(ctx, agentID2, "u1"); !ok {
		t.Error("agentID2:u1 access should still be cached")
	}
}

func TestPermissionCache_HandleInvalidation_AgentAccess_ClearAll(t *testing.T) {
	pc := NewPermissionCache()
	defer pc.Close()

	ctx := context.Background()
	agentID := uuid.New()

	pc.SetAgentAccess(ctx, agentID, "u1", true, "admin")

	pc.HandleInvalidation(bus.CacheInvalidatePayload{Kind: bus.CacheKindAgentAccess, Key: ""})

	if _, _, ok := pc.GetAgentAccess(ctx, agentID, "u1"); ok {
		t.Error("agent access should be cleared when Key is empty")
	}
}

func TestPermissionCache_HandleInvalidation_TeamAccess_WithKey(t *testing.T) {
	pc := NewPermissionCache()
	defer pc.Close()

	ctx := context.Background()
	teamID1 := uuid.New()
	teamID2 := uuid.New()

	pc.SetTeamAccess(ctx, teamID1, "u1", true)
	pc.SetTeamAccess(ctx, teamID1, "u2", false)
	pc.SetTeamAccess(ctx, teamID2, "u1", true)

	pc.HandleInvalidation(bus.CacheInvalidatePayload{Kind: bus.CacheKindTeamAccess, Key: teamID1.String()})

	if _, ok := pc.GetTeamAccess(ctx, teamID1, "u1"); ok {
		t.Error("teamID1:u1 access should be cleared")
	}
	if _, ok := pc.GetTeamAccess(ctx, teamID1, "u2"); ok {
		t.Error("teamID1:u2 access should be cleared")
	}
	if _, ok := pc.GetTeamAccess(ctx, teamID2, "u1"); !ok {
		t.Error("teamID2:u1 access should still be cached")
	}
}

func TestPermissionCache_HandleInvalidation_TeamAccess_ClearAll(t *testing.T) {
	pc := NewPermissionCache()
	defer pc.Close()

	ctx := context.Background()
	teamID := uuid.New()

	pc.SetTeamAccess(ctx, teamID, "u1", true)

	pc.HandleInvalidation(bus.CacheInvalidatePayload{Kind: bus.CacheKindTeamAccess, Key: ""})

	if _, ok := pc.GetTeamAccess(ctx, teamID, "u1"); ok {
		t.Error("team access should be cleared when Key is empty")
	}
}

func TestPermissionCache_HandleInvalidation_UnknownKind(t *testing.T) {
	pc := NewPermissionCache()
	defer pc.Close()

	// unknown kind should not panic
	pc.HandleInvalidation(bus.CacheInvalidatePayload{Kind: "unknown_kind", Key: "anything"})
}
