package workspace

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// ProjectSwitchDeps bundles the cross-cutting stores the orchestrator needs.
// The package boundary deliberately keeps SessionStore unaware of FS layout
// and ProjectStore unaware of session lifecycle — the orchestrator wires
// them together so /project switch can land DB + FS state coherently.
type ProjectSwitchDeps struct {
	Sessions  store.SessionCoreStore
	Projects  store.ProjectStore
	Episodics store.EpisodicStore
	BaseDir   string
}

// projectSwitchMu serialises switches per session_key. Lock duration is short
// (one DB UPDATE + one FS rename, typically a handful of ms). Without it two
// concurrent inbound /project commands on the same session could interleave
// and leave DB + FS desynced.
//
// Pattern matches cmd/gateway_consumer.go:announceMu.
var projectSwitchMu sync.Map // sessionKey string → *sync.Mutex

func projectSwitchLock(sessionKey string) *sync.Mutex {
	v, _ := projectSwitchMu.LoadOrStore(sessionKey, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// SwitchSessionProject re-binds an agent session from its current project
// (if any) to newProjectID, relocates the session FS subdirectory under the
// new project slug, and re-tags session-scoped episodic_summaries rows.
//
// Memory tables that are project-scoped without a session_key
// (memory_documents, memory_chunks, kg_entities, vault_documents) are NOT
// mutated — those rows describe work done under the OLD project and stay
// semantically attached to it.
//
// Failure modes (B2a strict-orphan):
//   - DB UpdateProject fails       → return error, no FS side effects
//   - Episodic UPDATE fails        → log warning, continue (session row is
//                                    canonical)
//   - FS rename fails (ENOTEMPTY,
//     cross-fs, perm)               → log warning, files orphan at the old
//                                    path; resolver will use the new path
//                                    going forward; switching back surfaces
//                                    the orphan files again. RelocateOnMerge
//                                    already handles cross-fs via copy+delete.
//
// The caller is responsible for permission checks; this function only
// enforces that the new project (when non-nil) actually exists.
func SwitchSessionProject(ctx context.Context, deps ProjectSwitchDeps, sessionKey string, newProjectID *uuid.UUID) error {
	if sessionKey == "" {
		return errors.New("project_switch: session_key is required")
	}
	if deps.Sessions == nil || deps.Projects == nil || deps.Episodics == nil {
		return errors.New("project_switch: deps not configured")
	}
	if deps.BaseDir == "" {
		return errors.New("project_switch: base dir is required")
	}

	mu := projectSwitchLock(sessionKey)
	mu.Lock()
	defer mu.Unlock()

	sess := deps.Sessions.Get(ctx, sessionKey)
	if sess == nil {
		return fmt.Errorf("project_switch: session %q not found", sessionKey)
	}
	oldProjectID := sess.ProjectID

	// No-op when the binding is already correct (keep the operation
	// idempotent so the bot can replay the command safely).
	if uuidPtrsEqual(oldProjectID, newProjectID) {
		return nil
	}

	// Resolve slugs up-front so we can fail fast if the new project does
	// not exist, before any DB mutation.
	oldSlug := lookupProjectSlug(ctx, deps.Projects, oldProjectID)
	newSlug := lookupProjectSlug(ctx, deps.Projects, newProjectID)
	if newProjectID != nil && newSlug == "" {
		return fmt.Errorf("project_switch: new project %s not found", newProjectID)
	}

	// Phase 1: DB updates. Session row is authoritative; episodic update is
	// best-effort to keep session-scoped episodic memory aligned.
	if err := deps.Sessions.UpdateProject(ctx, sessionKey, newProjectID); err != nil {
		return fmt.Errorf("project_switch: update session: %w", err)
	}
	if err := deps.Episodics.UpdateSessionProject(ctx, sessionKey, oldProjectID, newProjectID); err != nil {
		slog.WarnContext(ctx, "project_switch.episodic_update_failed",
			"session_key", sessionKey,
			"old_project_id", uuidPtrStr(oldProjectID),
			"new_project_id", uuidPtrStr(newProjectID),
			"err", err,
		)
	}

	// Phase 2: FS relocate session subdir. Only meaningful when both sides
	// have a project (slugs known). When oldSlug or newSlug is empty the
	// session lived at a non-project path under the old binding — we leave
	// the FS untouched and let the resolver build the new path next run.
	if oldSlug != "" && newSlug != "" {
		oldDir, err1 := ProjectWorkspacePath(deps.BaseDir, oldSlug)
		newDir, err2 := ProjectWorkspacePath(deps.BaseDir, newSlug)
		if err1 == nil && err2 == nil {
			seg := SanitizeSegment(sessionKey)
			oldSessPath := filepath.Join(oldDir, "sessions", seg)
			newSessPath := filepath.Join(newDir, "sessions", seg)
			if err := RelocateOnMerge(oldSessPath, newSessPath); err != nil {
				slog.WarnContext(ctx, "project_switch.fs_relocate_failed",
					"session_key", sessionKey,
					"old_path", oldSessPath,
					"new_path", newSessPath,
					"err", err,
				)
			}
		}
	}

	slog.InfoContext(ctx, "project_switch.completed",
		"session_key", sessionKey,
		"old_project_id", uuidPtrStr(oldProjectID),
		"new_project_id", uuidPtrStr(newProjectID),
	)
	return nil
}

func lookupProjectSlug(ctx context.Context, projects store.ProjectStore, pid *uuid.UUID) string {
	if pid == nil {
		return ""
	}
	p, err := projects.Get(ctx, *pid)
	if err != nil || p == nil {
		return ""
	}
	return p.Slug
}

func uuidPtrsEqual(a, b *uuid.UUID) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func uuidPtrStr(u *uuid.UUID) string {
	if u == nil {
		return ""
	}
	return u.String()
}
