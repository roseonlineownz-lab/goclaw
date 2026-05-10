package bootstrap

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// SeedToStore in v4 takes (ctx, store, agentID) — no agentType parameter.
// The body unconditionally seeds the full predefined template set.
//
// RED until Phase 04 changes the signature.
func TestSeedToStore_NoAgentTypeParam(t *testing.T) {
	as := newSeedStub()
	agentID := uuid.New()

	seeded, err := SeedToStore(context.Background(), as, agentID)
	if err != nil {
		t.Fatalf("SeedToStore: %v", err)
	}

	got := make(map[string]bool, len(seeded))
	for _, name := range seeded {
		got[name] = true
	}
	// Agent-level seed includes the canonical predefined templates.
	// BOOTSTRAP.md is per-user only (handled by SeedUserFiles), so it must NOT appear here.
	for _, want := range []string{AgentsFile, SoulFile, IdentityFile, UserFile} {
		if !got[want] {
			t.Errorf("expected seeded set to contain %q, got %v", want, seeded)
		}
	}
	if got[BootstrapFile] {
		t.Errorf("BOOTSTRAP.md must not be seeded at agent level (it is per-user only); got %v", seeded)
	}
}

// USER.md is the only per-user seed file constant; the legacy
// UserPredefinedFile constant was removed in v4. The CI grep gate also
// guards against stray references.
func TestUserFile_IsCanonicalUserMD(t *testing.T) {
	if UserFile != "USER.md" {
		t.Errorf("UserFile = %q, want %q", UserFile, "USER.md")
	}
}

// SeedUserFiles drops the agentType parameter and the BOOTSTRAP_PREDEFINED.md
// rename branch; the canonical BOOTSTRAP.md is always used.
//
// RED until Phase 04 changes the signature + body.
func TestSeedUserFiles_NoAgentTypeParam(t *testing.T) {
	as := newSeedStub()
	agentID := uuid.New()
	wizardUserMD := "wizard-personalised user content"
	as.agentFiles[UserFile] = wizardUserMD

	seeded, err := SeedUserFiles(context.Background(), as, agentID, "user-alice", false, nil)
	if err != nil {
		t.Fatalf("SeedUserFiles: %v", err)
	}

	if len(seeded) == 0 {
		t.Fatalf("expected SeedUserFiles to seed at least one file")
	}

	got, ok := as.seededUserFiles[UserFile]
	if !ok {
		t.Fatalf("expected USER.md to be seeded for the user")
	}
	if !strings.Contains(got, wizardUserMD) {
		t.Errorf("expected agent-level USER.md to win as user seed, got %q", got)
	}

	// BOOTSTRAP.md must be seeded under its canonical name (no _PREDEFINED rename).
	if _, ok := as.seededUserFiles[BootstrapFile]; !ok {
		t.Errorf("expected %q to be seeded; got %v", BootstrapFile, keysOf(as.seededUserFiles))
	}
}

// EmbeddedUserFiles takes no arguments after the purge.
//
// RED until Phase 04 changes the signature.
func TestEmbeddedUserFiles_NoAgentTypeParam(t *testing.T) {
	files := EmbeddedUserFiles()
	if len(files) == 0 {
		t.Fatalf("expected at least one embedded user file")
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
