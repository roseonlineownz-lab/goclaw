package store

import "testing"

// TestAgentDataZeroValueShareFlags asserts the zero value of AgentData
// has both share flags defaulting to false.
//
// RED until Phase 02 lands the new fields on AgentData.
func TestAgentDataZeroValueShareFlags(t *testing.T) {
	var a AgentData
	if a.ShareWorkspace {
		t.Errorf("zero AgentData.ShareWorkspace: want false, got true")
	}
	if a.ShareMemory {
		t.Errorf("zero AgentData.ShareMemory: want false, got true")
	}
}

// TestAgentDataShareFlagsIndependent asserts the two flags are
// independent fields — flipping one must not affect the other.
//
// RED until Phase 02 lands the new fields on AgentData.
func TestAgentDataShareFlagsIndependent(t *testing.T) {
	var a AgentData
	a.ShareMemory = true
	if a.ShareWorkspace {
		t.Errorf("ShareWorkspace must remain false when only ShareMemory is set")
	}

	var b AgentData
	b.ShareWorkspace = true
	if b.ShareMemory {
		t.Errorf("ShareMemory must remain false when only ShareWorkspace is set")
	}
}
