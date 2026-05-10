package permissions

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// TestMigrateConfigPermissionsForMerge_EmptySource verifies that passing an
// empty sourceUserIDs slice is a no-op: the helper returns nil without executing
// any SQL. This prevents accidental full-table updates when the merge request
// carries no source users.
func TestMigrateConfigPermissionsForMerge_EmptySource(t *testing.T) {
	err := MigrateConfigPermissionsForMerge(context.Background(), nil, nil, uuid.New())
	if err != nil {
		t.Errorf("empty sourceUserIDs: expected nil error, got: %v", err)
	}

	err = MigrateConfigPermissionsForMerge(context.Background(), nil, []uuid.UUID{}, uuid.New())
	if err != nil {
		t.Errorf("empty slice sourceUserIDs: expected nil error, got: %v", err)
	}
}
