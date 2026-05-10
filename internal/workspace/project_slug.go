package workspace

import (
	"errors"
	"regexp"
	"strings"
)

// slugRe matches valid project slugs: starts and ends with lowercase alphanum,
// hyphens allowed in the middle, total length 3–100 chars.
// Pattern: ^[a-z0-9][a-z0-9-]{1,98}[a-z0-9]$
var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,98}[a-z0-9]$`)

// ValidateProjectSlug checks whether s is a valid project slug.
// Accepted pattern: ^[a-z0-9][a-z0-9-]{1,98}[a-z0-9]$
// — starts and ends with lowercase alphanum, 3–100 chars total, hyphens allowed inside.
// FS-safe: no uppercase, no underscores, no spaces, no path separators.
func ValidateProjectSlug(s string) error {
	if s == "" {
		return errors.New("slug must not be empty")
	}
	// Defense in depth: reject path traversal component before regex.
	if strings.Contains(s, "..") {
		return errors.New("slug must not contain '..'")
	}
	if strings.ContainsAny(s, "/\\") {
		return errors.New("slug must not contain path separators")
	}
	if !slugRe.MatchString(s) {
		return errors.New("slug must be 3–100 chars, lowercase alphanumeric with hyphens, no leading or trailing hyphens")
	}
	return nil
}
