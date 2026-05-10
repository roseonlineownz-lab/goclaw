package tools

import (
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

func TestResolveWorkspace_EmptyLayers(t *testing.T) {
	got := ResolveWorkspace("/data")
	if got != "/data" {
		t.Errorf("expected /data, got %s", got)
	}
}


func TestResolveWorkspace_SoloAgent(t *testing.T) {
	userID := SanitizePathSegment("user:telegram:12345")
	got := ResolveWorkspace("/ws",
		UserChatLayer(userID, false),
	)
	want := filepath.Join("/ws", "user_telegram_12345")
	if got != want {
		t.Errorf("want %s, got %s", want, got)
	}
}

func TestResolveWorkspace_SoloAgentShared(t *testing.T) {
	got := ResolveWorkspace("/ws",
		UserChatLayer("user123", true),
	)
	if got != "/ws" {
		t.Errorf("shared should be no-op, got %s", got)
	}
}

func TestResolveWorkspace_SoloAgentProject(t *testing.T) {
	projectID := uuid.MustParse("0193d000-0000-7000-8000-000000000004")
	userID := SanitizePathSegment("user:slack:u1")
	got := ResolveWorkspace("/ws",
		ProjectLayer(&projectID),
		UserChatLayer(userID, false),
	)
	want := filepath.Join("/ws", "projects", projectID.String(), "user_slack_u1")
	if got != want {
		t.Errorf("want %s, got %s", want, got)
	}
}

func TestResolveWorkspace_NilProject(t *testing.T) {
	got := ResolveWorkspace("/data",
		ProjectLayer(nil),
	)
	if got != "/data" {
		t.Errorf("nil project should be no-op, got %s", got)
	}
}

func TestResolveWorkspace_NilTeam(t *testing.T) {
	got := ResolveWorkspace("/data",
		TeamLayer(uuid.Nil),
	)
	if got != "/data" {
		t.Errorf("nil team should be no-op, got %s", got)
	}
}

func TestResolveWorkspace_ZeroProject(t *testing.T) {
	nilID := uuid.Nil
	got := ResolveWorkspace("/data",
		ProjectLayer(&nilID),
	)
	if got != "/data" {
		t.Errorf("zero project should be no-op, got %s", got)
	}
}

func TestResolveWorkspace_SharedTrue(t *testing.T) {
	got := ResolveWorkspace("/data",
		UserChatLayer("chat-123", true),
	)
	if got != "/data" {
		t.Errorf("shared=true should skip segment, got %s", got)
	}
}

func TestSanitizePathSegment(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "simple"},
		{"user:telegram:123", "user_telegram_123"},
		{"user@email.com", "user_email_com"},
		{"hello world", "hello_world"},
		{"a-b_c", "a-b_c"},
		{"", ""},
		{"café", "caf_"},
		{"../etc/passwd", "___etc_passwd"},
	}
	for _, tt := range tests {
		got := SanitizePathSegment(tt.input)
		if got != tt.want {
			t.Errorf("SanitizePathSegment(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
