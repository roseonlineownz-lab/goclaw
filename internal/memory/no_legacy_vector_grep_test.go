package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoLegacyVector1536References asserts that no `vector(1536)` literals
// remain in migrations, internal Go source, or cmd source.
//
// This test runs without a database — it is a plain filesystem grep.
// A non-zero match count is a schema regression: some SQL file or Go constant
// was reverted to the old dimension. The correct dimension is halfvec(3072).
//
// Excludes:
//   - this test file itself (self-referential)
//   - *.md documentation files (historical narrative is OK)
//   - vendor/ directory
func TestNoLegacyVector1536References(t *testing.T) {
	// Resolve repo root by walking up from this test file's directory.
	repoRoot := findRepoRoot(t)

	searchDirs := []string{
		filepath.Join(repoRoot, "migrations"),
		filepath.Join(repoRoot, "internal"),
		filepath.Join(repoRoot, "cmd"),
		filepath.Join(repoRoot, "pkg"),
	}

	banned := "vector(1536)"
	var hits []string

	for _, dir := range searchDirs {
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil // skip unreadable dirs
			}
			if d.IsDir() {
				// Skip vendor and generated directories.
				base := d.Name()
				if base == "vendor" || base == ".git" || base == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			ext := filepath.Ext(path)
			if ext != ".go" && ext != ".sql" {
				return nil // only scan Go and SQL files
			}
			// Skip this test file itself.
			if strings.HasSuffix(path, "no_legacy_vector_grep_test.go") {
				return nil
			}

			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			lines := strings.Split(string(data), "\n")
			for i, line := range lines {
				if strings.Contains(line, banned) {
					hits = append(hits, filepath.Join(filepath.Base(filepath.Dir(path)), filepath.Base(path))+
						":"+itoa(i+1)+": "+strings.TrimSpace(line))
				}
			}
			return nil
		})
		if err != nil {
			t.Logf("walk error in %s: %v", dir, err)
		}
	}

	if len(hits) > 0 {
		t.Errorf("found %d legacy vector(1536) reference(s) — schema must use halfvec(3072):\n%s",
			len(hits), strings.Join(hits, "\n"))
	}
}

// TestNoLegacyVectorCosineOpsReferences asserts that no `vector_cosine_ops`
// HNSW opclass references remain — all indexes must use `halfvec_cosine_ops`.
func TestNoLegacyVectorCosineOpsReferences(t *testing.T) {
	repoRoot := findRepoRoot(t)

	searchDirs := []string{
		filepath.Join(repoRoot, "migrations"),
	}

	banned := "vector_cosine_ops"
	var hits []string

	for _, dir := range searchDirs {
		filepath.WalkDir(dir, func(path string, d os.DirEntry, _ error) error { //nolint:errcheck
			if d.IsDir() {
				return nil
			}
			if filepath.Ext(path) != ".sql" {
				return nil
			}
			data, _ := os.ReadFile(path)
			lines := strings.Split(string(data), "\n")
			for i, line := range lines {
				if strings.Contains(line, banned) {
					hits = append(hits, filepath.Base(path)+":"+itoa(i+1)+": "+strings.TrimSpace(line))
				}
			}
			return nil
		})
	}

	if len(hits) > 0 {
		t.Errorf("found %d legacy vector_cosine_ops reference(s) — use halfvec_cosine_ops:\n%s",
			len(hits), strings.Join(hits, "\n"))
	}
}

// findRepoRoot walks up from the current working directory until it finds go.mod.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root (no go.mod found)")
		}
		dir = parent
	}
}

// itoa converts an int to its decimal string representation.
// Avoids importing strconv in a test-only file.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	buf := make([]byte, 0, 10)
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	if neg {
		buf = append([]byte{'-'}, buf...)
	}
	return string(buf)
}
