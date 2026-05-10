package workspace

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// RelocateOnMerge moves a workspace directory from oldPath to newPath after a
// contact merge commits to the DB. It is always invoked post-commit and must
// be treated as best-effort: the DB pointer is already canonical regardless of
// whether the FS move succeeds.
//
// Strategy:
//  1. Try os.Rename (atomic on same filesystem, ~zero cost).
//  2. On cross-device error (EXDEV / "invalid cross-device link"), fall back to
//     recursive copy then delete the source tree.
//  3. On partial copy failure: leave both copies intact (old path for forensics)
//     and return an error — the caller logs and may emit a retry event.
//
// Callers must NOT fail the merge transaction on error from this function.
// Old files at oldPath remain readable until manual cleanup if relocation fails.
func RelocateOnMerge(oldPath, newPath string) error {
	if oldPath == "" || newPath == "" {
		return fmt.Errorf("relocate: oldPath and newPath must be non-empty")
	}
	if oldPath == newPath {
		return nil
	}

	// Ensure parent directory of newPath exists.
	if err := os.MkdirAll(filepath.Dir(newPath), 0o750); err != nil {
		return fmt.Errorf("relocate: mkdir parent of new path: %w", err)
	}

	// Attempt atomic rename first (works when src and dst are on the same device).
	if err := os.Rename(oldPath, newPath); err == nil {
		return nil
	} else if !isCrossDeviceError(err) {
		return fmt.Errorf("relocate: rename %s → %s: %w", oldPath, newPath, err)
	}

	// Cross-device fallback: recursive copy then remove source.
	if err := copyDirRecursive(oldPath, newPath); err != nil {
		// Leave both copies; return error so caller can log/retry.
		return fmt.Errorf("relocate: cross-device copy %s → %s: %w", oldPath, newPath, err)
	}
	if err := os.RemoveAll(oldPath); err != nil {
		// Copy succeeded; source deletion failed. Non-fatal: old path is now
		// a redundant copy that can be cleaned up manually.
		return fmt.Errorf("relocate: remove source after copy %s: %w", oldPath, err)
	}
	return nil
}

// isCrossDeviceError reports whether err is an EXDEV / cross-device link error.
func isCrossDeviceError(err error) bool {
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) {
		return errors.Is(linkErr.Err, syscall.EXDEV)
	}
	return false
}

// copyDirRecursive copies the directory tree at src to dst.
// dst must not exist prior to the call (MkdirAll on parent already done by caller).
func copyDirRecursive(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)

		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		return copyFile(path, target, info.Mode())
	})
}

// copyFile copies a single regular file from src to dst, preserving mode bits.
func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("copy open src %s: %w", src, err)
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("copy open dst %s: %w", dst, err)
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy data %s → %s: %w", src, dst, err)
	}
	return out.Sync()
}
