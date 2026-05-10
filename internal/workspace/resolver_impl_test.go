package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestResolve_EnforcementLabel verifies the human-readable workspace label
// returned by DefaultEnforcementLabel for each Scope value.
func TestResolve_EnforcementLabel(t *testing.T) {
	tests := []struct {
		name   string
		scope  Scope
		shared bool
		substr string
	}{
		{"personal", ScopePersonal, false, "personal workspace"},
		{"team_shared", ScopeTeam, true, "shared team workspace"},
		{"team_isolated", ScopeTeam, false, "isolated team workspace"},
		{"delegate", ScopeDelegate, false, "delegated task"},
		{"project", ScopeProject, false, "project workspace"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			label := DefaultEnforcementLabel(tt.scope, tt.shared)
			if !strings.Contains(label, tt.substr) {
				t.Errorf("label = %q, missing %q", label, tt.substr)
			}
		})
	}
}

// TestResolve_EmptyBaseDir verifies the resolver rejects empty BaseDir.
func TestResolve_EmptyBaseDir(t *testing.T) {
	r := NewResolver()
	_, err := r.Resolve(context.Background(), ResolveParams{
		AgentID: "agent-1",
		UserID:  "user-1",
	})
	if err == nil {
		t.Error("expected error for empty BaseDir")
	}
}

// TestResolve_RequiresProject verifies that v4's Resolve only handles the
// project-priority branch — non-project paths must use ResolveChannel.
func TestResolve_RequiresProject(t *testing.T) {
	r := NewResolver()
	_, err := r.Resolve(context.Background(), ResolveParams{
		AgentID: "agent-1",
		UserID:  "user-1",
		BaseDir: t.TempDir(),
	})
	if err == nil {
		t.Error("expected error: Resolve without ProjectID must reject and direct caller to ResolveChannel")
	}
}

// TestResolve_ProjectPriority verifies that when ProjectID + ProjectSlug are set
// the workspace resolver routes the session to the project workspace path under
// p.BaseDir and returns ScopeProject.
func TestResolve_ProjectPriority(t *testing.T) {
	base := t.TempDir()

	projectID := uuid.MustParse("01900000-0000-7000-8000-000000000001")
	slug := "my-project"

	r := NewResolver()
	wc, err := r.Resolve(context.Background(), ResolveParams{
		AgentID:     "agent-1",
		UserID:      "user-1",
		BaseDir:     base,
		ProjectID:   &projectID,
		ProjectSlug: slug,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if wc.Scope != ScopeProject {
		t.Errorf("Scope = %q, want project", wc.Scope)
	}
	if wc.ProjectID == nil || *wc.ProjectID != projectID {
		t.Errorf("ProjectID = %v, want %v", wc.ProjectID, projectID)
	}
	if wc.ProjectSlug != slug {
		t.Errorf("ProjectSlug = %q, want %q", wc.ProjectSlug, slug)
	}
	// Project path is rooted under p.BaseDir (single-root invariant).
	want := filepath.Join(base, "projects", slug)
	if wc.ActivePath != want {
		t.Errorf("ActivePath = %q, want %q", wc.ActivePath, want)
	}
	assertDirExists(t, wc.ActivePath)
}

func assertDirExists(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Errorf("directory %q does not exist: %v", path, err)
	} else if !info.IsDir() {
		t.Errorf("%q exists but is not a directory", path)
	}
}
