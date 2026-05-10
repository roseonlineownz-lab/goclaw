package tools

// Baseline deny-glob matrix tests.
//
// Verify the 5 baseline patterns block protected paths even when an agent holds
// a broad grant. Check order:
//  1. Folder gate (CheckEditFilePermission) → passes
//  2. Deny-glob (CheckDenyGlobs)            → must DENY for each sensitive path
//
// Tests exercise CheckDenyGlobs directly (unit scope) with a stub store.
// Integration over full Execute() is in filesystem_write_split_test.go and
// filesystem_delete_test.go.

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// stubGlobStore implements store.ConfigPermissionStore returning fixed globs.
type stubGlobStore struct {
	globs []string
}

func (s *stubGlobStore) CheckPermission(_ context.Context, _ uuid.UUID, _, _, _ string) (bool, error) {
	return true, nil
}
func (s *stubGlobStore) Grant(_ context.Context, _ *store.ConfigPermission) error { return nil }
func (s *stubGlobStore) Revoke(_ context.Context, _ uuid.UUID, _, _, _ string) error { return nil }
func (s *stubGlobStore) List(_ context.Context, _ uuid.UUID, _, _ string) ([]store.ConfigPermission, error) {
	return nil, nil
}
func (s *stubGlobStore) ListWriters(_ context.Context, _ uuid.UUID, _, _ string) ([]store.ConfigPermission, error) {
	return nil, nil
}
func (s *stubGlobStore) GetDenyGlobs(_ context.Context, _ uuid.UUID, _, _ string) ([]string, error) {
	return s.globs, nil
}

// baselineMatrix maps each workspace-relative path to whether it should be
// blocked by the 5 baseline patterns.
var baselineMatrix = []struct {
	path    string
	blocked bool
}{
	// .env* — dotenv variants
	{".env", true},
	{".env.local", true},
	{".env.production", true},
	{".envrc", true},
	// secrets/** — includes nested and hidden files
	{"secrets/api.txt", true},
	{"secrets/nested/key.json", true},
	{"secrets/.hidden", true},
	// .git/** — git internals
	{".git/config", true},
	{".git/objects/pack/file", true},
	// *.key
	{"id_rsa.key", true},
	{"deploy.key", true},
	// *.pem
	{"cert.pem", true},
	{"ca-bundle.pem", true},
	// benign — must NOT be blocked
	{"src/main.go", false},
	{"README.md", false},
	{"config/app.json", false},
	{"envfile.txt", false},  // not a dotfile
	{"mysecrets.txt", false}, // "secrets" not in path prefix
}

func groupCtxForDenyTest() context.Context {
	ctx := context.Background()
	ctx = store.WithUserID(ctx, "group:telegram:-100456")
	ctx = store.WithSenderID(ctx, "42")
	ctx = store.WithAgentID(ctx, uuid.New())
	return ctx
}

func TestDenyGlobsBaseline_GroupContext(t *testing.T) {
	ctx := groupCtxForDenyTest()
	ps := &stubGlobStore{globs: store.DefaultDenyGlobs}

	for _, tc := range baselineMatrix {
		err := permissions.CheckDenyGlobs(ctx, ps, tc.path)
		if tc.blocked && err == nil {
			t.Errorf("path %q: expected deny-glob block, got nil", tc.path)
		}
		if !tc.blocked && err != nil {
			t.Errorf("path %q: expected no block, got %v", tc.path, err)
		}
	}
}

func TestDenyGlobsBaseline_NonGroupContext_AlsoBlocks(t *testing.T) {
	// Deny-globs now apply universally — DM, web, desktop contexts all blocked.
	ctx := store.WithUserID(context.Background(), "user:alice")
	ctx = store.WithAgentID(ctx, uuid.New())
	ps := &stubGlobStore{globs: store.DefaultDenyGlobs}

	for _, path := range []string{".env", "secrets/api.txt", ".git/config"} {
		if err := permissions.CheckDenyGlobs(ctx, ps, path); err == nil {
			t.Errorf("non-group path %q: expected deny-glob block, got nil", path)
		}
	}
}

func TestDenyGlobsBaseline_NilStore_DefaultGlobsApply(t *testing.T) {
	// Even with nil permStore the default globs protect sensitive paths.
	ctx := context.Background()
	ctx = store.WithAgentID(ctx, uuid.New())

	for _, path := range []string{".env", "secrets/api.txt", ".git/config", "id_rsa.key", "cert.pem"} {
		if err := permissions.CheckDenyGlobs(ctx, nil, path); err == nil {
			t.Errorf("nil store path %q: expected deny-glob block, got nil", path)
		}
	}
	// Benign paths must still be allowed.
	for _, path := range []string{"src/main.go", "README.md", "config/app.json"} {
		if err := permissions.CheckDenyGlobs(ctx, nil, path); err != nil {
			t.Errorf("nil store path %q: expected no block, got %v", path, err)
		}
	}
}

func TestDenyGlobsBaseline_EmptyPath_NoBlock(t *testing.T) {
	ctx := groupCtxForDenyTest()
	ps := &stubGlobStore{globs: store.DefaultDenyGlobs}
	if err := permissions.CheckDenyGlobs(ctx, ps, ""); err != nil {
		t.Errorf("empty path: expected nil, got %v", err)
	}
}
