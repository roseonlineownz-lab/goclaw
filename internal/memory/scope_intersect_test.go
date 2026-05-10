package memory

import (
	"testing"
)

func TestIntersectScopes(t *testing.T) {
	const (
		ag1 = "agent-1"
		u1  = "user-1"
		u2  = "user-2"
		t1  = "team-1"
		c1  = "contact-1"
		p1  = "project-1"
	)

	cases := []struct {
		name   string
		input  []ScopeKey
		want   ScopeKey
	}{
		{
			name:  "empty input returns zero ScopeKey",
			input: nil,
			want:  ScopeKey{},
		},
		{
			name:  "single source returns itself",
			input: []ScopeKey{{AgentID: ag1, UserID: u1, TeamID: t1, ContactID: c1, ProjectID: p1}},
			want:  ScopeKey{AgentID: ag1, UserID: u1, TeamID: t1, ContactID: c1, ProjectID: p1},
		},
		{
			name: "all dimensions identical → kept",
			input: []ScopeKey{
				{AgentID: ag1, UserID: u1, TeamID: t1, ContactID: c1, ProjectID: p1},
				{AgentID: ag1, UserID: u1, TeamID: t1, ContactID: c1, ProjectID: p1},
				{AgentID: ag1, UserID: u1, TeamID: t1, ContactID: c1, ProjectID: p1},
			},
			want: ScopeKey{AgentID: ag1, UserID: u1, TeamID: t1, ContactID: c1, ProjectID: p1},
		},
		{
			name: "one user mismatch → UserID cleared",
			input: []ScopeKey{
				{AgentID: ag1, UserID: u1},
				{AgentID: ag1, UserID: u2},
			},
			want: ScopeKey{AgentID: ag1, UserID: ""},
		},
		{
			name: "one dimension empty vs non-empty → cleared",
			input: []ScopeKey{
				{AgentID: ag1, UserID: u1, TeamID: t1},
				{AgentID: ag1, UserID: u1, TeamID: ""},
			},
			want: ScopeKey{AgentID: ag1, UserID: u1, TeamID: ""},
		},
		{
			name: "all dimensions mismatch → agent-broad",
			input: []ScopeKey{
				{AgentID: ag1, UserID: u1, TeamID: t1, ContactID: c1, ProjectID: p1},
				{AgentID: ag1, UserID: u2, TeamID: "", ContactID: "", ProjectID: ""},
			},
			want: ScopeKey{AgentID: ag1},
		},
		{
			name: "L31: user-private chunks consolidate to user-private summary",
			input: []ScopeKey{
				{AgentID: ag1, UserID: u1},
				{AgentID: ag1, UserID: u1},
				{AgentID: ag1, UserID: u1},
			},
			want: ScopeKey{AgentID: ag1, UserID: u1},
		},
		{
			name: "project matches across all → kept",
			input: []ScopeKey{
				{AgentID: ag1, ProjectID: p1},
				{AgentID: ag1, ProjectID: p1},
			},
			want: ScopeKey{AgentID: ag1, ProjectID: p1},
		},
		{
			name: "contact mismatch clears contact only",
			input: []ScopeKey{
				{AgentID: ag1, UserID: u1, ContactID: c1, ProjectID: p1},
				{AgentID: ag1, UserID: u1, ContactID: "", ProjectID: p1},
			},
			want: ScopeKey{AgentID: ag1, UserID: u1, ContactID: "", ProjectID: p1},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IntersectScopes(tc.input)
			if got != tc.want {
				t.Errorf("IntersectScopes(%v)\n got  %+v\n want %+v", tc.input, got, tc.want)
			}
		})
	}
}
