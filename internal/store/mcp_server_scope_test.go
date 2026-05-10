package store

import (
	"testing"

	"github.com/google/uuid"
)

// TestMCPServerData_Scope verifies the Scope() helper returns the correct
// string for all three valid states of the team_id / project_id mutex.
func TestMCPServerData_Scope(t *testing.T) {
	teamID := uuid.New()
	projectID := uuid.New()

	cases := []struct {
		name      string
		teamID    *uuid.UUID
		projectID *uuid.UUID
		want      string
	}{
		{"global — both nil", nil, nil, "global"},
		{"team-scoped", &teamID, nil, "team"},
		{"project-scoped", nil, &projectID, "project"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := MCPServerData{TeamID: tc.teamID, ProjectID: tc.projectID}
			if got := srv.Scope(); got != tc.want {
				t.Errorf("Scope() = %q, want %q", got, tc.want)
			}
		})
	}
}
