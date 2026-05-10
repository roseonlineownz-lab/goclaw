package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// alwaysDenyPermStore always denies CheckPermission so delete is blocked.
type alwaysDenyPermStore struct{ stubGlobStore }

func (a *alwaysDenyPermStore) CheckPermission(_ context.Context, _ uuid.UUID, _, _, _ string) (bool, error) {
	return false, nil
}

func TestDeleteFileTool_AllowedPath_DeletesFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "delete_me.txt")
	if err := os.WriteFile(target, []byte("bye"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := NewDeleteFileTool(dir, true)
	result := tool.Execute(context.Background(), map[string]any{"path": "delete_me.txt"})
	if result.IsError {
		t.Fatalf("expected success, got error: %s", result.ForLLM)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("file should have been deleted")
	}
}

func TestDeleteFileTool_MissingFile_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	tool := NewDeleteFileTool(dir, true)
	result := tool.Execute(context.Background(), map[string]any{"path": "ghost.txt"})
	if !result.IsError {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestDeleteFileTool_EmptyPath_ReturnsError(t *testing.T) {
	tool := NewDeleteFileTool(t.TempDir(), true)
	result := tool.Execute(context.Background(), map[string]any{})
	if !result.IsError {
		t.Fatal("expected error for empty path, got nil")
	}
}

func TestDeleteFileTool_PathTraversal_Rejected(t *testing.T) {
	dir := t.TempDir()
	tool := NewDeleteFileTool(dir, true)
	result := tool.Execute(context.Background(), map[string]any{"path": "../../etc/passwd"})
	if !result.IsError {
		t.Fatal("expected path traversal rejection, got nil error")
	}
}

func TestDeleteFileTool_PermDenied_GroupContext(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "safe.txt")
	os.WriteFile(target, []byte("x"), 0644) //nolint:errcheck

	tool := NewDeleteFileTool(dir, true)
	// Wire a store that always denies CheckPermission.
	ps := &alwaysDenyPermStore{stubGlobStore{globs: store.DefaultDenyGlobs}}
	tool.SetConfigPermStore(ps)

	// Simulate group context so the gate is active.
	ctx := context.Background()
	ctx = store.WithUserID(ctx, "group:telegram:-100456")
	ctx = store.WithSenderID(ctx, "42")
	ctx = store.WithAgentID(ctx, uuid.New())

	result := tool.Execute(ctx, map[string]any{"path": "safe.txt"})
	if !result.IsError {
		t.Fatal("expected permission denied, got nil error")
	}
}

func TestDeleteFileTool_DenyGlob_BlocksEvenWithGrant(t *testing.T) {
	dir := t.TempDir()
	// Create a .env file inside workspace.
	envFile := filepath.Join(dir, ".env")
	os.WriteFile(envFile, []byte("SECRET=x"), 0644) //nolint:errcheck

	tool := NewDeleteFileTool(dir, true)
	// Allow permission but keep baseline deny-glob patterns.
	ps := &stubGlobStore{globs: store.DefaultDenyGlobs}
	tool.SetConfigPermStore(ps)

	ctx := context.Background()
	ctx = store.WithUserID(ctx, "group:telegram:-100456")
	ctx = store.WithSenderID(ctx, "42")
	ctx = store.WithAgentID(ctx, uuid.New())

	result := tool.Execute(ctx, map[string]any{"path": ".env"})
	if !result.IsError {
		t.Fatal("expected deny-glob block on .env delete, got nil error")
	}
	// File must NOT have been deleted.
	if _, err := os.Stat(envFile); os.IsNotExist(err) {
		t.Fatal(".env file was deleted despite deny-glob block")
	}
}

func TestWorkspaceRelPath(t *testing.T) {
	cases := []struct {
		resolved  string
		workspace string
		want      string
	}{
		{"/ws/foo/bar.go", "/ws", "foo/bar.go"},
		{"/ws/foo/bar.go", "/ws/", "foo/bar.go"},
		{"/ws/.env", "/ws", ".env"},
		{"/other/file.go", "/ws", "file.go"}, // outside workspace → base name
		{"/ws/secrets/api.txt", "/ws", "secrets/api.txt"},
	}
	for _, tc := range cases {
		got := workspaceRelPath(tc.resolved, tc.workspace)
		if got != tc.want {
			t.Errorf("workspaceRelPath(%q, %q) = %q, want %q", tc.resolved, tc.workspace, got, tc.want)
		}
	}
}
