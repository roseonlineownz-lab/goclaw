package permissions

import "testing"

// HighestShareRole truth-table — pure-Go precedence helper.
func TestHighestShareRole(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want string
	}{
		{"empty", nil, ShareNone},
		{"none only", []string{ShareNone}, ShareNone},
		{"single viewer", []string{ShareViewer}, ShareViewer},
		{"viewer + member → member", []string{ShareViewer, ShareMember}, ShareMember},
		{"member + editor → editor", []string{ShareMember, ShareEditor}, ShareEditor},
		{"editor + owner → owner", []string{ShareEditor, ShareOwner}, ShareOwner},
		{"owner overrides all", []string{ShareViewer, ShareOwner, ShareMember}, ShareOwner},
		{"unknown ignored", []string{"weird", ShareViewer}, ShareViewer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HighestShareRole(tc.in...); got != tc.want {
				t.Errorf("HighestShareRole(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// shareFromPrecedence is the inverse of sharePrecedence.
func TestShareFromPrecedence(t *testing.T) {
	for role, p := range sharePrecedence {
		if got := shareFromPrecedence(p); got != role {
			t.Errorf("shareFromPrecedence(%d) = %q, want %q", p, got, role)
		}
	}
}
