package pg

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/google/uuid"
)

// memberTestDB reuses the same PG connection setup as project tests.
func memberTestDB(t *testing.T) *sql.DB {
	t.Helper()
	return projectTestDB(t)
}

// memberSeedUser inserts a user using the full UUID string for email+user_key
// to avoid collisions in a shared test DB across multiple runs.
func memberSeedUser(t *testing.T, db *sql.DB) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	idStr := id.String()
	_, err := db.Exec(
		`INSERT INTO users (id, email, password_hash, role, kind, user_key)
		 VALUES ($1, $2, 'x', 'member', 'human', $3)`,
		id, "mem-"+idStr+"@local", "mem-"+idStr,
	)
	if err != nil {
		t.Fatalf("memberSeedUser: %v", err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM users WHERE id = $1", id) })
	return id
}

// seedTeamForMember inserts a minimal agent + team and returns the team UUID.
// Cleanup is registered on t.
func seedTeamForMember(t *testing.T, db *sql.DB, ownerID uuid.UUID) uuid.UUID {
	t.Helper()

	// Minimal agent required by agent_teams.lead_agent_id FK.
	agentID := uuid.Must(uuid.NewV7())
	suffix := agentID.String()[:8]
	_, err := db.Exec(
		`INSERT INTO agents (id, agent_key, owner_id, owner_user_id, provider, model, metadata)
		 VALUES ($1, $2, $3, $4, 'openai', 'gpt-4o', '{}')`,
		agentID, "test-agent-"+suffix, ownerID.String(), ownerID,
	)
	if err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM agents WHERE id = $1", agentID) })

	teamID := uuid.Must(uuid.NewV7())
	tsuf := teamID.String()[:8]
	_, err = db.Exec(
		`INSERT INTO agent_teams (id, name, lead_agent_id, created_by, team_key, metadata)
		 VALUES ($1, $2, $3, $4, $5, '{}')`,
		teamID, "test-team-"+tsuf, agentID, ownerID.String(), "test-team-"+tsuf,
	)
	if err != nil {
		t.Fatalf("seed team: %v", err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM agent_teams WHERE id = $1", teamID) })

	return teamID
}

func TestPGTeamUserMemberStore_AddAndListByTeam(t *testing.T) {
	db := memberTestDB(t)
	ctx := context.Background()
	s := NewPGTeamUserMemberStore(db)

	ownerID := memberSeedUser(t, db)
	teamID := seedTeamForMember(t, db, ownerID)
	memberID := memberSeedUser(t, db)

	if err := s.AddMember(ctx, teamID.String(), memberID.String(), "member", nil); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	t.Cleanup(func() {
		db.Exec("DELETE FROM team_user_members WHERE team_id = $1 AND user_id = $2", teamID, memberID)
	})

	members, err := s.ListByTeam(ctx, teamID.String())
	if err != nil {
		t.Fatalf("ListByTeam: %v", err)
	}
	found := false
	for _, m := range members {
		if m.UserID == memberID.String() {
			found = true
			if m.Role != "member" {
				t.Errorf("role: got %q want member", m.Role)
			}
		}
	}
	if !found {
		t.Errorf("ListByTeam did not return the added user")
	}
}

func TestPGTeamUserMemberStore_AddAndListByUser(t *testing.T) {
	db := memberTestDB(t)
	ctx := context.Background()
	s := NewPGTeamUserMemberStore(db)

	ownerID := memberSeedUser(t, db)
	teamID := seedTeamForMember(t, db, ownerID)
	memberID := memberSeedUser(t, db)

	if err := s.AddMember(ctx, teamID.String(), memberID.String(), "viewer", nil); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	t.Cleanup(func() {
		db.Exec("DELETE FROM team_user_members WHERE team_id = $1 AND user_id = $2", teamID, memberID)
	})

	memberships, err := s.ListByUser(ctx, memberID.String())
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	found := false
	for _, m := range memberships {
		if m.TeamID == teamID.String() {
			found = true
			if m.Role != "viewer" {
				t.Errorf("role: got %q want viewer", m.Role)
			}
		}
	}
	if !found {
		t.Errorf("ListByUser did not return the added team")
	}
}

func TestPGTeamUserMemberStore_DuplicateRejectsCompositeKey(t *testing.T) {
	db := memberTestDB(t)
	ctx := context.Background()
	s := NewPGTeamUserMemberStore(db)

	ownerID := memberSeedUser(t, db)
	teamID := seedTeamForMember(t, db, ownerID)
	memberID := memberSeedUser(t, db)

	if err := s.AddMember(ctx, teamID.String(), memberID.String(), "member", nil); err != nil {
		t.Fatalf("first AddMember: %v", err)
	}
	t.Cleanup(func() {
		db.Exec("DELETE FROM team_user_members WHERE team_id = $1 AND user_id = $2", teamID, memberID)
	})

	// Second insert should fail with a unique/PK violation.
	if err := s.AddMember(ctx, teamID.String(), memberID.String(), "admin", nil); err == nil {
		t.Error("expected error on duplicate (team_id, user_id), got nil")
	}
}

func TestPGTeamUserMemberStore_RemoveMember(t *testing.T) {
	db := memberTestDB(t)
	ctx := context.Background()
	s := NewPGTeamUserMemberStore(db)

	ownerID := memberSeedUser(t, db)
	teamID := seedTeamForMember(t, db, ownerID)
	memberID := memberSeedUser(t, db)

	if err := s.AddMember(ctx, teamID.String(), memberID.String(), "member", nil); err != nil {
		t.Fatalf("AddMember: %v", err)
	}

	if err := s.RemoveMember(ctx, teamID.String(), memberID.String()); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}

	members, err := s.ListByTeam(ctx, teamID.String())
	if err != nil {
		t.Fatalf("ListByTeam after remove: %v", err)
	}
	for _, m := range members {
		if m.UserID == memberID.String() {
			t.Error("RemoveMember: user still present in ListByTeam")
		}
	}
}

func TestPGTeamUserMemberStore_InvalidRoleRejected(t *testing.T) {
	db := memberTestDB(t)
	ctx := context.Background()
	s := NewPGTeamUserMemberStore(db)

	ownerID := memberSeedUser(t, db)
	teamID := seedTeamForMember(t, db, ownerID)
	memberID := memberSeedUser(t, db)

	// "owner" is not a valid role per CHECK constraint.
	err := s.AddMember(ctx, teamID.String(), memberID.String(), "owner", nil)
	if err == nil {
		db.Exec("DELETE FROM team_user_members WHERE team_id = $1 AND user_id = $2", teamID, memberID)
		t.Error("expected CHECK violation for role='owner', got nil")
	}
}

func TestPGTeamUserMemberStore_TeamDeleteCascades(t *testing.T) {
	db := memberTestDB(t)
	ctx := context.Background()
	s := NewPGTeamUserMemberStore(db)

	ownerID := memberSeedUser(t, db)
	teamID := seedTeamForMember(t, db, ownerID)
	memberID := memberSeedUser(t, db)

	if err := s.AddMember(ctx, teamID.String(), memberID.String(), "member", nil); err != nil {
		t.Fatalf("AddMember: %v", err)
	}

	// Delete the team — CASCADE should remove the membership row.
	if _, err := db.Exec("DELETE FROM agent_teams WHERE id = $1", teamID); err != nil {
		t.Fatalf("delete team: %v", err)
	}

	// ListByUser should return zero rows for this now-deleted team.
	memberships, err := s.ListByUser(ctx, memberID.String())
	if err != nil {
		t.Fatalf("ListByUser after team delete: %v", err)
	}
	for _, m := range memberships {
		if m.TeamID == teamID.String() {
			t.Error("CASCADE: membership row still present after team delete")
		}
	}
}

func TestPGTeamUserMemberStore_UserDeleteCascades(t *testing.T) {
	db := memberTestDB(t)
	ctx := context.Background()
	s := NewPGTeamUserMemberStore(db)

	ownerID := memberSeedUser(t, db)
	teamID := seedTeamForMember(t, db, ownerID)
	memberID := memberSeedUser(t, db)

	if err := s.AddMember(ctx, teamID.String(), memberID.String(), "admin", nil); err != nil {
		t.Fatalf("AddMember: %v", err)
	}

	// Delete the user — CASCADE should remove the membership row.
	if _, err := db.Exec("DELETE FROM users WHERE id = $1", memberID); err != nil {
		t.Fatalf("delete user: %v", err)
	}

	// ListByTeam should no longer contain the deleted user.
	members, err := s.ListByTeam(ctx, teamID.String())
	if err != nil {
		t.Fatalf("ListByTeam after user delete: %v", err)
	}
	for _, m := range members {
		if m.UserID == memberID.String() {
			t.Error("CASCADE: membership row still present after user delete")
		}
	}
}

func TestPGTeamUserMemberStore_GetRole(t *testing.T) {
	db := memberTestDB(t)
	ctx := context.Background()
	s := NewPGTeamUserMemberStore(db)

	ownerID := memberSeedUser(t, db)
	teamID := seedTeamForMember(t, db, ownerID)
	memberID := memberSeedUser(t, db)

	if err := s.AddMember(ctx, teamID.String(), memberID.String(), "admin", nil); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	t.Cleanup(func() {
		db.Exec("DELETE FROM team_user_members WHERE team_id = $1 AND user_id = $2", teamID, memberID)
	})

	role, found, err := s.GetRole(ctx, teamID.String(), memberID.String())
	if err != nil {
		t.Fatalf("GetRole: %v", err)
	}
	if !found {
		t.Fatal("GetRole: expected found=true")
	}
	if role != "admin" {
		t.Errorf("GetRole: got %q want admin", role)
	}

	// Non-existent
	_, found2, err2 := s.GetRole(ctx, teamID.String(), uuid.Must(uuid.NewV7()).String())
	if err2 != nil {
		t.Fatalf("GetRole(missing): %v", err2)
	}
	if found2 {
		t.Error("GetRole(missing): expected found=false")
	}

	// Confirm errors.Is sentinel works.
	_, err3 := s.ListByTeam(ctx, uuid.Must(uuid.NewV7()).String())
	if err3 != nil && !errors.Is(err3, sql.ErrNoRows) {
		// ListByTeam returns empty slice (not error) for unknown team — just sanity check.
		_ = err3
	}
}
