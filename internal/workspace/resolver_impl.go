package workspace

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// defaultResolver implements Resolver. v4 production routes ALL non-project
// sessions through ResolveChannel (12-scenario channel/web matrix). Resolve()
// only handles the project-priority branch; all other paths come from
// ResolveChannel via the Loop's wrapper in agent/loop_workspace_channel.go.
//
// Stateless — all inputs come via ResolveParams. No DB queries.
// Does NOT import tools package (avoids circular dependency).
type defaultResolver struct{}

// NewResolver creates a workspace Resolver.
func NewResolver() Resolver { return &defaultResolver{} }

// Resolve returns the project workspace for sessions bound to a project.
// Non-project paths are NOT supported here — call ResolveChannel for those.
func (r *defaultResolver) Resolve(_ context.Context, params ResolveParams) (*WorkspaceContext, error) {
	if params.BaseDir == "" {
		return nil, fmt.Errorf("workspace: base dir is required")
	}
	if params.ProjectID == nil || params.ProjectSlug == "" {
		return nil, fmt.Errorf("workspace: Resolve requires ProjectID + ProjectSlug; non-project paths must use ResolveChannel")
	}
	return r.resolveProject(params)
}

// resolveProject handles sessions bound to a specific project.
// The project folder is the active path; slug validation is re-confirmed here
// so a bad slug stored in the DB cannot escape the projects directory.
//
// Uses p.BaseDir so project paths share the same root as the channel/web
// resolver (single workspace root invariant — see ResolveChannel).
func (r *defaultResolver) resolveProject(p ResolveParams) (*WorkspaceContext, error) {
	path, err := ProjectWorkspacePath(p.BaseDir, p.ProjectSlug)
	if err != nil {
		return nil, fmt.Errorf("workspace: project resolution failed: %w", err)
	}
	ensureDir(path)
	owner := p.UserID
	if owner == "" {
		owner = p.ChatID
	}
	wc := &WorkspaceContext{
		ActivePath:       path,
		Scope:            ScopeProject,
		OwnerID:          owner,
		MemoryScope:      "user",
		KGScope:          "user",
		EnforcementLabel: DefaultEnforcementLabel(ScopeProject, false),
		ProjectID:        p.ProjectID,
		ProjectSlug:      p.ProjectSlug,
	}
	return wc, nil
}

// SanitizeSegment makes a string safe for filesystem path use.
// Replaces any character that is not ASCII alphanumeric, hyphen, or underscore
// with '_'. Used by both the workspace resolver and the FS-backed memory writer
// to build scope-derived directory paths safely.
func SanitizeSegment(s string) string {
	return sanitizeSegment(s)
}

// sanitizeSegment is the internal implementation; exported as SanitizeSegment.
// Mirrors tools.SanitizePathSegment without importing tools package.
func sanitizeSegment(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// ensureDir creates workspace directory (0755 for personal/delegate).
func ensureDir(path string) {
	if err := os.MkdirAll(path, 0755); err != nil {
		slog.Warn("workspace: failed to create directory", "path", path, "err", err)
	}
}

// ensureDirTeam creates team workspace directory (0750 — more restrictive).
func ensureDirTeam(path string) {
	if err := os.MkdirAll(path, 0750); err != nil {
		slog.Warn("workspace: failed to create team directory", "path", path, "err", err)
	}
}
