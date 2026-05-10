package store_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// mockConfigPermStore records calls and returns programmable allow/deny.
type mockConfigPermStore struct {
	// calls records (scope, configType, userID) tuples from CheckPermission.
	calls []mockPermCall
	// allowResult controls what CheckPermission returns for the next call.
	allowResult bool
}

type mockPermCall struct {
	AgentID    uuid.UUID
	Scope      string
	ConfigType string
	UserID     string
}

func (m *mockConfigPermStore) CheckPermission(_ context.Context, agentID uuid.UUID, scope, configType, userID string) (bool, error) {
	m.calls = append(m.calls, mockPermCall{AgentID: agentID, Scope: scope, ConfigType: configType, UserID: userID})
	return m.allowResult, nil
}

func (m *mockConfigPermStore) Grant(_ context.Context, _ *store.ConfigPermission) error { return nil }
func (m *mockConfigPermStore) Revoke(_ context.Context, _ uuid.UUID, _, _, _ string) error {
	return nil
}
func (m *mockConfigPermStore) List(_ context.Context, _ uuid.UUID, _, _ string) ([]store.ConfigPermission, error) {
	return nil, nil
}
func (m *mockConfigPermStore) ListWriters(_ context.Context, _ uuid.UUID, _ string, _ string) ([]store.ConfigPermission, error) {
	return nil, nil
}
func (m *mockConfigPermStore) GetDenyGlobs(_ context.Context, _ uuid.UUID, _, _ string) ([]string, error) {
	return nil, nil
}

// buildGroupCtx builds a context that looks like a group context with the given senderID.
func buildGroupCtx(senderID string) context.Context {
	ctx := context.Background()
	ctx = store.WithUserID(ctx, "group:telegram:-100456")
	ctx = store.WithAgentID(ctx, uuid.New())
	if senderID != "" {
		ctx = store.WithSenderID(ctx, senderID)
	}
	return ctx
}

func buildAdminGroupCtx(senderID string) context.Context {
	ctx := buildGroupCtx(senderID)
	ctx = store.WithRole(ctx, "admin")
	return ctx
}

func buildDMCtx(senderID string) context.Context {
	ctx := context.Background()
	ctx = store.WithUserID(ctx, "dm:telegram:12345")
	ctx = store.WithAgentID(ctx, uuid.New())
	if senderID != "" {
		ctx = store.WithSenderID(ctx, senderID)
	}
	return ctx
}

// TestCheckWriteFilePermission_GroupContexts exercises the write_file gate.
func TestCheckWriteFilePermission_GroupContexts(t *testing.T) {
	tests := []struct {
		name        string
		ctx         context.Context
		allowResult bool
		wantErr     bool
	}{
		{
			name:        "admin role bypass",
			ctx:         buildAdminGroupCtx("99999"),
			allowResult: false, // store returns deny — should be bypassed
			wantErr:     false,
		},
		{
			name:    "empty sender denied",
			ctx:     buildGroupCtx(""),
			wantErr: true,
		},
		{
			name:    "synthetic sender subagent denied",
			ctx:     buildGroupCtx("subagent:abc"),
			wantErr: true,
		},
		{
			name:    "synthetic sender system denied",
			ctx:     buildGroupCtx("system:init"),
			wantErr: true,
		},
		{
			name:        "real sender group allowed",
			ctx:         buildGroupCtx("123456"),
			allowResult: true,
			wantErr:     false,
		},
		{
			name:        "real sender group denied (no grant)",
			ctx:         buildGroupCtx("123456"),
			allowResult: false,
			wantErr:     true,
		},
		{
			name:    "DM context always allowed",
			ctx:     buildDMCtx("123456"),
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := &mockConfigPermStore{allowResult: tc.allowResult}
			err := store.CheckWriteFilePermission(tc.ctx, m)
			if tc.wantErr && err == nil {
				t.Errorf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// TestCheckEditFilePermission_GroupContexts exercises the edit_file gate.
func TestCheckEditFilePermission_GroupContexts(t *testing.T) {
	tests := []struct {
		name        string
		ctx         context.Context
		allowResult bool
		wantErr     bool
	}{
		{
			name:        "admin role bypass",
			ctx:         buildAdminGroupCtx("99999"),
			allowResult: false,
			wantErr:     false,
		},
		{
			name:    "empty sender denied",
			ctx:     buildGroupCtx(""),
			wantErr: true,
		},
		{
			name:    "synthetic sender notification denied",
			ctx:     buildGroupCtx("notification:x"),
			wantErr: true,
		},
		{
			name:        "real sender group allowed",
			ctx:         buildGroupCtx("777"),
			allowResult: true,
			wantErr:     false,
		},
		{
			name:        "real sender group denied",
			ctx:         buildGroupCtx("777"),
			allowResult: false,
			wantErr:     true,
		},
		{
			name:    "DM context always allowed",
			ctx:     buildDMCtx("888"),
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := &mockConfigPermStore{allowResult: tc.allowResult}
			err := store.CheckEditFilePermission(tc.ctx, m)
			if tc.wantErr && err == nil {
				t.Errorf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// TestCheckDeleteFilePermission_GroupContexts exercises the delete_file gate.
func TestCheckDeleteFilePermission_GroupContexts(t *testing.T) {
	tests := []struct {
		name        string
		ctx         context.Context
		allowResult bool
		wantErr     bool
	}{
		{
			name:        "admin bypass",
			ctx:         buildAdminGroupCtx("55"),
			allowResult: false,
			wantErr:     false,
		},
		{
			name:    "empty sender denied",
			ctx:     buildGroupCtx(""),
			wantErr: true,
		},
		{
			name:    "ticker sender denied",
			ctx:     buildGroupCtx("ticker:heartbeat"),
			wantErr: true,
		},
		{
			name:        "real sender allowed",
			ctx:         buildGroupCtx("222"),
			allowResult: true,
			wantErr:     false,
		},
		{
			name:        "real sender denied",
			ctx:         buildGroupCtx("222"),
			allowResult: false,
			wantErr:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := &mockConfigPermStore{allowResult: tc.allowResult}
			err := store.CheckDeleteFilePermission(tc.ctx, m)
			if tc.wantErr && err == nil {
				t.Errorf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// TestCheckCronPermission_GroupContexts exercises the cron gate.
func TestCheckCronPermission_GroupContexts(t *testing.T) {
	tests := []struct {
		name        string
		ctx         context.Context
		allowResult bool
		wantErr     bool
	}{
		{
			name:        "admin bypass",
			ctx:         buildAdminGroupCtx("55"),
			allowResult: false,
			wantErr:     false,
		},
		{
			name:    "synthetic sender denied",
			ctx:     buildGroupCtx("session_send_tool"),
			wantErr: true,
		},
		{
			name:        "real sender with cron grant allowed",
			ctx:         buildGroupCtx("333"),
			allowResult: true,
			wantErr:     false,
		},
		{
			name:        "real sender no cron grant denied",
			ctx:         buildGroupCtx("333"),
			allowResult: false,
			wantErr:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := &mockConfigPermStore{allowResult: tc.allowResult}
			err := store.CheckCronPermission(tc.ctx, m)
			if tc.wantErr && err == nil {
				t.Errorf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}
