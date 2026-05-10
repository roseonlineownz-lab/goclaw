package permissions_test

// resolver_test.go — TDD truth-table for the 4-layer AND-intersect resolver.
//
// Layer order: (1) privacy gate, (2) user.role baseline, (3) project_grants,
// (4) agent_shares (explicit + implicit team). AND-intersect → min role across
// active layers. If any layer rejects, access denied.
//
// Tests are RED until Phase 05 implements NewResolver / CheckAccess.

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
)

// --- Stub functions for resolver dependencies ---

type roleResult struct {
	role  string
	found bool
}

func makeResolver(
	userRole string,
	projectGrant roleResult,    // project layer (found=false → layer skipped)
	agentShare roleResult,      // agent share layer
	teamMembership roleResult,  // implicit team membership layer
) *permissions.Resolver {
	return permissions.NewResolver(permissions.ResolverConfig{
		UserRole: func(_ context.Context, _ uuid.UUID) string { return userRole },
		ProjectGrant: func(_ context.Context, _, _ uuid.UUID) (string, bool) {
			return projectGrant.role, projectGrant.found
		},
		AgentShare: func(_ context.Context, _, _ uuid.UUID) (string, bool) {
			return agentShare.role, agentShare.found
		},
		TeamMembership: func(_ context.Context, _, _ uuid.UUID) (string, bool) {
			return teamMembership.role, teamMembership.found
		},
	})
}

const noProject = "" // sentinel for nil projectID

func checkAccess(r *permissions.Resolver, userRole, action, projectID string) bool {
	ctx := context.Background()
	userID := uuid.New()
	agentID := uuid.New()
	var projID *uuid.UUID
	if projectID != noProject {
		id := uuid.New()
		projID = &id
	}
	return r.CheckAccess(ctx, userID, agentID, projID, permissions.Action(action))
}

// --- Truth table ---

func TestResolver_TruthTable(t *testing.T) {
	cases := []struct {
		name           string
		userRole       string
		projectGrant   roleResult
		agentShare     roleResult
		teamMembership roleResult
		projectID      string // noProject = layer skipped
		action         string
		wantAllow      bool
	}{
		// Row 1: admin bypass — no other layers consulted.
		{
			name: "admin_bypass_write_file",
			userRole: permissions.ShareOwner, // admin maps to owner in share vocabulary
			projectGrant:   roleResult{found: false},
			agentShare:     roleResult{found: false},
			teamMembership: roleResult{found: false},
			projectID: noProject,
			action:    string(permissions.ActionWriteFile),
			wantAllow: true,
		},
		// Row 2: member + editor everywhere → allow write.
		{
			name:           "member_editor_everywhere_write_allow",
			userRole:       permissions.ShareMember,
			projectGrant:   roleResult{role: permissions.ShareEditor, found: true},
			agentShare:     roleResult{role: permissions.ShareEditor, found: true},
			teamMembership: roleResult{role: permissions.ShareMember, found: true},
			projectID:      "bound",
			action:         string(permissions.ActionWriteFile),
			wantAllow:      true,
		},
		// Row 3: project caps at viewer → deny write.
		{
			name:           "project_viewer_cap_denies_write",
			userRole:       permissions.ShareMember,
			projectGrant:   roleResult{role: permissions.ShareViewer, found: true},
			agentShare:     roleResult{role: permissions.ShareEditor, found: true},
			teamMembership: roleResult{role: permissions.ShareMember, found: true},
			projectID:      "bound",
			action:         string(permissions.ActionWriteFile),
			wantAllow:      false,
		},
		// Row 4: implicit team membership → allow read.
		{
			name:           "implicit_team_member_allows_read",
			userRole:       permissions.ShareMember,
			projectGrant:   roleResult{found: false},
			agentShare:     roleResult{found: false},
			teamMembership: roleResult{role: permissions.ShareMember, found: true},
			projectID:      noProject,
			action:         string(permissions.ActionRead),
			wantAllow:      true,
		},
		// Row 5: agent share capped at viewer → deny write.
		{
			name:           "agent_viewer_share_denies_write",
			userRole:       permissions.ShareMember,
			projectGrant:   roleResult{found: false},
			agentShare:     roleResult{role: permissions.ShareViewer, found: true},
			teamMembership: roleResult{found: false},
			projectID:      noProject,
			action:         string(permissions.ActionWriteFile),
			wantAllow:      false,
		},
		// Row 6: viewer user + editor grants → allow read.
		{
			name:           "viewer_user_editor_grants_allow_read",
			userRole:       permissions.ShareViewer,
			projectGrant:   roleResult{role: permissions.ShareEditor, found: true},
			agentShare:     roleResult{role: permissions.ShareEditor, found: true},
			teamMembership: roleResult{role: permissions.ShareEditor, found: true},
			projectID:      "bound",
			action:         string(permissions.ActionRead),
			wantAllow:      true,
		},
		// Row 7: viewer user caps write even with editor grants.
		{
			name:           "viewer_user_caps_write_deny",
			userRole:       permissions.ShareViewer,
			projectGrant:   roleResult{role: permissions.ShareEditor, found: true},
			agentShare:     roleResult{role: permissions.ShareEditor, found: true},
			teamMembership: roleResult{role: permissions.ShareEditor, found: true},
			projectID:      "bound",
			action:         string(permissions.ActionWriteFile),
			wantAllow:      false,
		},
		// Row 8: member user, no grants at all → default deny (read).
		{
			name:           "no_grants_default_deny",
			userRole:       permissions.ShareMember,
			projectGrant:   roleResult{found: false},
			agentShare:     roleResult{found: false},
			teamMembership: roleResult{found: false},
			projectID:      noProject,
			action:         string(permissions.ActionRead),
			wantAllow:      false,
		},
		// Row 9: member + project editor + implicit team → allow write.
		{
			name:           "member_project_editor_implicit_team_allow_write",
			userRole:       permissions.ShareMember,
			projectGrant:   roleResult{role: permissions.ShareEditor, found: true},
			agentShare:     roleResult{found: false},
			teamMembership: roleResult{role: permissions.ShareMember, found: true},
			projectID:      "bound",
			action:         string(permissions.ActionWriteFile),
			wantAllow:      true,
		},
		// Row 11: nil projectID → project layer skipped; agent editor → allow write.
		{
			name:           "nil_project_skips_project_layer",
			userRole:       permissions.ShareMember,
			projectGrant:   roleResult{found: false},
			agentShare:     roleResult{role: permissions.ShareEditor, found: true},
			teamMembership: roleResult{found: false},
			projectID:      noProject,
			action:         string(permissions.ActionWriteFile),
			wantAllow:      true,
		},
		// Row 12: explicit + implicit team grant is idempotent → allow write.
		{
			name:           "explicit_and_implicit_team_idempotent",
			userRole:       permissions.ShareMember,
			projectGrant:   roleResult{role: permissions.ShareEditor, found: true},
			agentShare:     roleResult{role: permissions.ShareEditor, found: true},
			teamMembership: roleResult{role: permissions.ShareEditor, found: true},
			projectID:      "bound",
			action:         string(permissions.ActionWriteFile),
			wantAllow:      true,
		},
		// Cron action requires member level — same AND-intersect logic.
		{
			name:           "member_editor_everywhere_cron_allow",
			userRole:       permissions.ShareMember,
			projectGrant:   roleResult{found: false},
			agentShare:     roleResult{role: permissions.ShareEditor, found: true},
			teamMembership: roleResult{found: false},
			projectID:      noProject,
			action:         string(permissions.ActionCron),
			wantAllow:      true,
		},
		// Delete action requires member level.
		{
			name:           "viewer_agent_share_denies_delete",
			userRole:       permissions.ShareMember,
			projectGrant:   roleResult{found: false},
			agentShare:     roleResult{role: permissions.ShareViewer, found: true},
			teamMembership: roleResult{found: false},
			projectID:      noProject,
			action:         string(permissions.ActionDeleteFile),
			wantAllow:      false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := makeResolver(tc.userRole,
				tc.projectGrant,
				tc.agentShare,
				tc.teamMembership,
			)
			ctx := context.Background()
			userID := uuid.New()
			agentID := uuid.New()
			var projID *uuid.UUID
			if tc.projectID != noProject {
				id := uuid.New()
				projID = &id
			}
			got := r.CheckAccess(ctx, userID, agentID, projID, permissions.Action(tc.action))
			if got != tc.wantAllow {
				t.Errorf("CheckAccess = %v, want %v", got, tc.wantAllow)
			}
		})
	}
}
