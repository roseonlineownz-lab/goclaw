package http

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/workspace"
)

// TestContactMergeRelocate_PathSanitization is a regression guard for G1.
// The previous implementation built the relocation paths from raw DB strings
// (channel_type, sender_id, user_key). A malicious value like "../escape"
// would break out of baseDir on os.Rename. After the fix, every segment goes
// through workspace.SanitizeSegment which restricts to [a-zA-Z0-9_-]; the
// resulting paths must never contain ".." or "/".
func TestContactMergeRelocate_PathSanitization(t *testing.T) {
	cases := []struct {
		name        string
		channelType string
		senderID    string
		userKey     string
	}{
		{"basic", "telegram", "12345", "alice"},
		{"channel_traversal", "../etc/passwd", "12345", "alice"},
		{"sender_traversal", "telegram", "../../../etc/passwd", "alice"},
		{"user_traversal", "telegram", "12345", "../../etc/passwd"},
		{"sender_with_slash", "telegram", "evil/inner", "alice"},
		{"user_with_slash", "telegram", "12345", "evil\\path"},
		{"unicode_segment", "telegram", "12345", "alice@special"},
	}
	baseDir := "/var/data/workspace"
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			groupSeg := workspace.SanitizeSegment(tc.channelType) + "-" + workspace.SanitizeSegment(tc.senderID)
			userSeg := workspace.SanitizeSegment(tc.userKey)
			newPath := filepath.Join(baseDir, "users", userSeg, "groups", groupSeg)

			// Sanitized output must never contain ".." or path separators
			// inside the segment portions; filepath.Join keeps "/" between
			// segments which is fine. Test segment-internal hygiene.
			if strings.Contains(groupSeg, "..") || strings.Contains(groupSeg, "/") || strings.Contains(groupSeg, "\\") {
				t.Errorf("groupSeg leaks unsafe chars: %q", groupSeg)
			}
			if strings.Contains(userSeg, "..") || strings.Contains(userSeg, "/") || strings.Contains(userSeg, "\\") {
				t.Errorf("userSeg leaks unsafe chars: %q", userSeg)
			}

			// Final path must stay under baseDir (defense in depth).
			cleanedNew := filepath.Clean(newPath)
			if !strings.HasPrefix(cleanedNew, filepath.Clean(baseDir)+string(filepath.Separator)) {
				t.Errorf("path escapes baseDir: %q", cleanedNew)
			}
		})
	}
}

// TestTeamAttachments_ChatIDSanitization is a regression guard for G6.
// chat_id is a raw channel-platform string. Without sanitization a value
// like "../../../etc" would compose into the final filepath.Join and only
// be caught by the wsRoot prefix check (which itself is a defense-in-depth
// fallback, not the first line). After the fix, the segment is sanitized
// before join.
func TestTeamAttachments_ChatIDSanitization(t *testing.T) {
	cases := []struct {
		name   string
		chatID string
	}{
		{"basic", "chat-1234"},
		{"chat_traversal", "../../../etc/passwd"},
		{"slash_inject", "evil/path"},
		{"backslash_inject", "evil\\path"},
		{"unicode", "chat:special-#1"},
	}
	baseDir := "/var/data/workspace"
	teamSeg := "00000000-0000-0000-0000-000000000001" // UUID, already safe
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seg := workspace.SanitizeSegment(tc.chatID)
			joined := filepath.Clean(filepath.Join(baseDir, "teams", teamSeg, seg, "attachment.bin"))
			if strings.Contains(seg, "..") || strings.Contains(seg, "/") || strings.Contains(seg, "\\") {
				t.Errorf("chat_id segment leaks unsafe chars: %q", seg)
			}
			if !strings.HasPrefix(joined, filepath.Clean(baseDir)+string(filepath.Separator)) {
				t.Errorf("path escapes baseDir: %q", joined)
			}
		})
	}
}
