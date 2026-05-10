// Package identity provides deterministic slug generators for user and team
// workspace identifiers. Slugs are immutable once created — collision
// resolution (UNIQUE constraint violation → append id suffix) is the
// responsibility of the store layer, not this package.
package identity

import (
	"strings"
	"unicode"
)

// SlugFromEmail derives a stable slug from an email address and a hex ID
// string (first 6 chars of the row's UUID hex). The slug is based on the
// local-part before '@'; any '+suffix' is stripped first.
//
// Rules (applied in order):
//  1. Take local-part (before '@'); strip '+...' suffix.
//  2. Lowercase; replace every non-alphanumeric rune with '-'.
//  3. Collapse consecutive '-' runs to a single '-'.
//  4. Trim leading and trailing '-'.
//  5. Truncate to 50 characters.
//  6. If result is shorter than 3 characters, return "u-" + idHex[:6].
func SlugFromEmail(email, idHex string) string {
	local := email
	if before, _, ok := strings.Cut(email, "@"); ok {
		local = before
	}
	// Strip plus-addressing suffix (e.g. alice+work → alice).
	if idx := strings.IndexByte(local, '+'); idx >= 0 {
		local = local[:idx]
	}
	slug := sanitise(local)
	if len(slug) < 3 {
		return "u-" + safeHex(idHex, 6)
	}
	return slug
}

// SlugFromName derives a stable slug from a team (or generic entity) name
// and a hex ID string. Follows the same sanitise pipeline as SlugFromEmail;
// the fallback prefix is "t-" instead of "u-".
func SlugFromName(name, idHex string) string {
	slug := sanitise(name)
	if len(slug) < 3 {
		return "t-" + safeHex(idHex, 6)
	}
	return slug
}

// sanitise applies the common slug transformation pipeline:
// lowercase → replace non-alnum with '-' → collapse '-' runs → trim → truncate 50.
func sanitise(s string) string {
	s = strings.ToLower(s)

	// Replace every non-alphanumeric ASCII rune with '-'.
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	s = b.String()

	// Collapse consecutive '-' runs.
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}

	// Trim leading/trailing '-'.
	s = strings.Trim(s, "-")

	// Truncate to 50 characters.
	if len(s) > 50 {
		s = s[:50]
		// Re-trim in case truncation landed on a '-'.
		s = strings.TrimRight(s, "-")
	}

	return s
}

// safeHex returns up to n bytes of hex, or the full string if shorter.
func safeHex(hex string, n int) string {
	if len(hex) <= n {
		return hex
	}
	return hex[:n]
}
