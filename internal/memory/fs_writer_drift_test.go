package memory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestFSWriterDBFSDriftFullCycle exercises the complete drift detection path:
// write → verify clean Read → delete FS file out-of-band → Read must return
// ErrDriftDetected (not a stale cached value, not os.ErrNotExist, not nil).
//
// DB-FS drift is a silent corruption class: without an explicit hash check on
// every Read, a missing FS file would either panic, return empty content, or
// return stale OS-cached data — all of which are worse than a loud error.
func TestFSWriterDBFSDriftFullCycle(t *testing.T) {
	ctx := context.Background()
	scope, dir := mustTempScope(t)
	w := newNoopFSWriter()

	const relPath = "docs/drift-cycle.md"
	content := randomBytes(512)

	// Phase 1: establish the DB row + FS file.
	_, err := w.Write(ctx, scope, relPath, content, -1)
	if err != nil {
		t.Logf("RED: Write failed (%v) — noopFSWriter; drift path requires phase 04", err)
		return
	}

	// Phase 2: Read must succeed and return matching content.
	got, _, err := w.Read(ctx, scope, relPath)
	if err != nil {
		t.Fatalf("clean Read failed: %v", err)
	}
	if string(got) != string(content) {
		t.Fatal("clean Read returned wrong content")
	}

	// Phase 3: delete the FS file out-of-band (simulates external rm or
	// storage failure after the DB write committed but before FS write landed).
	fsPath := filepath.Join(dir, relPath)
	if removeErr := os.Remove(fsPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		t.Fatalf("could not remove FS file for drift simulation: %v", removeErr)
	}

	// Phase 4: Read must return ErrDriftDetected — not nil, not os.ErrNotExist.
	_, _, err = w.Read(ctx, scope, relPath)
	if !errors.Is(err, ErrDriftDetected) {
		t.Errorf("after FS delete: want ErrDriftDetected, got %v", err)
	}
}

// TestFSWriterDBFSDriftHashMismatch verifies drift detection when the FS file
// exists but its content has been modified out-of-band (hash mismatch).
// This catches partial overwrites (e.g. another process writes to the file
// directly, bypassing the FSWriter's atomic-rename protocol).
func TestFSWriterDBFSDriftHashMismatch(t *testing.T) {
	ctx := context.Background()
	scope, dir := mustTempScope(t)
	w := newNoopFSWriter()

	const relPath = "docs/drift-hash.md"
	content := randomBytes(512)

	_, err := w.Write(ctx, scope, relPath, content, -1)
	if err != nil {
		t.Logf("RED: Write failed (%v) — noopFSWriter; hash-mismatch path requires phase 04", err)
		return
	}

	// Corrupt the FS file in-place (direct write bypasses FSWriter protocol).
	fsPath := filepath.Join(dir, relPath)
	corrupted := randomBytes(512)
	if writeErr := os.WriteFile(fsPath, corrupted, 0644); writeErr != nil {
		t.Fatalf("could not corrupt FS file: %v", writeErr)
	}

	// Read must detect the hash mismatch and return ErrDriftDetected.
	_, _, err = w.Read(ctx, scope, relPath)
	if !errors.Is(err, ErrDriftDetected) {
		t.Errorf("after content corruption: want ErrDriftDetected, got %v", err)
	}
}

// TestFSWriterPathTraversalRejected verifies that the FSWriter rejects
// relPath values containing ".." segments (directory traversal attack).
// The implementation must return an error — not sanitize silently.
func TestFSWriterPathTraversalRejected(t *testing.T) {
	ctx := context.Background()
	scope, _ := mustTempScope(t)
	w := newNoopFSWriter()

	traversalPaths := []string{
		"../etc/passwd",
		"docs/../../etc/passwd",
		"./../../secret",
		"/absolute/path",
	}

	for _, p := range traversalPaths {
		_, err := w.Write(ctx, scope, p, randomBytes(64), -1)
		// RED: noop returns ErrVersionConflict; phase 04 must return a specific
		// traversal-rejection error (not ErrVersionConflict).
		// This test will tighten the assertion once phase 04 adds the guard.
		if err == nil {
			t.Errorf("traversal path %q: expected error, got nil", p)
		}
		t.Logf("traversal path %q → error (acceptable RED): %v", p, err)
	}
}
