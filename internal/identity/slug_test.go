package identity

import (
	"strings"
	"testing"
)

// Tests drive the slug generation rules. Slugs are deterministic:
// same (email, idHex) always produces the same output. Collision resolution
// (appending the id suffix) lives at the store layer, not here.

func TestSlugFromEmail(t *testing.T) {
	cases := []struct {
		name  string
		email string
		idHex string
		want  string
	}{
		{
			name:  "plain local part",
			email: "alice@example.com",
			idHex: "abcdef",
			want:  "alice",
		},
		{
			name:  "strip plus suffix",
			email: "alice+work@example.com",
			idHex: "abcdef",
			want:  "alice",
		},
		{
			name:  "dots become dashes",
			email: "a.b.c@x.io",
			idHex: "abcdef",
			want:  "a-b-c",
		},
		{
			name:  "fallback when sanitised under 3 chars",
			email: "@@@",
			idHex: "abcdef",
			want:  "u-abcdef",
		},
		{
			name:  "long input truncated to 50 chars",
			email: strings.Repeat("x", 80) + "@example.com",
			idHex: "abcdef",
			want:  strings.Repeat("x", 50),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SlugFromEmail(tc.email, tc.idHex)
			if got != tc.want {
				t.Errorf("SlugFromEmail(%q, %q) = %q; want %q", tc.email, tc.idHex, got, tc.want)
			}
		})
	}
}

func TestSlugFromName(t *testing.T) {
	cases := []struct {
		name  string
		input string
		idHex string
		want  string
	}{
		{
			name:  "team name with special chars",
			input: "Acme Team #1",
			idHex: "abcdef",
			want:  "acme-team-1",
		},
		{
			name:  "short name fallback",
			input: "AB",
			idHex: "abcdef",
			want:  "t-abcdef",
		},
		{
			name:  "long name truncated",
			input: strings.Repeat("z", 80),
			idHex: "abcdef",
			want:  strings.Repeat("z", 50),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SlugFromName(tc.input, tc.idHex)
			if got != tc.want {
				t.Errorf("SlugFromName(%q, %q) = %q; want %q", tc.input, tc.idHex, got, tc.want)
			}
		})
	}
}

func TestSlugDeterministic(t *testing.T) {
	// Same inputs always produce same output — essential for workspace folder stability.
	email, idHex := "user@example.com", "ff00aa"
	first := SlugFromEmail(email, idHex)
	for i := range 10 {
		if got := SlugFromEmail(email, idHex); got != first {
			t.Errorf("non-deterministic: iteration %d got %q, want %q", i, got, first)
		}
	}
}
