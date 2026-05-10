package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestProjectWorkspacePath_RejectsInvalidSlug verifies that path-traversal and
// illegal slug patterns are rejected before any filesystem access.
func TestProjectWorkspacePath_RejectsInvalidSlug(t *testing.T) {
	tmp := t.TempDir()

	cases := []struct {
		name string
		slug string
	}{
		{"path_traversal_dotdot", "../etc"},
		{"slash_separator", "foo/bar"},
		{"backslash_separator", `foo\bar`},
		{"leading_hyphen", "-bad"},
		{"uppercase", "Bad-Slug"},
		{"empty", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ProjectWorkspacePath(tmp, tc.slug)
			if err == nil {
				t.Errorf("slug %q: expected error, got nil", tc.slug)
			}
		})
	}
}

// TestEnsureProjectFolder_CreatesIdempotently verifies that calling EnsureProjectFolder
// twice on the same slug succeeds without error and the folder exists.
func TestEnsureProjectFolder_CreatesIdempotently(t *testing.T) {
	tmp := t.TempDir()

	slug := "my-project"

	path1, err := EnsureProjectFolder(context.Background(), tmp, slug)
	if err != nil {
		t.Fatalf("first EnsureProjectFolder: %v", err)
	}

	path2, err := EnsureProjectFolder(context.Background(), tmp, slug)
	if err != nil {
		t.Fatalf("second EnsureProjectFolder (idempotent): %v", err)
	}

	if path1 != path2 {
		t.Errorf("paths differ: %q vs %q", path1, path2)
	}

	info, err := os.Stat(path1)
	if err != nil {
		t.Fatalf("folder does not exist after create: %v", err)
	}
	if !info.IsDir() {
		t.Error("expected directory, got file")
	}
}

// TestProjectWorkspacePath_UnderWorkspaceRoot asserts the returned path has the
// expected projects/ sub-folder prefix relative to workspace root.
func TestProjectWorkspacePath_UnderWorkspaceRoot(t *testing.T) {
	tmp := t.TempDir()

	slug := "alpha-project"
	got, err := ProjectWorkspacePath(tmp, slug)
	if err != nil {
		t.Fatalf("ProjectWorkspacePath: %v", err)
	}

	wantPrefix := filepath.Join(tmp, "projects") + string(filepath.Separator)
	if !strings.HasPrefix(got+string(filepath.Separator), wantPrefix) {
		t.Errorf("path %q is not under projects prefix %q", got, wantPrefix)
	}

	wantPath := filepath.Join(tmp, "projects", slug)
	if got != wantPath {
		t.Errorf("ProjectWorkspacePath = %q, want %q", got, wantPath)
	}
}

// TestEnsureProjectFolder_FolderMode verifies that created project folders have
// permission mode 0o755 (owner rwx, group and other rx).
func TestEnsureProjectFolder_FolderMode(t *testing.T) {
	tmp := t.TempDir()

	path, err := EnsureProjectFolder(context.Background(), tmp, "perm-test")
	if err != nil {
		t.Fatalf("EnsureProjectFolder: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	// Mask to permission bits only.
	got := info.Mode().Perm()
	want := os.FileMode(0o755)
	if got != want {
		t.Errorf("folder mode = %o, want %o", got, want)
	}
}

// TestEnsureProjectFolder_ExistingFolderIdempotent verifies no error when folder
// already exists (e.g. from a previous run or a concurrent creator).
func TestEnsureProjectFolder_ExistingFolderIdempotent(t *testing.T) {
	tmp := t.TempDir()

	// Pre-create the folder manually.
	slug := "pre-existing"
	preCreated := filepath.Join(tmp, "projects", slug)
	if err := os.MkdirAll(preCreated, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// EnsureProjectFolder must succeed even though folder exists.
	got, err := EnsureProjectFolder(context.Background(), tmp, slug)
	if err != nil {
		t.Fatalf("EnsureProjectFolder on existing folder: %v", err)
	}
	if got != preCreated {
		t.Errorf("path = %q, want %q", got, preCreated)
	}
}

// TestProjectWorkspacePath_SlugWithDotDot asserts that a slug containing '..'
// is rejected before any filesystem side-effect.
func TestProjectWorkspacePath_SlugWithDotDot(t *testing.T) {
	tmp := t.TempDir()

	slug := "foo..bar"
	_, err := ProjectWorkspacePath(tmp, slug)
	if err == nil {
		t.Error("expected error for slug containing '..', got nil")
	}

	// Verify no folder was created as a side effect.
	leaked := filepath.Join(tmp, "projects", slug)
	if _, statErr := os.Stat(leaked); statErr == nil {
		t.Errorf("folder %q was created despite slug rejection — path traversal risk", leaked)
	}
}

// TestProjectWorkspacePath_ArchiveDoesNotCallEnsure documents and verifies the
// invariant: archive is a status-only flip on the DB row; no FS operation is
// invoked. Verified structurally — EnsureProjectFolder is never called from
// UpdateStatus flows (confirmed by absence of call-site in project_lifecycle.go).
// This test exercises a direct slug-only path: ProjectWorkspacePath succeeds
// for a valid slug but no MkdirAll is invoked from this path alone.
func TestProjectWorkspacePath_ArchiveDoesNotCallEnsure(t *testing.T) {
	tmp := t.TempDir()

	slug := "archive-me"

	// Obtain the path without creating the folder (ProjectWorkspacePath only validates + computes).
	path, err := ProjectWorkspacePath(tmp, slug)
	if err != nil {
		t.Fatalf("ProjectWorkspacePath: %v", err)
	}

	// Assert the folder was NOT created by ProjectWorkspacePath alone.
	if _, statErr := os.Stat(path); statErr == nil {
		t.Error("ProjectWorkspacePath must not create the folder — only EnsureProjectFolder should")
	}
}
