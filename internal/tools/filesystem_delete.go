package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// DeleteFileTool removes a file from the workspace.
// Requires delete_file permission in group/guild contexts.
// Deny-glob patterns are enforced after the permission gate: even a granted user
// cannot delete files matching baseline patterns (.env*, secrets/**, .git/**, *.key, *.pem).
type DeleteFileTool struct {
	workspace       string
	restrict        bool
	allowedPrefixes []string
	deniedPrefixes  []string
	permStore       store.ConfigPermissionStore // nil = no group delete restriction
}

func NewDeleteFileTool(workspace string, restrict bool) *DeleteFileTool {
	return &DeleteFileTool{workspace: workspace, restrict: restrict}
}

// AllowPaths adds extra path prefixes that delete_file is allowed to access.
func (t *DeleteFileTool) AllowPaths(prefixes ...string) {
	t.allowedPrefixes = append(t.allowedPrefixes, prefixes...)
}

// DenyPaths adds path prefixes that delete_file must reject unconditionally.
func (t *DeleteFileTool) DenyPaths(prefixes ...string) {
	t.deniedPrefixes = append(t.deniedPrefixes, prefixes...)
}

// SetConfigPermStore enables group delete permission checks.
func (t *DeleteFileTool) SetConfigPermStore(s store.ConfigPermissionStore) {
	t.permStore = s
}

func (t *DeleteFileTool) Name() string { return "delete_file" }
func (t *DeleteFileTool) Description() string {
	return "Delete a file in the workspace folder. Use with caution — this operation is irreversible."
}

func (t *DeleteFileTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "File path to delete (relative to workspace, or absolute)",
			},
		},
		"required": []string{"path"},
	}
}

func (t *DeleteFileTool) Execute(ctx context.Context, args map[string]any) *Result {
	path, _ := args["path"].(string)
	if path == "" {
		return ErrorResult("path is required")
	}

	// Group delete permission check.
	if t.permStore != nil {
		if err := store.CheckDeleteFilePermission(ctx, t.permStore); err != nil {
			return ErrorResult(err.Error())
		}
	}

	// Host execution — use per-user workspace from context if available.
	workspace := ToolWorkspaceFromCtx(ctx)
	if workspace == "" {
		workspace = t.workspace
	}
	allowed := allowedWriteWithTeamWorkspace(ctx, t.allowedPrefixes)
	resolved, err := resolvePathWithAllowed(path, workspace, effectiveRestrict(ctx, t.restrict), allowed)
	if err != nil {
		return ErrorResult(err.Error())
	}
	if err := checkDeniedPath(resolved, t.workspace, t.deniedPrefixes); err != nil {
		return ErrorResult(err.Error())
	}

	// Deny-glob layer: overrides a grant for protected paths.
	relPath := workspaceRelPath(resolved, workspace)
	if err := permissions.CheckDenyGlobs(ctx, t.permStore, relPath); err != nil {
		return ErrorResult(err.Error())
	}

	if err := os.Remove(resolved); err != nil {
		if os.IsNotExist(err) {
			return ErrorResult(fmt.Sprintf("file not found: %s", path))
		}
		return ErrorResult(fmt.Sprintf("failed to delete file: %v", err))
	}

	return SilentResult(fmt.Sprintf("File deleted: %s", path))
}

// workspaceRelPath returns a workspace-relative forward-slash path for glob matching.
// Both resolved and workspace are first run through filepath.EvalSymlinks so that
// macOS /var → /private/var symlinks don't cause prefix-strip failures. Returns
// the base name when resolved cannot be made relative to workspace.
func workspaceRelPath(resolved, workspace string) string {
	if workspace == "" {
		return filepath.Base(resolved)
	}
	// Resolve symlinks on both sides so the prefix comparison is reliable.
	// Ignore errors — fall back to raw paths.
	if r, err := filepath.EvalSymlinks(resolved); err == nil {
		resolved = r
	}
	if w, err := filepath.EvalSymlinks(workspace); err == nil {
		workspace = w
	}
	// Ensure workspace has trailing separator for clean prefix trim.
	wsPrefix := workspace
	if !strings.HasSuffix(wsPrefix, string(filepath.Separator)) {
		wsPrefix += string(filepath.Separator)
	}
	rel := strings.TrimPrefix(resolved, wsPrefix)
	if rel == resolved {
		// resolved is not under workspace — return base name as fallback.
		return filepath.Base(resolved)
	}
	return filepath.ToSlash(rel)
}
