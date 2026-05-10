//go:build integration

package integration

import (
	"database/sql"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/permissions"
)

// resolverSeedTeam creates a team with a lead agent (lead_agent_id NOT NULL).
func resolverSeedTeam(t *testing.T, db *sql.DB, ownerID uuid.UUID) uuid.UUID {
	t.Helper()
	leadAgent := seedAgentForShares(t, db, ownerID)
	id := uuid.New()
	suffix := id.String()[:8]
	if _, err := db.Exec(
		`INSERT INTO agent_teams (id, team_key, name, owner_user_id, lead_agent_id, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		id, "rt-team-"+suffix, "T-"+suffix, ownerID, leadAgent, ownerID.String(),
	); err != nil {
		t.Fatalf("seed team: %v", err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM agent_teams WHERE id = $1", id) })
	return id
}

func grantUserToTeam(t *testing.T, db *sql.DB, teamID, userID uuid.UUID, role string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO team_user_grants (team_id, user_id, role) VALUES ($1, $2, $3)`,
		teamID, userID, role,
	); err != nil {
		t.Fatalf("grant user to team: %v", err)
	}
	t.Cleanup(func() {
		db.Exec("DELETE FROM team_user_grants WHERE team_id = $1 AND user_id = $2", teamID, userID)
	})
}

// TestAgentAccessResolver covers the truth-table for ResolveRole:
//   - ownership
//   - explicit user grant (viewer/member/editor)
//   - implicit team grant via team_user_grants
//   - precedence: editor (explicit) > member (implicit) when sources mix
//   - no relation → ShareNone
//   - revoked share → ShareNone
//   - team deletion cascades implicit grant
func TestAgentAccessResolver(t *testing.T) {
	db := testDB(t)
	resolver := permissions.NewAgentAccessResolver(db)
	ctx := t.Context()

	// Common actors.
	owner := seedUserForShares(t, db)
	memberUser := seedUserForShares(t, db)
	teamMate := seedUserForShares(t, db)
	stranger := seedUserForShares(t, db)
	team := resolverSeedTeam(t, db, owner)
	grantUserToTeam(t, db, team, teamMate, "member")
	agent := seedAgentForShares(t, db, owner)

	t.Run("owner", func(t *testing.T) {
		got, err := resolver.ResolveRole(ctx, owner, agent)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if got != permissions.ShareOwner {
			t.Errorf("owner: got %q, want %q", got, permissions.ShareOwner)
		}
	})

	t.Run("stranger gets none", func(t *testing.T) {
		got, err := resolver.ResolveRole(ctx, stranger, agent)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if got != permissions.ShareNone {
			t.Errorf("stranger: got %q, want none", got)
		}
	})

	t.Run("explicit viewer grant", func(t *testing.T) {
		_, _ = db.Exec(`DELETE FROM agent_shares WHERE agent_id = $1`, agent)
		_, err := db.Exec(`INSERT INTO agent_shares
			(id, agent_id, shared_with_user_id, role, created_by)
			VALUES (uuid_generate_v7(), $1, $2, 'viewer', $3)`, agent, memberUser, owner)
		if err != nil {
			t.Fatalf("insert share: %v", err)
		}
		got, err := resolver.ResolveRole(ctx, memberUser, agent)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if got != permissions.ShareViewer {
			t.Errorf("got %q, want viewer", got)
		}
	})

	t.Run("implicit team member grant", func(t *testing.T) {
		_, _ = db.Exec(`DELETE FROM agent_shares WHERE agent_id = $1`, agent)
		_, err := db.Exec(`INSERT INTO agent_shares
			(id, agent_id, shared_with_team_id, role, created_by)
			VALUES (uuid_generate_v7(), $1, $2, 'member', $3)`, agent, team, owner)
		if err != nil {
			t.Fatalf("insert team share: %v", err)
		}
		got, err := resolver.ResolveRole(ctx, teamMate, agent)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if got != permissions.ShareMember {
			t.Errorf("got %q, want member", got)
		}
		// Stranger (no team membership) still none.
		got2, _ := resolver.ResolveRole(ctx, stranger, agent)
		if got2 != permissions.ShareNone {
			t.Errorf("stranger after team-only share: got %q, want none", got2)
		}
	})

	t.Run("explicit editor wins over implicit member", func(t *testing.T) {
		_, _ = db.Exec(`DELETE FROM agent_shares WHERE agent_id = $1`, agent)
		// Team member grant — implicit path.
		_, _ = db.Exec(`INSERT INTO agent_shares
			(id, agent_id, shared_with_team_id, role, created_by)
			VALUES (uuid_generate_v7(), $1, $2, 'member', $3)`, agent, team, owner)
		// Same user has explicit editor grant.
		_, err := db.Exec(`INSERT INTO agent_shares
			(id, agent_id, shared_with_user_id, role, created_by)
			VALUES (uuid_generate_v7(), $1, $2, 'editor', $3)`, agent, teamMate, owner)
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
		got, err := resolver.ResolveRole(ctx, teamMate, agent)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if got != permissions.ShareEditor {
			t.Errorf("got %q, want editor (precedence)", got)
		}
	})

	t.Run("revoke share returns none", func(t *testing.T) {
		_, _ = db.Exec(`DELETE FROM agent_shares WHERE agent_id = $1`, agent)
		got, err := resolver.ResolveRole(ctx, memberUser, agent)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if got != permissions.ShareNone {
			t.Errorf("after revoke: got %q, want none", got)
		}
	})

	t.Run("team delete cascades implicit grant", func(t *testing.T) {
		// Re-seed independent team + grant + share to avoid prior cleanup races.
		freshTeam := resolverSeedTeam(t, db, owner)
		freshUser := seedUserForShares(t, db)
		grantUserToTeam(t, db, freshTeam, freshUser, "member")
		if _, err := db.Exec(`INSERT INTO agent_shares
			(id, agent_id, shared_with_team_id, role, created_by)
			VALUES (uuid_generate_v7(), $1, $2, 'member', $3)`, agent, freshTeam, owner); err != nil {
			t.Fatalf("insert: %v", err)
		}
		// Sanity: grant exists.
		if got, _ := resolver.ResolveRole(ctx, freshUser, agent); got != permissions.ShareMember {
			t.Fatalf("pre-delete: got %q, want member", got)
		}
		// Cascade.
		if _, err := db.Exec(`DELETE FROM agent_teams WHERE id = $1`, freshTeam); err != nil {
			t.Fatalf("delete team: %v", err)
		}
		got, _ := resolver.ResolveRole(ctx, freshUser, agent)
		if got != permissions.ShareNone {
			t.Errorf("post-team-delete: got %q, want none", got)
		}
	})
}
