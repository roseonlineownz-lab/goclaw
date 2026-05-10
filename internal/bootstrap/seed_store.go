package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/channels"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// ChannelMeta carries channel-provided contact info for bootstrap decisions.
type ChannelMeta struct {
	ChannelType     string
	DisplayName     string
	DefaultTimezone string
}

// shouldSkipBootstrap returns true when channel provides enough user info
// to pre-fill USER.md, making the interactive bootstrap unnecessary.
// Currently only Pancake channel qualifies (provides Facebook profile name).
func shouldSkipBootstrap(meta *ChannelMeta) bool {
	return meta != nil &&
		meta.ChannelType == "pancake" &&
		meta.DisplayName != ""
}

// buildPrefilledUser generates USER.md content pre-filled with channel-provided contact info.
func buildPrefilledUser(meta *ChannelMeta) string {
	tz := meta.DefaultTimezone
	if tz == "" {
		tz = "(unknown)"
	}
	name := channels.SanitizeDisplayName(meta.DisplayName)
	return fmt.Sprintf(`# USER.md - About This User

- **Name:** %s
- **What to call them:** %s
- **Timezone:** %s

## Context

_(First contact via %s channel. Profile info auto-filled from channel data.)_
`, name, name, tz, meta.ChannelType)
}



// retryOnBusy retries fn up to 3 times on SQLITE_BUSY errors with 500ms delay.
func retryOnBusy(fn func() error) error {
	var lastErr error
	for attempt := range 3 {
		lastErr = fn()
		if lastErr == nil {
			return nil
		}
		if !strings.Contains(lastErr.Error(), "SQLITE_BUSY") && !strings.Contains(lastErr.Error(), "database is locked") {
			return lastErr
		}
		if attempt < 2 {
			slog.Warn("bootstrap: retrying after SQLITE_BUSY", "attempt", attempt+1)
			time.Sleep(500 * time.Millisecond)
		}
	}
	return lastErr
}

// SeedToStore seeds embedded templates into agent_context_files (agent-level).
// Only writes files that don't already have content.
// Returns the list of file names that were seeded.
func SeedToStore(ctx context.Context, agentStore store.AgentStore, agentID uuid.UUID) ([]string, error) {
	existing, err := agentStore.GetAgentContextFiles(ctx, agentID)
	if err != nil {
		slog.Warn("bootstrap: failed to check existing agent files", "agent", agentID, "error", err)
		return nil, err
	}

	// Build set of files that already have content
	hasContent := make(map[string]bool)
	for _, f := range existing {
		if f.Content != "" {
			hasContent[f.FileName] = true
		}
	}

	var seeded []string
	for _, name := range templateFiles {
		// USER.md is seeded separately below as the agent-level baseline.
		if name == UserFile {
			continue
		}
		// TOOLS.md: local tool notes (camera, SSH, device names) — not applicable.
		if name == ToolsFile {
			continue
		}
		if hasContent[name] {
			continue
		}

		content, err := templateFS.ReadFile(filepath.Join("templates", name))
		if err != nil {
			slog.Warn("bootstrap: failed to read embedded template", "file", name, "error", err)
			continue
		}

		if err := retryOnBusy(func() error { return agentStore.SetAgentContextFile(ctx, agentID, name, string(content)) }); err != nil {
			return seeded, err
		}
		seeded = append(seeded, name)
	}

	// Seed USER.md for predefined agents (agent-level, not in templateFiles).
	// Provides baseline user-handling rules shared across all users.
	if !hasContent[UserFile] {
		content, err := templateFS.ReadFile(filepath.Join("templates", UserFile))
		if err == nil {
			if err := retryOnBusy(func() error { return agentStore.SetAgentContextFile(ctx, agentID, UserFile, string(content)) }); err != nil {
				return seeded, err
			}
			seeded = append(seeded, UserFile)
		}
	}

	if len(seeded) > 0 {
		slog.Info("seeded agent context files to store", "agent", agentID, "files", seeded)
	}

	return seeded, nil
}

// userSeedFiles is the set of files seeded per-user on first chat:
// USER.md (per-user profile, optionally inherited from agent-level wizard config)
// and BOOTSTRAP.md (one-shot onboarding, deleted after the ritual finishes).
var userSeedFiles = []string{
	UserFile,
	BootstrapFile,
}

// SeedUserFiles seeds embedded templates into user_context_files for a new user.
// Only writes files that don't already exist — safe to call multiple times.
//
// When skipIfAnyExist is true, returns immediately if the user already has ANY
// context files (even with empty content slots). This prevents re-seeding
// BOOTSTRAP.md after auto-cleanup on server restart — existing files indicate
// the user is not brand-new, so BOOTSTRAP.md should stay gone. Use
// skipIfAnyExist=true for existing profiles, false for newly created profiles.
//
// USER.md seeding: if agent_context_files already holds a populated USER.md
// (set by the wizard or management dashboard), that content is used as the
// per-user seed instead of the blank embedded template — wizard-configured
// owner profiles are preserved on first chat.
//
// Returns the list of file names that were seeded.
func SeedUserFiles(ctx context.Context, agentStore store.AgentStore, agentID uuid.UUID, userID string, skipIfAnyExist bool, channelMeta *ChannelMeta) ([]string, error) {
	// Check existing per-user files to avoid overwriting personalized content
	existing, err := agentStore.GetUserContextFiles(ctx, agentID, userID)
	if err != nil {
		slog.Warn("bootstrap: failed to check existing user files", "agent", agentID, "user", userID, "error", err)
		return nil, err
	}

	// Early exit: user already has files → not a brand-new user.
	// Avoids re-seeding BOOTSTRAP.md after auto-cleanup on server restart.
	if skipIfAnyExist && len(existing) > 0 {
		slog.Debug("bootstrap: skip user seed (existing files)", "agent", agentID, "user", userID, "existing", len(existing))
		return nil, nil
	}

	// Channel-provided contact info: skip bootstrap, pre-fill USER.md directly.
	// Currently only Pancake channel (Facebook Messenger) provides enough user info
	// (display_name from Facebook profile) to skip the interactive onboarding flow.
	if shouldSkipBootstrap(channelMeta) {
		userContent := buildPrefilledUser(channelMeta)
		if err := retryOnBusy(func() error {
			return agentStore.SetUserContextFile(ctx, agentID, userID, UserFile, userContent)
		}); err != nil {
			return nil, err
		}
		seeded := []string{UserFile}
		slog.Info("bootstrap skipped (channel contact info)",
			"agent", agentID, "user", userID, "channel", channelMeta.ChannelType,
			"display_name", channelMeta.DisplayName, "files", seeded)
		return seeded, nil
	}

	hasFile := make(map[string]bool, len(existing))
	for _, f := range existing {
		if f.Content != "" {
			hasFile[f.FileName] = true
		}
	}

	// Load agent-level files once to use as seed fallback. USER.md at agent
	// level may contain a pre-configured owner profile (e.g. set by the wizard
	// or management dashboard). Use it as the per-user seed instead of the
	// blank embedded template so the agent starts with the correct owner context.
	var agentLevelFiles map[string]string
	agentFiles, err := agentStore.GetAgentContextFiles(ctx, agentID)
	if err == nil && len(agentFiles) > 0 {
		agentLevelFiles = make(map[string]string, len(agentFiles))
		for _, f := range agentFiles {
			if f.Content != "" {
				agentLevelFiles[f.FileName] = f.Content
			}
		}
	}

	var seeded []string
	for _, name := range userSeedFiles {
		if hasFile[name] {
			continue // already has personalized content, don't overwrite
		}

		// USER.md: prefer agent-level content as seed when present so wizard/dashboard
		// owner profiles propagate to the first user.
		if name == UserFile {
			if agentContent, ok := agentLevelFiles[name]; ok {
				if err := retryOnBusy(func() error { return agentStore.SetUserContextFile(ctx, agentID, userID, name, agentContent) }); err != nil {
					return seeded, err
				}
				seeded = append(seeded, name)
				continue
			}
		}

		content, err := templateFS.ReadFile(filepath.Join("templates", name))
		if err != nil {
			slog.Warn("bootstrap: failed to read embedded template for user seed", "file", name, "error", err)
			continue
		}

		if err := retryOnBusy(func() error { return agentStore.SetUserContextFile(ctx, agentID, userID, name, string(content)) }); err != nil {
			return seeded, err
		}
		seeded = append(seeded, name)
	}

	if len(seeded) > 0 {
		slog.Info("seeded user context files", "agent", agentID, "user", userID, "files", seeded)
	}

	return seeded, nil
}

// EmbeddedUserFiles returns in-memory context files from embedded templates.
// Used as a fallback when DB seeding fails (e.g. SQLITE_BUSY) so the first
// turn still gets bootstrap onboarding without waiting for DB recovery.
func EmbeddedUserFiles() []ContextFile {
	var result []ContextFile
	for _, name := range userSeedFiles {
		content, err := templateFS.ReadFile(filepath.Join("templates", name))
		if err != nil {
			continue
		}
		result = append(result, ContextFile{Path: name, Content: string(content)})
	}
	return result
}
