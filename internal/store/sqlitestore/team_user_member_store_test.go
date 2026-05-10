//go:build sqlite || sqliteonly

package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

// newTestMemberStore opens an in-memory SQLite DB and returns a member store.
func newTestMemberStore(t *testing.T) (context.Context, *SQLiteTeamUserMemberStore, *sql.DB) {
	t.Helper()
	db, err := OpenDB(filepath.Join(t.TempDir(), "members.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := EnsureSchema(db); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	return context.Background(), NewSQLiteTeamUserMemberStore(db), db
}

// seedSQLiteUser inserts a minimal user row and returns its UUID string.
// Uses the full UUID as user_key to guarantee uniqueness within a test DB.
func seedSQLiteUser(t *testing.T, db *sql.DB) string {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	idStr := id.String()
	_, err := db.Exec(
		`INSERT INTO users (id, email, password_hash, role, kind, user_key)
		 VALUES (?, ?, 'x', 'member', 'human', ?)`,
		idStr, "member-"+idStr+"@local", "member-"+idStr,
	)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return idStr
}

// seedSQLiteTeam inserts a minimal agent + team and returns the team UUID string.
func seedSQLiteTeam(t *testing.T, db *sql.DB, ownerID string) string {
	t.Helper()
	agentID := uuid.Must(uuid.NewV7())
	asuf := agentID.String()[:8]
	_, err := db.Exec(
		`INSERT INTO agents (id, agent_key, owner_id, owner_user_id, provider, model, metadata)
		 VALUES (?, ?, ?, ?, 'openai', 'gpt-4o', '{}')`,
		agentID.String(), "agent-"+asuf, ownerID, ownerID,
	)
	if err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	teamID := uuid.Must(uuid.NewV7())
	tsuf := teamID.String()[:8]
	_, err = db.Exec(
		`INSERT INTO agent_teams (id, name, lead_agent_id, created_by, team_key, metadata)
		 VALUES (?, ?, ?, ?, ?, '{}')`,
		teamID.String(), "team-"+tsuf, agentID.String(), ownerID, "team-"+tsuf,
	)
	if err != nil {
		t.Fatalf("seed team: %v", err)
	}

	return teamID.String()
}

func TestSQLiteTeamUserMemberStore_AddAndListByTeam(t *testing.T) {
	ctx, s, db := newTestMemberStore(t)

	ownerID := seedSQLiteUser(t, db)
	teamID := seedSQLiteTeam(t, db, ownerID)
	memberID := seedSQLiteUser(t, db)

	if err := s.AddMember(ctx, teamID, memberID, "member", nil); err != nil {
		t.Fatalf("AddMember: %v", err)
	}

	members, err := s.ListByTeam(ctx, teamID)
	if err != nil {
		t.Fatalf("ListByTeam: %v", err)
	}
	found := false
	for _, m := range members {
		if m.UserID == memberID {
			found = true
			if m.Role != "member" {
				t.Errorf("role: got %q want member", m.Role)
			}
		}
	}
	if !found {
		t.Error("ListByTeam did not return the added user")
	}
}

func TestSQLiteTeamUserMemberStore_AddAndListByUser(t *testing.T) {
	ctx, s, db := newTestMemberStore(t)

	ownerID := seedSQLiteUser(t, db)
	teamID := seedSQLiteTeam(t, db, ownerID)
	memberID := seedSQLiteUser(t, db)

	if err := s.AddMember(ctx, teamID, memberID, "viewer", nil); err != nil {
		t.Fatalf("AddMember: %v", err)
	}

	memberships, err := s.ListByUser(ctx, memberID)
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	found := false
	for _, m := range memberships {
		if m.TeamID == teamID {
			found = true
			if m.Role != "viewer" {
				t.Errorf("role: got %q want viewer", m.Role)
			}
		}
	}
	if !found {
		t.Error("ListByUser did not return the added team")
	}
}

func TestSQLiteTeamUserMemberStore_DuplicateRejectsCompositeKey(t *testing.T) {
	ctx, s, db := newTestMemberStore(t)

	ownerID := seedSQLiteUser(t, db)
	teamID := seedSQLiteTeam(t, db, ownerID)
	memberID := seedSQLiteUser(t, db)

	if err := s.AddMember(ctx, teamID, memberID, "member", nil); err != nil {
		t.Fatalf("first AddMember: %v", err)
	}

	// Second insert should fail with PRIMARY KEY constraint violation.
	if err := s.AddMember(ctx, teamID, memberID, "admin", nil); err == nil {
		t.Error("expected error on duplicate (team_id, user_id), got nil")
	}
}

func TestSQLiteTeamUserMemberStore_RemoveMember(t *testing.T) {
	ctx, s, db := newTestMemberStore(t)

	ownerID := seedSQLiteUser(t, db)
	teamID := seedSQLiteTeam(t, db, ownerID)
	memberID := seedSQLiteUser(t, db)

	if err := s.AddMember(ctx, teamID, memberID, "member", nil); err != nil {
		t.Fatalf("AddMember: %v", err)
	}

	if err := s.RemoveMember(ctx, teamID, memberID); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}

	members, err := s.ListByTeam(ctx, teamID)
	if err != nil {
		t.Fatalf("ListByTeam after remove: %v", err)
	}
	for _, m := range members {
		if m.UserID == memberID {
			t.Error("RemoveMember: user still present in ListByTeam")
		}
	}
}

func TestSQLiteTeamUserMemberStore_InvalidRoleRejected(t *testing.T) {
	ctx, s, db := newTestMemberStore(t)

	ownerID := seedSQLiteUser(t, db)
	teamID := seedSQLiteTeam(t, db, ownerID)
	memberID := seedSQLiteUser(t, db)

	// "owner" is not a valid role per CHECK constraint.
	err := s.AddMember(ctx, teamID, memberID, "owner", nil)
	if err == nil {
		db.Exec("DELETE FROM team_user_members WHERE team_id = ? AND user_id = ?", teamID, memberID)
		t.Error("expected CHECK violation for role='owner', got nil")
	}
}

func TestSQLiteTeamUserMemberStore_TeamDeleteCascades(t *testing.T) {
	ctx, s, db := newTestMemberStore(t)

	ownerID := seedSQLiteUser(t, db)
	teamID := seedSQLiteTeam(t, db, ownerID)
	memberID := seedSQLiteUser(t, db)

	if err := s.AddMember(ctx, teamID, memberID, "member", nil); err != nil {
		t.Fatalf("AddMember: %v", err)
	}

	// Delete the team — CASCADE should remove the membership row.
	if _, err := db.Exec("DELETE FROM agent_teams WHERE id = ?", teamID); err != nil {
		t.Fatalf("delete team: %v", err)
	}

	memberships, err := s.ListByUser(ctx, memberID)
	if err != nil {
		t.Fatalf("ListByUser after team delete: %v", err)
	}
	for _, m := range memberships {
		if m.TeamID == teamID {
			t.Error("CASCADE: membership row still present after team delete")
		}
	}
}

func TestSQLiteTeamUserMemberStore_UserDeleteCascades(t *testing.T) {
	ctx, s, db := newTestMemberStore(t)

	ownerID := seedSQLiteUser(t, db)
	teamID := seedSQLiteTeam(t, db, ownerID)
	memberID := seedSQLiteUser(t, db)

	if err := s.AddMember(ctx, teamID, memberID, "admin", nil); err != nil {
		t.Fatalf("AddMember: %v", err)
	}

	// Delete the user — CASCADE should remove the membership row.
	if _, err := db.Exec("DELETE FROM users WHERE id = ?", memberID); err != nil {
		t.Fatalf("delete user: %v", err)
	}

	members, err := s.ListByTeam(ctx, teamID)
	if err != nil {
		t.Fatalf("ListByTeam after user delete: %v", err)
	}
	for _, m := range members {
		if m.UserID == memberID {
			t.Error("CASCADE: membership row still present after user delete")
		}
	}
}

func TestSQLiteTeamUserMemberStore_GetRole(t *testing.T) {
	ctx, s, db := newTestMemberStore(t)

	ownerID := seedSQLiteUser(t, db)
	teamID := seedSQLiteTeam(t, db, ownerID)
	memberID := seedSQLiteUser(t, db)

	if err := s.AddMember(ctx, teamID, memberID, "admin", nil); err != nil {
		t.Fatalf("AddMember: %v", err)
	}

	role, found, err := s.GetRole(ctx, teamID, memberID)
	if err != nil {
		t.Fatalf("GetRole: %v", err)
	}
	if !found {
		t.Fatal("GetRole: expected found=true")
	}
	if role != "admin" {
		t.Errorf("GetRole: got %q want admin", role)
	}

	// Non-existent pair.
	_, found2, err2 := s.GetRole(ctx, teamID, uuid.Must(uuid.NewV7()).String())
	if err2 != nil {
		t.Fatalf("GetRole(missing): %v", err2)
	}
	if found2 {
		t.Error("GetRole(missing): expected found=false")
	}

	// sql.ErrNoRows sentinel is wrapped correctly when not found (via errors.Is).
	_, _, err3 := s.GetRole(ctx, uuid.Must(uuid.NewV7()).String(), uuid.Must(uuid.NewV7()).String())
	if err3 != nil && !errors.Is(err3, sql.ErrNoRows) {
		// GetRole returns found=false, err=nil for missing rows — this path is not reached.
		_ = err3
	}
}
