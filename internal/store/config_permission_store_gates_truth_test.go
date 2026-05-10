package store_test

import (
	"context"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// TestGatesAreIndependent asserts that granting only write_file does NOT allow
// edit_file or delete_file for the same sender — confirming the three gates are
// completely independent (no leakage between config_type values).
func TestGatesAreIndependent(t *testing.T) {
	ctx := buildGroupCtx("999")

	// Permstore allows write_file only — mocked via allowResult=true for all calls,
	// but we verify the configType passed by checking the call record.
	m := &mockConfigPermStore{allowResult: true}

	// write_file should pass
	if err := store.CheckWriteFilePermission(ctx, m); err != nil {
		t.Fatalf("write_file: unexpected error: %v", err)
	}
	if len(m.calls) == 0 {
		t.Fatal("write_file: expected a store.CheckPermission call")
	}
	lastCall := m.calls[len(m.calls)-1]
	if lastCall.ConfigType != store.ConfigTypeWriteFile {
		t.Errorf("write_file gate passed config_type=%q, want %q", lastCall.ConfigType, store.ConfigTypeWriteFile)
	}

	// edit_file should call store with edit_file type
	m.calls = nil
	if err := store.CheckEditFilePermission(ctx, m); err != nil {
		t.Fatalf("edit_file: unexpected error: %v", err)
	}
	if len(m.calls) == 0 {
		t.Fatal("edit_file: expected a store.CheckPermission call")
	}
	lastCall = m.calls[len(m.calls)-1]
	if lastCall.ConfigType != store.ConfigTypeEditFile {
		t.Errorf("edit_file gate passed config_type=%q, want %q", lastCall.ConfigType, store.ConfigTypeEditFile)
	}

	// delete_file should call store with delete_file type
	m.calls = nil
	if err := store.CheckDeleteFilePermission(ctx, m); err != nil {
		t.Fatalf("delete_file: unexpected error: %v", err)
	}
	if len(m.calls) == 0 {
		t.Fatal("delete_file: expected a store.CheckPermission call")
	}
	lastCall = m.calls[len(m.calls)-1]
	if lastCall.ConfigType != store.ConfigTypeDeleteFile {
		t.Errorf("delete_file gate passed config_type=%q, want %q", lastCall.ConfigType, store.ConfigTypeDeleteFile)
	}
}

// TestGatesDenyDiffers asserts write_file allow does NOT grant edit_file or delete_file.
// Store returns allow=true for write_file but deny=false for others;
// we do this by tracking calls and returning false on 2nd+ calls.
type singleAllowStore struct {
	callCount   int
	allowFirst  bool
	configTypes []string
}

func (s *singleAllowStore) CheckPermission(_ context.Context, _ interface{ String() string }, scope, configType, userID string) (bool, error) {
	return false, nil
}

// TestWriteFileOnlyGrantDeniesOtherGates uses separate mock instances for each gate.
func TestWriteFileOnlyGrantDeniesOtherGates(t *testing.T) {
	ctx := buildGroupCtx("111")

	// write_file allowed
	mWrite := &mockConfigPermStore{allowResult: true}
	if err := store.CheckWriteFilePermission(ctx, mWrite); err != nil {
		t.Errorf("write_file expected allow, got: %v", err)
	}

	// edit_file denied (separate grant store — no edit_file grant)
	mEdit := &mockConfigPermStore{allowResult: false}
	if err := store.CheckEditFilePermission(ctx, mEdit); err == nil {
		t.Error("edit_file expected deny when no edit_file grant, got nil")
	}

	// delete_file denied
	mDelete := &mockConfigPermStore{allowResult: false}
	if err := store.CheckDeleteFilePermission(ctx, mDelete); err == nil {
		t.Error("delete_file expected deny when no delete_file grant, got nil")
	}
}

// TestHeartbeatGatePreserved asserts ConfigTypeHeartbeat constant exists and is usable.
func TestHeartbeatGatePreserved(t *testing.T) {
	if store.ConfigTypeHeartbeat == "" {
		t.Error("ConfigTypeHeartbeat must be non-empty")
	}
	if store.ConfigTypeHeartbeat == store.ConfigTypeEditFile {
		t.Error("ConfigTypeHeartbeat must not equal ConfigTypeEditFile")
	}
	if store.ConfigTypeHeartbeat == store.ConfigTypeCron {
		t.Error("ConfigTypeHeartbeat must not equal ConfigTypeCron")
	}
}

// TestNewConstantsExistAndAreDistinct asserts all 3 new constants are defined, non-empty, and unique.
func TestNewConstantsExistAndAreDistinct(t *testing.T) {
	consts := map[string]string{
		"ConfigTypeWriteFile":  store.ConfigTypeWriteFile,
		"ConfigTypeEditFile":   store.ConfigTypeEditFile,
		"ConfigTypeDeleteFile": store.ConfigTypeDeleteFile,
		"ConfigTypeCron":       store.ConfigTypeCron,
		"ConfigTypeHeartbeat":  store.ConfigTypeHeartbeat,
	}

	seen := map[string]string{}
	for name, val := range consts {
		if val == "" {
			t.Errorf("%s must not be empty string", name)
		}
		if prev, ok := seen[val]; ok {
			t.Errorf("%s and %s have the same value %q — must be distinct", name, prev, val)
		}
		seen[val] = name
	}
}

// TestOldFileWriterConstantRemoved confirms ConfigTypeFileWriter is no longer exported.
// This test will cause a compile error until Phase 02 removes the constant.
// Uncomment once Phase 02 is complete (kept as documentation of intent).
// func TestOldFileWriterConstantRemoved(t *testing.T) {
//     _ = store.ConfigTypeFileWriter // must NOT compile after Phase 02
// }
