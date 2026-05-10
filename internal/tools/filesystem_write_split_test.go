package tools

// filesystem_write_split_test.go verifies the new-vs-overwrite gate split:
//   - new file      → requires write_file grant
//   - existing file → requires edit_file grant (overwrite semantics)
//
// Also verifies the deny-glob layer blocks .env* writes even with a full grant.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// writeFileOnlyStore allows write_file but denies edit_file.
type writeFileOnlyStore struct{ stubGlobStore }

func (w *writeFileOnlyStore) CheckPermission(_ context.Context, _ uuid.UUID, _, configType, _ string) (bool, error) {
	if configType == store.ConfigTypeWriteFile {
		return true, nil
	}
	return false, nil // edit_file denied
}

// editFileOnlyStore allows edit_file but denies write_file.
type editFileOnlyStore struct{ stubGlobStore }

func (e *editFileOnlyStore) CheckPermission(_ context.Context, _ uuid.UUID, _, configType, _ string) (bool, error) {
	if configType == store.ConfigTypeEditFile {
		return true, nil
	}
	return false, nil // write_file denied
}

func groupWriteCtx(t *testing.T) context.Context {
	t.Helper()
	ctx := context.Background()
	ctx = store.WithUserID(ctx, "group:telegram:-100456")
	ctx = store.WithSenderID(ctx, "42")
	ctx = store.WithAgentID(ctx, uuid.New())
	return ctx
}

// --- New file requires write_file grant ---

func TestWriteFileTool_NewFile_WriteFileGrantRequired(t *testing.T) {
	dir := t.TempDir()
	tool := NewWriteFileTool(dir, true)
	ps := &writeFileOnlyStore{stubGlobStore{globs: store.DefaultDenyGlobs}}
	tool.SetConfigPermStore(ps)

	result := tool.Execute(groupWriteCtx(t), map[string]any{
		"path":    "new_file.txt",
		"content": "hello",
	})
	if result.IsError {
		t.Fatalf("new file with write_file grant should succeed, got: %s", result.ForLLM)
	}
	if _, err := os.Stat(filepath.Join(dir, "new_file.txt")); os.IsNotExist(err) {
		t.Fatal("file was not created")
	}
}

func TestWriteFileTool_NewFile_EditFileGrantDenied(t *testing.T) {
	dir := t.TempDir()
	tool := NewWriteFileTool(dir, true)
	// edit_file allowed but write_file not — new file creation must fail.
	ps := &editFileOnlyStore{stubGlobStore{globs: store.DefaultDenyGlobs}}
	tool.SetConfigPermStore(ps)

	result := tool.Execute(groupWriteCtx(t), map[string]any{
		"path":    "new_file.txt",
		"content": "hello",
	})
	if !result.IsError {
		t.Fatal("new file without write_file grant should fail, got success")
	}
}

// --- Existing file requires edit_file grant ---

func TestWriteFileTool_ExistingFile_EditFileGrantRequired(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "existing.txt")
	os.WriteFile(existing, []byte("old"), 0644) //nolint:errcheck

	tool := NewWriteFileTool(dir, true)
	ps := &editFileOnlyStore{stubGlobStore{globs: store.DefaultDenyGlobs}}
	tool.SetConfigPermStore(ps)

	result := tool.Execute(groupWriteCtx(t), map[string]any{
		"path":    "existing.txt",
		"content": "new",
	})
	if result.IsError {
		t.Fatalf("overwrite with edit_file grant should succeed, got: %s", result.ForLLM)
	}
	data, _ := os.ReadFile(existing)
	if string(data) != "new" {
		t.Fatalf("file content not updated: got %q", string(data))
	}
}

func TestWriteFileTool_ExistingFile_WriteFileGrantDenied(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "existing.txt")
	os.WriteFile(existing, []byte("old"), 0644) //nolint:errcheck

	tool := NewWriteFileTool(dir, true)
	// write_file allowed but edit_file not — overwrite must fail.
	ps := &writeFileOnlyStore{stubGlobStore{globs: store.DefaultDenyGlobs}}
	tool.SetConfigPermStore(ps)

	result := tool.Execute(groupWriteCtx(t), map[string]any{
		"path":    "existing.txt",
		"content": "new",
	})
	if !result.IsError {
		t.Fatal("overwrite without edit_file grant should fail, got success")
	}
	// File must not be modified.
	data, _ := os.ReadFile(existing)
	if string(data) != "old" {
		t.Fatalf("file was modified despite denied grant: got %q", string(data))
	}
}

// --- Deny-glob overrides grant ---

func TestWriteFileTool_DenyGlob_BlocksEnvWrite(t *testing.T) {
	dir := t.TempDir()
	tool := NewWriteFileTool(dir, true)
	// Grant write_file, but baseline deny-globs must block .env.
	ps := &writeFileOnlyStore{stubGlobStore{globs: store.DefaultDenyGlobs}}
	tool.SetConfigPermStore(ps)

	result := tool.Execute(groupWriteCtx(t), map[string]any{
		"path":    ".env",
		"content": "SECRET=x",
	})
	if !result.IsError {
		t.Fatal("expected deny-glob block on .env write, got success")
	}
	if _, err := os.Stat(filepath.Join(dir, ".env")); !os.IsNotExist(err) {
		t.Fatal(".env file was created despite deny-glob block")
	}
}

func TestWriteFileTool_DenyGlob_BlocksSecretsDir(t *testing.T) {
	dir := t.TempDir()
	tool := NewWriteFileTool(dir, true)
	ps := &writeFileOnlyStore{stubGlobStore{globs: store.DefaultDenyGlobs}}
	tool.SetConfigPermStore(ps)

	result := tool.Execute(groupWriteCtx(t), map[string]any{
		"path":    "secrets/api.key",
		"content": "secret-value",
	})
	if !result.IsError {
		t.Fatal("expected deny-glob block on secrets/api.key write, got success")
	}
}

// TestWriteFileTool_DenyGlob_DmContextNoPermStore verifies that deny-globs block
// sensitive paths even in DM/web/desktop contexts (no permStore, no group prefix).
// This is the regression for the bypass where the context-prefix gate was removed.
func TestWriteFileTool_DenyGlob_DmContextNoPermStore(t *testing.T) {
	dir := t.TempDir()
	tool := NewWriteFileTool(dir, true)
	// No permStore — simulates DM/web/desktop context where group context is absent.

	ctx := context.Background()
	ctx = store.WithAgentID(ctx, uuid.New())
	ctx = store.WithUserID(ctx, "user:alice") // DM context, not group:

	for _, path := range []string{".env", "secrets/api.txt", ".git/config", "id_rsa.key", "cert.pem"} {
		result := tool.Execute(ctx, map[string]any{
			"path":    path,
			"content": "SENSITIVE=data",
		})
		if !result.IsError {
			t.Errorf("DM context path %q: expected deny-glob block even without permStore, got success", path)
		}
		if _, err := os.Stat(filepath.Join(dir, path)); !os.IsNotExist(err) {
			t.Errorf("DM context path %q: file was created on disk despite deny-glob", path)
		}
	}
}
