package agent

import "testing"

// shouldShareWorkspace is now driven solely by the agent's share_workspace
// flag. peerKind / userID arguments are kept for signature compatibility but
// no longer affect the decision (DM/group/users-list distinctions retired
// with the legacy WorkspaceSharing JSONB blob).

func TestShouldShareWorkspace_DefaultFalse(t *testing.T) {
	l := &Loop{}
	if l.shouldShareWorkspace("user1", "direct") {
		t.Error("default Loop should not share workspace")
	}
}

func TestShouldShareWorkspace_FlagTrue(t *testing.T) {
	l := &Loop{shareWorkspace: true}
	if !l.shouldShareWorkspace("user1", "direct") {
		t.Error("share_workspace=true should share for direct")
	}
	if !l.shouldShareWorkspace("group:tg:-100", "group") {
		t.Error("share_workspace=true should share for group")
	}
	if !l.shouldShareWorkspace("anyone", "unknown") {
		t.Error("share_workspace=true should share regardless of peerKind")
	}
}

func TestShouldShareWorkspace_FlagFalseRejectsAll(t *testing.T) {
	l := &Loop{shareWorkspace: false}
	for _, peer := range []string{"direct", "group", "unknown", ""} {
		if l.shouldShareWorkspace("user", peer) {
			t.Errorf("share_workspace=false should reject peerKind=%q", peer)
		}
	}
}

func TestShouldShareFlags_Independence(t *testing.T) {
	// share_memory must not turn on workspace sharing, and vice versa.
	a := &Loop{shareMemory: true}
	if a.shouldShareWorkspace("user", "direct") {
		t.Error("shareMemory must not enable workspace sharing")
	}

	b := &Loop{shareWorkspace: true}
	if b.shouldShareMemory() {
		t.Error("shareWorkspace must not enable memory sharing")
	}
}
