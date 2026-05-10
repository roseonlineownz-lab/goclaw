package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRelocateOnMerge_HappyPath verifies that RelocateOnMerge moves a directory
// and its contents from oldPath to newPath on the same filesystem (atomic rename).
func TestRelocateOnMerge_HappyPath(t *testing.T) {
	base := t.TempDir()

	oldPath := filepath.Join(base, "old-workspace")
	newPath := filepath.Join(base, "new-workspace")

	// Create source tree: dir + file.
	if err := os.MkdirAll(filepath.Join(oldPath, "subdir"), 0o750); err != nil {
		t.Fatalf("setup mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldPath, "file.txt"), []byte("hello"), 0o640); err != nil {
		t.Fatalf("setup write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldPath, "subdir", "nested.txt"), []byte("world"), 0o640); err != nil {
		t.Fatalf("setup write nested: %v", err)
	}

	if err := RelocateOnMerge(oldPath, newPath); err != nil {
		t.Fatalf("RelocateOnMerge: %v", err)
	}

	// Old path must be gone.
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Errorf("oldPath still exists after relocation: %v", err)
	}

	// New path must contain the files.
	data, err := os.ReadFile(filepath.Join(newPath, "file.txt"))
	if err != nil {
		t.Fatalf("read file.txt at newPath: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("file.txt content: got %q want %q", string(data), "hello")
	}

	nested, err := os.ReadFile(filepath.Join(newPath, "subdir", "nested.txt"))
	if err != nil {
		t.Fatalf("read nested.txt at newPath: %v", err)
	}
	if string(nested) != "world" {
		t.Errorf("nested.txt content: got %q want %q", string(nested), "world")
	}
}

// TestRelocateOnMerge_CrossDeviceFallback verifies the copy-then-delete fallback
// by replacing os.Rename with a stub that returns a cross-device error, then
// asserting the file ends up at newPath via the copy path.
//
// Since injecting a real cross-device error requires two separate mounts (not
// available in t.TempDir), this test exercises copyDirRecursive directly and
// checks end-to-end copy semantics. The isCrossDeviceError path is unit-tested
// separately in TestIsCrossDeviceError.
func TestRelocateOnMerge_CrossDeviceFallback_CopySemantics(t *testing.T) {
	base := t.TempDir()

	src := filepath.Join(base, "src-dir")
	dst := filepath.Join(base, "dst-dir")

	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o750); err != nil {
		t.Fatalf("setup src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("aaa"), 0o640); err != nil {
		t.Fatalf("setup a.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "b.txt"), []byte("bbb"), 0o640); err != nil {
		t.Fatalf("setup b.txt: %v", err)
	}

	// Exercise copyDirRecursive directly (the cross-device fallback path).
	if err := copyDirRecursive(src, dst); err != nil {
		t.Fatalf("copyDirRecursive: %v", err)
	}

	// Source must still exist (copy does not delete).
	if _, err := os.Stat(src); err != nil {
		t.Errorf("src removed by copyDirRecursive (it must not): %v", err)
	}

	// Destination must contain copies of both files.
	gotA, err := os.ReadFile(filepath.Join(dst, "a.txt"))
	if err != nil {
		t.Fatalf("read dst/a.txt: %v", err)
	}
	if string(gotA) != "aaa" {
		t.Errorf("a.txt: got %q want %q", string(gotA), "aaa")
	}

	gotB, err := os.ReadFile(filepath.Join(dst, "sub", "b.txt"))
	if err != nil {
		t.Fatalf("read dst/sub/b.txt: %v", err)
	}
	if string(gotB) != "bbb" {
		t.Errorf("b.txt: got %q want %q", string(gotB), "bbb")
	}
}

// TestRelocateOnMerge_SamePath verifies that relocating to the same path is a no-op.
func TestRelocateOnMerge_SamePath(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "workspace")
	if err := os.MkdirAll(path, 0o750); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := RelocateOnMerge(path, path); err != nil {
		t.Errorf("same-path relocation must be a no-op, got: %v", err)
	}
}

// TestRelocateOnMerge_EmptyPaths verifies that empty path arguments return an error.
func TestRelocateOnMerge_EmptyPaths(t *testing.T) {
	if err := RelocateOnMerge("", "/some/path"); err == nil {
		t.Error("expected error for empty oldPath, got nil")
	}
	if err := RelocateOnMerge("/some/path", ""); err == nil {
		t.Error("expected error for empty newPath, got nil")
	}
}
