package tools

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/bootstrap"
	"github.com/nextlevelbuilder/goclaw/internal/cache"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// protectedFileSet defines files that require group file writer permission in group chats.
// These files control the agent's identity and behavior — only allowlisted users can modify them.
var protectedFileSet = map[string]bool{
	bootstrap.SoulFile:         true,
	bootstrap.IdentityFile:     true,
	bootstrap.AgentsFile:       true,
	bootstrap.UserFile:         true,
	bootstrap.CapabilitiesFile: true,
}

// contextFileSet is the set of filenames routed to the DB store.
// TOOLS.md excluded — not applicable.
var contextFileSet = map[string]bool{
	bootstrap.SoulFile:         true,
	bootstrap.AgentsFile:       true,
	bootstrap.IdentityFile:     true,
	bootstrap.UserFile:         true,
	bootstrap.BootstrapFile:    true, // first-run file (deleted after completion)
	bootstrap.HeartbeatFile:    true, // agent-level heartbeat checklist
	bootstrap.CapabilitiesFile: true, // domain expertise (evolvable when self_evolve=true)
}

// isContextFile checks if a path refers to a workspace-root context file.
// Handles both relative ("SOUL.md") and absolute ("/workspace/SOUL.md") paths.
// Also matches absolute paths under per-user workspace subdirectories
// (e.g. "/workspace/<userID>/USER.md") since context files at any depth
// under the workspace root should be routed to DB.
func isContextFile(path, workspace string) (fileName string, ok bool) {
	base := filepath.Base(path)
	if !contextFileSet[base] {
		return "", false
	}

	// Relative root-level: "SOUL.md", "./SOUL.md"
	dir := filepath.Dir(path)
	if dir == "." || dir == "/" || dir == "" {
		return base, true
	}

	// Absolute path under workspace (includes per-user subdirectories):
	// "/workspace/SOUL.md" or "/workspace/<userID>/SOUL.md"
	if workspace != "" && filepath.IsAbs(path) {
		cleanPath := filepath.Clean(path)
		cleanWS := filepath.Clean(workspace)
		if strings.HasPrefix(filepath.Dir(cleanPath), cleanWS) {
			return base, true
		}
	}

	return "", false
}

const defaultContextCacheTTL = 5 * time.Minute

// ContextFileInterceptor routes context file reads/writes to the agent store.
// Keeps SOUL.md, IDENTITY.md etc. in Postgres. USER.md and BOOTSTRAP.md are
// per-user; everything else is agent-level shared.
type ContextFileInterceptor struct {
	agentStore       store.AgentStore
	workspace        string // workspace root for matching absolute paths
	agentCache       cache.Cache[[]store.AgentContextFileData] // agent-level files, keyed by agentID.String()
	userCache        cache.Cache[[]store.AgentContextFileData] // user-level files, keyed by "agentID:userID"
	ttl              time.Duration
	permStore store.ConfigPermissionStore // nil = no group write restriction
}

// NewContextFileInterceptor creates an interceptor backed by the given agent store.
// Cache implementations are injected (in-memory or Redis) so callers control the backend.
func NewContextFileInterceptor(
	as store.AgentStore,
	workspace string,
	agentCache, userCache cache.Cache[[]store.AgentContextFileData],
) *ContextFileInterceptor {
	return &ContextFileInterceptor{
		agentStore: as,
		workspace:  workspace,
		agentCache: agentCache,
		userCache:  userCache,
		ttl:        defaultContextCacheTTL,
	}
}

// SetConfigPermStore sets the config permission store for group writer permission checks.
func (b *ContextFileInterceptor) SetConfigPermStore(s store.ConfigPermissionStore) {
	b.permStore = s
}

// ReadFile attempts to read a context file from the DB (with cache).
// USER.md and BOOTSTRAP.md are per-user; everything else is agent-level shared.
// Identity files (SOUL.md, IDENTITY.md, AGENTS.md, …) are already injected
// into the system prompt and reading them via tools is rejected to prevent
// agents from echoing persona configuration back to users.
//
// Returns (content, true, nil) if handled, or ("", false, nil) if not a context file.
func (b *ContextFileInterceptor) ReadFile(ctx context.Context, path string) (string, bool, error) {
	fileName, ok := isContextFile(path, b.workspace)
	if !ok {
		return "", false, nil
	}

	agentID := store.AgentIDFromContext(ctx)
	if agentID == uuid.Nil {
		return "", false, nil // no agent context
	}

	userID := store.UserIDFromContext(ctx)

	// USER.md and BOOTSTRAP.md route to user-level first, then fall back to agent-level.
	if userID != "" && (fileName == bootstrap.UserFile || fileName == bootstrap.BootstrapFile) {
		content, handled, err := b.readUserFile(ctx, agentID, userID, fileName)
		if err != nil {
			return "", handled, err
		}
		if content != "" {
			return content, handled, nil
		}
		return b.readAgentFile(ctx, agentID, fileName)
	}

	// Block reads of shared identity files. SOUL.md and CAPABILITIES.md are
	// readable when self_evolve is enabled so the agent can inspect current
	// content before making incremental updates.
	if fileName != bootstrap.UserFile && fileName != bootstrap.BootstrapFile && fileName != bootstrap.HeartbeatFile {
		allowEvolveRead := (fileName == bootstrap.SoulFile || fileName == bootstrap.CapabilitiesFile) && store.SelfEvolveFromContext(ctx)
		if !allowEvolveRead {
			return "", true, fmt.Errorf(
				"this file (%s) is already loaded into your context. You don't need to read it again — refer to your system instructions instead.",
				fileName,
			)
		}
	}

	return b.readAgentFile(ctx, agentID, fileName)
}

func (b *ContextFileInterceptor) readAgentFile(ctx context.Context, agentID uuid.UUID, fileName string) (string, bool, error) {
	for _, f := range b.cachedAgentFiles(ctx, agentID) {
		if f.FileName == fileName {
			return f.Content, true, nil
		}
	}
	return "", true, nil
}

func (b *ContextFileInterceptor) readUserFile(ctx context.Context, agentID uuid.UUID, userID, fileName string) (string, bool, error) {
	for _, f := range b.cachedUserFiles(ctx, agentID, userID) {
		if f.FileName == fileName {
			return f.Content, true, nil
		}
	}
	return "", true, nil
}

// WriteFile attempts to write a context file to the DB.
//   - USER.md is per-user
//   - BOOTSTRAP.md empty content → delete (first-run completed)
//   - SOUL.md / CAPABILITIES.md allowed when self_evolve is enabled (writes go agent-level)
//   - everything else: blocked (predefined identity must be edited from the dashboard)
//
// Returns (true, nil) if handled, or (false, nil) if not a context file.
func (b *ContextFileInterceptor) WriteFile(ctx context.Context, path, content string) (bool, error) {
	fileName, ok := isContextFile(path, b.workspace)
	if !ok {
		return false, nil
	}

	agentID := store.AgentIDFromContext(ctx)
	if agentID == uuid.Nil {
		return false, nil // no agent context
	}

	userID := store.UserIDFromContext(ctx)

	// Permission check: protected files in group context require allowlist membership.
	// Exception: during bootstrap onboarding (BOOTSTRAP.md still exists for this user),
	// USER.md writes are allowed so the bot can complete the first-run ritual.
	if (strings.HasPrefix(userID, "group:") || strings.HasPrefix(userID, "guild:")) && protectedFileSet[fileName] {
		skipCheck := false
		if fileName == bootstrap.UserFile && b.hasBootstrapFile(ctx, agentID, userID) {
			skipCheck = true // onboarding in progress — allow USER.md write
		}
		if !skipCheck {
			senderID := store.SenderIDFromContext(ctx)
			if senderID != "" && b.permStore != nil {
				numericID := strings.SplitN(senderID, "|", 2)[0]
				allowed, err := b.permStore.CheckPermission(ctx, agentID, userID, store.ConfigTypeEditFile, numericID)
				if err != nil {
					slog.Warn("security.group_file_writer_check_failed",
						"error", err, "sender", numericID, "file", fileName, "group", userID)
					// fail open: allow write if check fails
				} else if !allowed {
					return true, fmt.Errorf("permission denied: you are not authorized to modify %s in this group. Ask a group file writer to add you with /addwriter", fileName)
				}
			}
			// senderID empty or no permStore = system context (cron, subagent) → fail open
		}
	}

	// BOOTSTRAP.md deletion: empty content = first-run completed → delete row.
	if fileName == bootstrap.BootstrapFile && content == "" && userID != "" {
		err := b.agentStore.DeleteUserContextFile(ctx, agentID, userID, fileName)
		if err == nil {
			b.invalidateUser(agentID, userID)
		}
		return true, err
	}

	// USER.md is the only file users can write through chat.
	if userID != "" && fileName == bootstrap.UserFile {
		err := b.agentStore.SetUserContextFile(ctx, agentID, userID, fileName, content)
		if err == nil {
			b.invalidateUser(agentID, userID)
		}
		return true, err
	}

	// HEARTBEAT.md is a system-managed signal file; allow writes at agent level.
	// SOUL.md / CAPABILITIES.md are gated by self_evolve and write to agent level.
	if fileName != bootstrap.HeartbeatFile {
		allowEvolve := (fileName == bootstrap.SoulFile || fileName == bootstrap.CapabilitiesFile) && store.SelfEvolveFromContext(ctx)
		if !allowEvolve {
			return true, fmt.Errorf(
				"this file (%s) is part of the agent's predefined configuration and cannot be modified through chat. "+
					"Only the agent owner can edit it from the management dashboard.",
				fileName,
			)
		}
		slog.Info("self-evolve: file updated",
			"file", fileName,
			"agent_id", agentID,
			"user_id", userID,
		)
	}

	err := b.agentStore.SetAgentContextFile(ctx, agentID, fileName, content)
	if err == nil {
		b.InvalidateAgent(agentID)
	}
	return true, err
}

// LoadContextFiles loads context files for a specific user+agent combination.
// Used by the agent loop to dynamically resolve context files for system prompt.
// Uses the same agentCache/userCache as ReadFile — invalidated on WriteFile and pubsub events.
//
// Layering: agent-level files form the base; per-user files (USER.md, BOOTSTRAP.md, …)
// override or extend the set when present.
func (b *ContextFileInterceptor) LoadContextFiles(ctx context.Context, agentID uuid.UUID, userID string) []bootstrap.ContextFile {
	if userID != "" {
		agentFiles := b.cachedAgentFiles(ctx, agentID)
		userFiles := b.cachedUserFiles(ctx, agentID, userID)

		userMap := make(map[string]string, len(userFiles))
		for _, f := range userFiles {
			if f.Content != "" {
				userMap[f.FileName] = f.Content
			}
		}

		var result []bootstrap.ContextFile
		for _, f := range agentFiles {
			content := f.Content
			if uc, ok := userMap[f.FileName]; ok {
				content = uc
			}
			if content == "" {
				continue
			}
			result = append(result, bootstrap.ContextFile{
				Path:    f.FileName,
				Content: content,
			})
		}

		// Include user-only files not present at agent level
		// (e.g. BOOTSTRAP.md — seeded per-user for onboarding, not at agent level)
		agentFileSet := make(map[string]bool, len(agentFiles))
		for _, f := range agentFiles {
			agentFileSet[f.FileName] = true
		}
		for _, f := range userFiles {
			if !agentFileSet[f.FileName] && f.Content != "" {
				result = append(result, bootstrap.ContextFile{
					Path:    f.FileName,
					Content: f.Content,
				})
			}
		}

		return result
	}

	// Fallback: agent-level only
	agentFiles := b.cachedAgentFiles(ctx, agentID)
	var result []bootstrap.ContextFile
	for _, f := range agentFiles {
		if f.Content == "" {
			continue
		}
		result = append(result, bootstrap.ContextFile{
			Path:    f.FileName,
			Content: f.Content,
		})
	}
	return result
}

// cachedAgentFiles returns agent-level context files, using agentCache.
// Same cache used by readAgentFile — invalidated by WriteFile and pubsub events.
func (b *ContextFileInterceptor) cachedAgentFiles(ctx context.Context, agentID uuid.UUID) []store.AgentContextFileData {
	key := agentID.String()
	if files, ok := b.agentCache.Get(ctx, key); ok {
		return files
	}
	files, err := b.agentStore.GetAgentContextFiles(ctx, agentID)
	if err != nil {
		return nil
	}
	b.agentCache.Set(ctx, key, files, b.ttl)
	return files
}

// cachedUserFiles returns user-level context files, using userCache.
// Same cache used by readUserFile — invalidated by WriteFile and pubsub events.
func (b *ContextFileInterceptor) cachedUserFiles(ctx context.Context, agentID uuid.UUID, userID string) []store.AgentContextFileData {
	key := agentID.String() + ":" + userID
	if files, ok := b.userCache.Get(ctx, key); ok {
		return files
	}
	files, err := b.agentStore.GetUserContextFiles(ctx, agentID, userID)
	if err != nil {
		return nil
	}
	// Convert to AgentContextFileData for unified cache storage
	cached := make([]store.AgentContextFileData, len(files))
	for i, f := range files {
		cached[i] = store.AgentContextFileData{
			AgentID:  f.AgentID,
			FileName: f.FileName,
			Content:  f.Content,
		}
	}
	b.userCache.Set(ctx, key, cached, b.ttl)
	return cached
}

// InvalidateAgent clears the cache for a specific agent (called from event handler).
func (b *ContextFileInterceptor) InvalidateAgent(agentID uuid.UUID) {
	ctx := context.Background()
	b.agentCache.Delete(ctx, agentID.String())
	// Also clear user caches for this agent
	b.userCache.DeleteByPrefix(ctx, agentID.String()+":")
}

// InvalidateAll clears all cached entries.
func (b *ContextFileInterceptor) InvalidateAll() {
	ctx := context.Background()
	b.agentCache.Clear(ctx)
	b.userCache.Clear(ctx)
}

// hasBootstrapFile checks if BOOTSTRAP.md still exists in user_context_files,
// indicating the user is still in onboarding. Used to exempt USER.md writes
// from group permission checks during the first-run ritual.
func (b *ContextFileInterceptor) hasBootstrapFile(ctx context.Context, agentID uuid.UUID, userID string) bool {
	content, _, err := b.readUserFile(ctx, agentID, userID, bootstrap.BootstrapFile)
	return err == nil && content != ""
}

// InvalidateUser clears the per-user cache for a specific agent+user combination.
func (b *ContextFileInterceptor) InvalidateUser(agentID uuid.UUID, userID string) {
	b.invalidateUser(agentID, userID)
}

func (b *ContextFileInterceptor) invalidateUser(agentID uuid.UUID, userID string) {
	b.userCache.Delete(context.Background(), agentID.String()+":"+userID)
}

// normalizeToRelative strips the workspace prefix from an absolute path,
// returning a workspace-relative path for consistent DB storage.
// e.g. "/home/user/workspace/SOUL.md" → "SOUL.md"
func normalizeToRelative(path, workspace string) string {
	if workspace == "" || !filepath.IsAbs(path) {
		return path
	}
	rel, err := filepath.Rel(filepath.Clean(workspace), filepath.Clean(path))
	if err != nil || strings.HasPrefix(rel, "..") {
		return path // outside workspace, return as-is
	}
	return rel
}
