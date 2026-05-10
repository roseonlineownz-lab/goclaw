package workspace

import (
	"strings"
	"testing"
)

func TestValidateProjectSlug(t *testing.T) {
	// 100-char valid slug: starts/ends with alphanum, hyphens in middle
	valid100 := "a" + strings.Repeat("b", 98) + "c"
	// 101-char slug: one char too long
	invalid101 := "a" + strings.Repeat("b", 99) + "c"

	cases := []struct {
		name    string
		slug    string
		wantErr bool
	}{
		// ── VALID ──────────────────────────────────────────────────────────
		{name: "simple_hyphenated", slug: "my-project", wantErr: false},
		{name: "trailing_digit", slug: "proj-1", wantErr: false},
		{name: "minimal_three_chars", slug: "a1b", wantErr: false},
		{name: "exactly_100_chars", slug: valid100, wantErr: false},

		// ── INVALID ────────────────────────────────────────────────────────
		{name: "empty_string", slug: "", wantErr: true},
		{name: "leading_hyphen", slug: "-foo", wantErr: true},
		{name: "trailing_hyphen", slug: "foo-", wantErr: true},
		{name: "uppercase_letter", slug: "Foo", wantErr: true},
		{name: "underscore", slug: "foo_bar", wantErr: true},
		{name: "space", slug: "foo bar", wantErr: true},
		{name: "101_chars_too_long", slug: invalid101, wantErr: true},
		{name: "double_hyphen_only", slug: "--", wantErr: true},
		{name: "dot_dot", slug: "..", wantErr: true},
		{name: "path_separator", slug: "/abc", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateProjectSlug(tc.slug)
			if tc.wantErr && err == nil {
				t.Errorf("slug %q: expected error, got nil", tc.slug)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("slug %q: expected nil, got %v", tc.slug, err)
			}
		})
	}
}
