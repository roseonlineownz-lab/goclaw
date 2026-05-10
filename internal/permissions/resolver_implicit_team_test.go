package permissions_test

// resolver_implicit_team_test.go — implicit team grant + idempotence tests.
//
// Implicit grant: user is member of team T AND agent is shared to T → user gets
// effective role = team grant role, even without an explicit agent_shares row.
//
// Idempotence: combining an explicit grant with an overlapping implicit team
// grant must not elevate or double-count the role — result is min(explicit, implicit).

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
)

func TestResolver_ImplicitTeamGrant_AllowsRead(t *testing.T) {
	// User has no explicit agent share but is a team member whose team holds a
	// member-level share on the agent → implicit member grant → read allowed.
	r := permissions.NewResolver(permissions.ResolverConfig{
		UserRole: func(_ context.Context, _ uuid.UUID) string { return permissions.ShareMember },
		ProjectGrant: func(_ context.Context, _, _ uuid.UUID) (string, bool) {
			return "", false // no project bound
		},
		AgentShare: func(_ context.Context, _, _ uuid.UUID) (string, bool) {
			return "", false // no explicit row
		},
		TeamMembership: func(_ context.Context, _, _ uuid.UUID) (string, bool) {
			return permissions.ShareMember, true // implicit via team
		},
	})

	ctx := context.Background()
	userID, agentID := uuid.New(), uuid.New()
	got := r.CheckAccess(ctx, userID, agentID, nil, permissions.ActionRead)
	if !got {
		t.Error("implicit team member should be allowed to read, got deny")
	}
}

func TestResolver_ImplicitTeamGrant_DeniesWrite_WhenTeamOnlyViewer(t *testing.T) {
	// Team has only viewer-level share → implicit viewer grant → write denied.
	r := permissions.NewResolver(permissions.ResolverConfig{
		UserRole: func(_ context.Context, _ uuid.UUID) string { return permissions.ShareMember },
		ProjectGrant: func(_ context.Context, _, _ uuid.UUID) (string, bool) {
			return "", false
		},
		AgentShare: func(_ context.Context, _, _ uuid.UUID) (string, bool) {
			return "", false
		},
		TeamMembership: func(_ context.Context, _, _ uuid.UUID) (string, bool) {
			return permissions.ShareViewer, true // team has viewer only
		},
	})

	ctx := context.Background()
	got := r.CheckAccess(ctx, uuid.New(), uuid.New(), nil, permissions.ActionWriteFile)
	if got {
		t.Error("implicit viewer-only team should deny write_file, got allow")
	}
}

func TestResolver_ExplicitAndImplicitOverlap_Idempotent(t *testing.T) {
	// User has explicit editor share AND implicit team member share.
	// Effective role = min(editor, member) = member → write allowed.
	r := permissions.NewResolver(permissions.ResolverConfig{
		UserRole: func(_ context.Context, _ uuid.UUID) string { return permissions.ShareMember },
		ProjectGrant: func(_ context.Context, _, _ uuid.UUID) (string, bool) {
			return "", false
		},
		AgentShare: func(_ context.Context, _, _ uuid.UUID) (string, bool) {
			return permissions.ShareEditor, true // explicit editor
		},
		TeamMembership: func(_ context.Context, _, _ uuid.UUID) (string, bool) {
			return permissions.ShareMember, true // implicit member
		},
	})

	ctx := context.Background()
	got := r.CheckAccess(ctx, uuid.New(), uuid.New(), nil, permissions.ActionWriteFile)
	if !got {
		t.Error("explicit editor + implicit member should allow write (min=member), got deny")
	}
}

func TestResolver_ExplicitEditorAlone_AllowsWrite(t *testing.T) {
	// Sanity: explicit editor, no team overlap → write allowed.
	r := permissions.NewResolver(permissions.ResolverConfig{
		UserRole: func(_ context.Context, _ uuid.UUID) string { return permissions.ShareMember },
		ProjectGrant: func(_ context.Context, _, _ uuid.UUID) (string, bool) { return "", false },
		AgentShare: func(_ context.Context, _, _ uuid.UUID) (string, bool) {
			return permissions.ShareEditor, true
		},
		TeamMembership: func(_ context.Context, _, _ uuid.UUID) (string, bool) { return "", false },
	})

	ctx := context.Background()
	got := r.CheckAccess(ctx, uuid.New(), uuid.New(), nil, permissions.ActionWriteFile)
	if !got {
		t.Error("explicit editor alone should allow write, got deny")
	}
}

func TestResolver_DefaultDeny_NoGrants(t *testing.T) {
	// No grants at all → default deny for any action.
	r := permissions.NewResolver(permissions.ResolverConfig{
		UserRole:       func(_ context.Context, _ uuid.UUID) string { return permissions.ShareMember },
		ProjectGrant:   func(_ context.Context, _, _ uuid.UUID) (string, bool) { return "", false },
		AgentShare:     func(_ context.Context, _, _ uuid.UUID) (string, bool) { return "", false },
		TeamMembership: func(_ context.Context, _, _ uuid.UUID) (string, bool) { return "", false },
	})

	ctx := context.Background()
	for _, action := range []permissions.Action{permissions.ActionRead, permissions.ActionWriteFile, permissions.ActionDeleteFile} {
		got := r.CheckAccess(ctx, uuid.New(), uuid.New(), nil, action)
		if got {
			t.Errorf("no grants: action %s should be denied, got allow", action)
		}
	}
}

func TestResolver_AdminBypass_NoLayersConsulted(t *testing.T) {
	// Admin user gets access without any grants — project/agent/team stubs panic
	// if called to prove they are short-circuited.
	r := permissions.NewResolver(permissions.ResolverConfig{
		UserRole: func(_ context.Context, _ uuid.UUID) string { return permissions.ShareOwner },
		ProjectGrant: func(_ context.Context, _, _ uuid.UUID) (string, bool) {
			panic("project layer must not be consulted for admin bypass")
		},
		AgentShare: func(_ context.Context, _, _ uuid.UUID) (string, bool) {
			panic("agent share layer must not be consulted for admin bypass")
		},
		TeamMembership: func(_ context.Context, _, _ uuid.UUID) (string, bool) {
			panic("team layer must not be consulted for admin bypass")
		},
	})

	ctx := context.Background()
	// owner/admin should bypass without panicking.
	got := r.CheckAccess(ctx, uuid.New(), uuid.New(), nil, permissions.ActionWriteFile)
	if !got {
		t.Error("owner/admin bypass should allow write, got deny")
	}
}
