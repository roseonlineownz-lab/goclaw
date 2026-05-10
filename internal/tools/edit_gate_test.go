package tools

// edit_gate_test.go confirms the edit tool uses the edit_file gate (not write_file)
// and that the deny-glob layer is also applied on edits.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// alwaysDenyForEditFile denies only ConfigTypeEditFile, allows everything else.
type alwaysDenyForEditFile struct{ stubGlobStore }

func (a *alwaysDenyForEditFile) CheckPermission(_ context.Context, _ uuid.UUID, _, configType, _ string) (bool, error) {
	if configType == store.ConfigTypeEditFile {
		return false, nil
	}
	return true, nil
}

func TestEditTool_UsesEditFileGate(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "hello.txt")
	os.WriteFile(target, []byte("hello world"), 0644) //nolint:errcheck

	tool := NewEditTool(dir, true)
	ps := &alwaysDenyForEditFile{stubGlobStore{globs: store.DefaultDenyGlobs}}
	tool.SetConfigPermStore(ps)

	ctx := context.Background()
	ctx = store.WithUserID(ctx, "group:telegram:-100456")
	ctx = store.WithSenderID(ctx, "42")
	ctx = store.WithAgentID(ctx, uuid.New())

	result := tool.Execute(ctx, map[string]any{
		"path":       "hello.txt",
		"old_string": "hello",
		"new_string": "goodbye",
	})
	if !result.IsError {
		t.Fatal("expected edit_file gate to deny, got success")
	}
}

func TestEditTool_DenyGlob_BlocksEnvEdit(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")
	os.WriteFile(envFile, []byte("SECRET=old"), 0644) //nolint:errcheck

	tool := NewEditTool(dir, true)
	// Allow the edit_file gate but enforce baseline deny-globs.
	ps := &stubGlobStore{globs: store.DefaultDenyGlobs}
	tool.SetConfigPermStore(ps)

	ctx := context.Background()
	ctx = store.WithUserID(ctx, "group:telegram:-100456")
	ctx = store.WithSenderID(ctx, "42")
	ctx = store.WithAgentID(ctx, uuid.New())

	result := tool.Execute(ctx, map[string]any{
		"path":       ".env",
		"old_string": "SECRET=old",
		"new_string": "SECRET=new",
	})
	if !result.IsError {
		t.Fatal("expected deny-glob block on .env edit, got success")
	}
	// Verify file was NOT modified.
	data, _ := os.ReadFile(envFile)
	if string(data) != "SECRET=old" {
		t.Fatalf(".env was modified despite deny-glob: got %q", string(data))
	}
}
