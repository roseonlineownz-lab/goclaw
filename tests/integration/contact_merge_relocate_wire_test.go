//go:build integration

// Tests that OnGroupContactsMerged callback is invoked by MergeUserAggregate
// when the merged contacts include group-peer rows.
package integration

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/store/pg"
)

// TestMergeRelocateCallback_InvokedForGroupContacts verifies that
// MergeUserAggregate calls OnGroupContactsMerged after commit when the
// ContactIDs set contains at least one group-peer contact.
func TestMergeRelocateCallback_InvokedForGroupContacts(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	suf := uuid.New().String()[:8]

	_, agentID := seedTenantAgent(t, db)
	_ = agentID
	sourceUser := seedMergeUserM(t, db, "relwire-src-"+suf)
	targetUser := seedMergeUserM(t, db, "relwire-tgt-"+suf)

	// Seed a group-peer contact linked to sourceUser.
	groupContactID := uuid.New()
	_, err := db.Exec(
		`INSERT INTO channel_contacts (id, channel_type, sender_id, contact_type, peer_kind, user_id)
		 VALUES ($1, 'telegram', $2, 'group', 'group', $3)`,
		groupContactID, "grp-relwire-"+suf, sourceUser,
	)
	if err != nil {
		t.Fatalf("seed group contact: %v", err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM channel_contacts WHERE id = $1", groupContactID) })

	// Stub callback captures invocations.
	var mu sync.Mutex
	var captured []uuid.UUID
	callback := func(ids []uuid.UUID) {
		mu.Lock()
		captured = append(captured, ids...)
		mu.Unlock()
	}

	contacts := pg.NewPGContactStore(db)
	mergeErr := contacts.MergeUserAggregate(ctx, store.MergeUserAggregateRequest{
		ContactIDs:            []uuid.UUID{groupContactID},
		SourceUserIDs:         []uuid.UUID{sourceUser},
		TargetUserID:          targetUser,
		MergeAudit:            []byte(`{"merged_by":"test"}`),
		OnGroupContactsMerged: callback,
	})
	if mergeErr != nil {
		t.Fatalf("MergeUserAggregate: %v", mergeErr)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(captured) == 0 {
		t.Fatal("OnGroupContactsMerged was not called — group contacts present but callback not invoked")
	}
	found := false
	for _, id := range captured {
		if id == groupContactID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("OnGroupContactsMerged did not include group contact ID %s; got %v", groupContactID, captured)
	}
}

// TestMergeRelocateCallback_NotInvokedForDMOnly verifies that
// OnGroupContactsMerged is NOT called when ContactIDs contains only DM contacts.
func TestMergeRelocateCallback_NotInvokedForDMOnly(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	suf := uuid.New().String()[:8]

	_, agentID := seedTenantAgent(t, db)
	_ = agentID
	sourceUser := seedMergeUserM(t, db, "relwire2-src-"+suf)
	targetUser := seedMergeUserM(t, db, "relwire2-tgt-"+suf)

	// Seed a DM (direct) contact — peer_kind = 'direct'.
	dmContactID := uuid.New()
	_, err := db.Exec(
		`INSERT INTO channel_contacts (id, channel_type, sender_id, contact_type, peer_kind, user_id)
		 VALUES ($1, 'telegram', $2, 'user', 'direct', $3)`,
		dmContactID, "dm-relwire2-"+suf, sourceUser,
	)
	if err != nil {
		t.Fatalf("seed DM contact: %v", err)
	}
	t.Cleanup(func() { db.Exec("DELETE FROM channel_contacts WHERE id = $1", dmContactID) })

	called := false
	contacts := pg.NewPGContactStore(db)
	mergeErr := contacts.MergeUserAggregate(ctx, store.MergeUserAggregateRequest{
		ContactIDs:            []uuid.UUID{dmContactID},
		SourceUserIDs:         []uuid.UUID{sourceUser},
		TargetUserID:          targetUser,
		MergeAudit:            []byte(`{"merged_by":"test"}`),
		OnGroupContactsMerged: func(_ []uuid.UUID) { called = true },
	})
	if mergeErr != nil {
		t.Fatalf("MergeUserAggregate: %v", mergeErr)
	}
	if called {
		t.Error("OnGroupContactsMerged was called despite no group contacts in merge set")
	}
}
