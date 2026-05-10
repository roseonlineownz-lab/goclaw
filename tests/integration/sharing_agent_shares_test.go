//go:build integration

package integration

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// ─── Helpers ──────────────────────────────────────────────────────────────

func seedUserForShares(t *testing.T, db *sql.DB) uuid.UUID {
	t.Helper()
	id := uuid.New()
	suffix := id.String()[:8]
	_, err := db.Exec(
		`INSERT INTO users (id, email, password_hash, display_name, role, kind, user_key)
		 VALUES ($1, $2, 'x', 'u', 'member', 'human', $3)`,
		id, "share-"+suffix+"@local", "share-"+suffix,
	)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM users WHERE id = $1", id) })
	return id
}

func seedAgentForShares(t *testing.T, db *sql.DB, ownerID uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.New()
	suffix := id.String()[:8]
	_, err := db.Exec(
		`INSERT INTO agents (id, agent_key, display_name, status, provider, model, owner_id, owner_user_id)
		 VALUES ($1, $2, $3, 'active', 'test', 'm', $4, $5)`,
		id, "shared-"+suffix, "shared-"+suffix, "owner-"+suffix, ownerID,
	)
	if err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM agents WHERE id = $1", id) })
	return id
}

func seedTeamForShares(t *testing.T, db *sql.DB, ownerID uuid.UUID) uuid.UUID {
	t.Helper()
	leadAgent := seedAgentForShares(t, db, ownerID)
	id := uuid.New()
	suffix := id.String()[:8]
	_, err := db.Exec(
		`INSERT INTO agent_teams (id, team_key, name, owner_user_id, lead_agent_id, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		id, "team-"+suffix, "Team "+suffix, ownerID, leadAgent, ownerID.String(),
	)
	if err != nil {
		t.Fatalf("seed team: %v", err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM agent_teams WHERE id = $1", id) })
	return id
}

// ─── Schema shape ─────────────────────────────────────────────────────────

// TestPGAgentSharesColumns asserts the rebuilt agent_shares table has the
// new column shape: id, agent_id, shared_with_user_id NULL, shared_with_team_id
// NULL, role, metadata, created_by, created_at, updated_at.
//
// RED until Phase 04 schema lands.
func TestPGAgentSharesColumns(t *testing.T) {
	db := testDB(t)

	want := map[string]struct{ dataType, isNullable string }{
		"id":                  {"uuid", "NO"},
		"agent_id":            {"uuid", "NO"},
		"shared_with_user_id": {"uuid", "YES"},
		"shared_with_team_id": {"uuid", "YES"},
		"role":                {"character varying", "NO"},
		"metadata":            {"jsonb", "NO"},
		"created_by":          {"uuid", "NO"},
		"created_at":          {"timestamp with time zone", "NO"},
		"updated_at":          {"timestamp with time zone", "NO"},
	}
	for col, want := range want {
		col, want := col, want
		t.Run(col, func(t *testing.T) {
			var dataType, isNullable string
			err := db.QueryRowContext(t.Context(),
				`SELECT data_type, is_nullable FROM information_schema.columns
				  WHERE table_schema = 'public' AND table_name = 'agent_shares' AND column_name = $1`,
				col,
			).Scan(&dataType, &isNullable)
			if err != nil {
				t.Fatalf("agent_shares.%s missing: %v", col, err)
			}
			if dataType != want.dataType {
				t.Errorf("agent_shares.%s: data_type=%q want %q", col, dataType, want.dataType)
			}
			if isNullable != want.isNullable {
				t.Errorf("agent_shares.%s: is_nullable=%q want %q", col, isNullable, want.isNullable)
			}
		})
	}

	// Legacy columns gone.
	for _, gone := range []string{"user_id", "granted_by"} {
		gone := gone
		t.Run("legacy_"+gone+"_removed", func(t *testing.T) {
			var n int
			db.QueryRowContext(t.Context(),
				`SELECT COUNT(*) FROM information_schema.columns
				  WHERE table_schema='public' AND table_name='agent_shares' AND column_name=$1`,
				gone).Scan(&n)
			if n != 0 {
				t.Errorf("agent_shares.%s must be removed (count=%d)", gone, n)
			}
		})
	}
}

// ─── Constraints ──────────────────────────────────────────────────────────

func insertShare(ctx context.Context, db *sql.DB, agentID uuid.UUID, userID, teamID *uuid.UUID, role string, createdBy uuid.UUID) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO agent_shares (agent_id, shared_with_user_id, shared_with_team_id, role, created_by)
		 VALUES ($1, $2, $3, $4, $5)`,
		agentID, userID, teamID, role, createdBy)
	return err
}

func TestPGAgentSharesRoleEnum(t *testing.T) {
	db := testDB(t)
	owner := seedUserForShares(t, db)
	target := seedUserForShares(t, db)
	agent := seedAgentForShares(t, db, owner)

	for _, bad := range []string{"owner", "user", "viewerz", ""} {
		bad := bad
		t.Run("reject_"+bad, func(t *testing.T) {
			if err := insertShare(t.Context(), db, agent, &target, nil, bad, owner); err == nil {
				t.Errorf("role=%q must be rejected", bad)
			}
		})
	}
	for _, good := range []string{"viewer", "member", "editor"} {
		good := good
		t.Run("accept_"+good, func(t *testing.T) {
			id := seedUserForShares(t, db) // unique target per accept attempt
			if err := insertShare(t.Context(), db, agent, &id, nil, good, owner); err != nil {
				t.Errorf("role=%q must be accepted: %v", good, err)
			}
		})
	}
}

func TestPGAgentSharesTargetMutex(t *testing.T) {
	db := testDB(t)
	owner := seedUserForShares(t, db)
	target := seedUserForShares(t, db)
	team := seedTeamForShares(t, db, owner)
	agent := seedAgentForShares(t, db, owner)

	// Both NULL → reject.
	if err := insertShare(t.Context(), db, agent, nil, nil, "viewer", owner); err == nil {
		t.Error("both target columns NULL must be rejected")
	}
	// Both set → reject.
	if err := insertShare(t.Context(), db, agent, &target, &team, "viewer", owner); err == nil {
		t.Error("both target columns set must be rejected")
	}
	// Exactly one (user) → accept.
	if err := insertShare(t.Context(), db, agent, &target, nil, "viewer", owner); err != nil {
		t.Errorf("user-only target must be accepted: %v", err)
	}
	// Exactly one (team) → accept.
	if err := insertShare(t.Context(), db, agent, nil, &team, "member", owner); err != nil {
		t.Errorf("team-only target must be accepted: %v", err)
	}
}

func TestPGAgentSharesUnique(t *testing.T) {
	db := testDB(t)
	owner := seedUserForShares(t, db)
	target := seedUserForShares(t, db)
	team := seedTeamForShares(t, db, owner)
	agent := seedAgentForShares(t, db, owner)

	if err := insertShare(t.Context(), db, agent, &target, nil, "viewer", owner); err != nil {
		t.Fatalf("first user share: %v", err)
	}
	if err := insertShare(t.Context(), db, agent, &target, nil, "editor", owner); err == nil {
		t.Error("duplicate (agent, user) must be rejected")
	}
	if err := insertShare(t.Context(), db, agent, nil, &team, "member", owner); err != nil {
		t.Fatalf("team share: %v", err)
	}
	if err := insertShare(t.Context(), db, agent, nil, &team, "viewer", owner); err == nil {
		t.Error("duplicate (agent, team) must be rejected")
	}
	// Same agent shared to different slots is allowed (different target slot).
	otherUser := seedUserForShares(t, db)
	if err := insertShare(t.Context(), db, agent, &otherUser, nil, "viewer", owner); err != nil {
		t.Errorf("user share to different user must be accepted: %v", err)
	}
}

func TestPGAgentSharesCascadeOnAgentDelete(t *testing.T) {
	db := testDB(t)
	owner := seedUserForShares(t, db)
	target := seedUserForShares(t, db)
	agent := seedAgentForShares(t, db, owner)

	if err := insertShare(t.Context(), db, agent, &target, nil, "viewer", owner); err != nil {
		t.Fatalf("seed share: %v", err)
	}
	if _, err := db.Exec("DELETE FROM agents WHERE id = $1", agent); err != nil {
		t.Fatalf("delete agent: %v", err)
	}
	var n int
	db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM agent_shares WHERE agent_id = $1`, agent).Scan(&n)
	if n != 0 {
		t.Errorf("agent delete must cascade shares; got %d remaining", n)
	}
}

func TestPGAgentSharesCascadeOnTeamDelete(t *testing.T) {
	db := testDB(t)
	owner := seedUserForShares(t, db)
	team := seedTeamForShares(t, db, owner)
	agent := seedAgentForShares(t, db, owner)

	if err := insertShare(t.Context(), db, agent, nil, &team, "member", owner); err != nil {
		t.Fatalf("seed team share: %v", err)
	}
	if _, err := db.Exec("DELETE FROM agent_teams WHERE id = $1", team); err != nil {
		t.Fatalf("delete team: %v", err)
	}
	var n int
	db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM agent_shares WHERE shared_with_team_id = $1`, team).Scan(&n)
	if n != 0 {
		t.Errorf("team delete must cascade team-target shares; got %d remaining", n)
	}
}

// ─── Indexes (named) ──────────────────────────────────────────────────────

func TestPGAgentSharesIndexesExist(t *testing.T) {
	db := testDB(t)

	want := []string{
		"idx_agent_shares_agent",
		"idx_agent_shares_user",
		"idx_agent_shares_team",
	}
	for _, name := range want {
		name := name
		t.Run(name, func(t *testing.T) {
			var n int
			db.QueryRowContext(t.Context(),
				`SELECT COUNT(*) FROM pg_indexes WHERE schemaname='public' AND tablename='agent_shares' AND indexname=$1`,
				name).Scan(&n)
			if n != 1 {
				t.Errorf("expected index %s to exist (got count=%d)", name, n)
			}
		})
	}
}

// ─── Defaults ─────────────────────────────────────────────────────────────

func TestPGAgentSharesUpdatedAtDefault(t *testing.T) {
	db := testDB(t)
	owner := seedUserForShares(t, db)
	target := seedUserForShares(t, db)
	agent := seedAgentForShares(t, db, owner)

	if err := insertShare(t.Context(), db, agent, &target, nil, "viewer", owner); err != nil {
		t.Fatalf("seed: %v", err)
	}
	var updated time.Time
	if err := db.QueryRowContext(t.Context(),
		`SELECT updated_at FROM agent_shares WHERE agent_id = $1 AND shared_with_user_id = $2`,
		agent, target).Scan(&updated); err != nil {
		t.Fatalf("select updated_at: %v", err)
	}
	if updated.IsZero() {
		t.Error("updated_at should default to now()")
	}
}

// ─── Sanity: error message mentions our constraint names ──────────────────

func TestPGAgentSharesConstraintsHaveDescriptiveErrors(t *testing.T) {
	db := testDB(t)
	owner := seedUserForShares(t, db)
	agent := seedAgentForShares(t, db, owner)

	err := insertShare(t.Context(), db, agent, nil, nil, "viewer", owner)
	if err == nil {
		t.Fatal("expected target-mutex rejection")
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "target") && !strings.Contains(msg, "check") {
		t.Errorf("error message should mention target/check constraint, got %q", err.Error())
	}
}
