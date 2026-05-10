package channels

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/workspace"
)

// ProjectCommandDeps bundles the stores the shared /project handler needs.
// Sessions, Projects, ProjectGrants are required; callers must verify
// non-nil at the channel layer (when any dep is missing the channel should
// reply with a "not configured" hint rather than calling this helper).
//
// Episodics + BaseDir are optional. When both are set, /project switch
// and /project clear go through workspace.SwitchSessionProject which keeps
// the FS layout coherent with the new binding (relocates the session
// subdir to the new project slug + retags session-scoped episodic memory).
// When either is missing, the handler falls back to a bare DB-only
// UpdateProject — the next agent run will write under the new path but
// pre-existing files stay where they are.
type ProjectCommandDeps struct {
	Sessions      store.SessionCoreStore
	Projects      store.ProjectStore
	ProjectGrants store.ProjectGrantStore
	Episodics     store.EpisodicStore
	BaseDir       string
}

// ProjectCommandRequest is the parsed-from-channel input to the shared
// handler. SessionKey is the canonical agent session key for the chat
// (caller computes via internal/agentsessions.BuildSessionKey* helpers).
type ProjectCommandRequest struct {
	SessionKey string
	UserID     string // app-side user UUID string; "" when caller cannot resolve
	// RawText is the full message text starting with "/project". The handler
	// trims and splits internally so each channel only forwards the raw line.
	RawText string
}

// HandleProjectCommand executes the /project subcommand encoded in
// req.RawText and returns the human-readable reply text to send back to the
// chat. It never panics; on internal error it returns a short user-facing
// message and logs the underlying cause.
//
// Subcommands:
//   - /project list                — show projects the user has access to
//   - /project current             — print current session binding
//   - /project switch <slug>       — bind session to <slug> (RBAC-checked)
//   - /project clear               — clear session binding (fall back to
//                                    channel default + parent override)
//   - /project (no args) | /project help → usage text
func HandleProjectCommand(ctx context.Context, deps ProjectCommandDeps, req ProjectCommandRequest) string {
	if deps.Sessions == nil || deps.Projects == nil || deps.ProjectGrants == nil {
		return "Project switching is not configured for this bot."
	}

	sub, rest := splitProjectSubcommand(req.RawText)
	switch sub {
	case "", "help":
		return projectHelpText()
	case "list":
		return handleProjectList(ctx, deps, req.UserID)
	case "current":
		return handleProjectCurrent(ctx, deps, req.SessionKey)
	case "switch":
		return handleProjectSwitch(ctx, deps, req, rest)
	case "clear":
		return handleProjectClear(ctx, deps, req)
	default:
		return "Unknown subcommand. Try /project help."
	}
}

// splitProjectSubcommand peels the leading "/project" off RawText and
// returns (subcommand, remainder). Subcommand is lowercased; remainder
// preserves the user's casing (slugs are lowercase by convention but we
// don't enforce here so the GetBySlug error message stays accurate).
func splitProjectSubcommand(raw string) (sub, rest string) {
	text := strings.TrimSpace(raw)
	// Strip the leading /project (or /project@bot suffix the channel may
	// have already stripped — defensive against either shape).
	if !strings.HasPrefix(text, "/project") {
		return "", ""
	}
	text = strings.TrimSpace(text[len("/project"):])
	// Drop @botname suffix if any survived.
	if strings.HasPrefix(text, "@") {
		if sp := strings.IndexAny(text, " \t"); sp >= 0 {
			text = strings.TrimSpace(text[sp+1:])
		} else {
			text = ""
		}
	}
	if text == "" {
		return "", ""
	}
	parts := strings.SplitN(text, " ", 2)
	sub = strings.ToLower(strings.TrimSpace(parts[0]))
	if len(parts) == 2 {
		rest = strings.TrimSpace(parts[1])
	}
	return sub, rest
}

func projectHelpText() string {
	return "Project commands:\n" +
		"/project list — projects you have access to\n" +
		"/project current — show this session's project binding\n" +
		"/project switch <slug> — bind this session to <slug>\n" +
		"/project clear — clear the session binding"
}

func handleProjectList(ctx context.Context, deps ProjectCommandDeps, userID string) string {
	if userID == "" {
		return "Cannot list projects without a user identity. Pair this account first."
	}
	grants, err := deps.ProjectGrants.ListForUser(ctx, userID)
	if err != nil {
		slog.Warn("project_command.list_failed", "user_id", userID, "err", err)
		return "Failed to load your projects."
	}
	// Resolve slug for each grant. ListForUser returns the row's project_id;
	// we cannot dump uuid to the user. Skip slugs we cannot resolve rather
	// than failing the whole list.
	if len(grants) == 0 {
		return "You don't have access to any projects yet."
	}
	var lines []string
	lines = append(lines, "Projects you have access to:")
	for _, g := range grants {
		pid, err := uuid.Parse(g.ProjectID)
		if err != nil {
			continue
		}
		p, err := deps.Projects.Get(ctx, pid)
		if err != nil {
			continue
		}
		lines = append(lines, fmt.Sprintf("• %s (%s)", p.Slug, g.Role))
	}
	if len(lines) == 1 {
		return "You don't have access to any projects yet."
	}
	return strings.Join(lines, "\n")
}

func handleProjectCurrent(ctx context.Context, deps ProjectCommandDeps, sessionKey string) string {
	sess := deps.Sessions.Get(ctx, sessionKey)
	if sess == nil || sess.ProjectID == nil {
		return "No project is bound to this session. The channel default (if any) will be used."
	}
	p, err := deps.Projects.Get(ctx, *sess.ProjectID)
	if err != nil {
		slog.Warn("project_command.current_lookup_failed",
			"session_key", sessionKey, "project_id", sess.ProjectID, "err", err)
		return "Session is bound to a project but its slug could not be resolved."
	}
	return fmt.Sprintf("Current project: %s", p.Slug)
}

func handleProjectSwitch(ctx context.Context, deps ProjectCommandDeps, req ProjectCommandRequest, slug string) string {
	if slug == "" {
		return "Usage: /project switch <slug>"
	}
	if req.UserID == "" {
		return "Cannot switch projects without a user identity. Pair this account first."
	}
	// Slug is whitespace-only on this path? Trim.
	slug = strings.TrimSpace(slug)
	// First space-separated word only — ignore trailing extra args.
	if sp := strings.IndexAny(slug, " \t"); sp >= 0 {
		slug = slug[:sp]
	}
	p, err := deps.Projects.GetBySlug(ctx, slug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Sprintf("Project %q not found.", slug)
		}
		slog.Warn("project_command.switch_lookup_failed", "slug", slug, "err", err)
		return "Failed to look up project."
	}
	rank, isOwner, found, err := deps.ProjectGrants.ResolveProjectRole(ctx, req.UserID, p.ID.String())
	if err != nil {
		slog.Warn("project_command.switch_perm_failed",
			"user_id", req.UserID, "project_id", p.ID, "err", err)
		return "Failed to check project access."
	}
	// Allow when caller is owner OR has any non-zero grant rank.
	if !found || (rank == 0 && !isOwner) {
		slog.Info("security.project_switch_denied",
			"user_id", req.UserID, "project_id", p.ID, "slug", slug)
		return fmt.Sprintf("You do not have access to project %q.", slug)
	}
	pid := p.ID
	if err := applyProjectSwitch(ctx, deps, req.SessionKey, &pid); err != nil {
		slog.Warn("project_command.switch_update_failed",
			"session_key", req.SessionKey, "project_id", pid, "err", err)
		return "Failed to switch project."
	}
	slog.Info("project_command.switched",
		"session_key", req.SessionKey, "user_id", req.UserID,
		"project_id", pid, "slug", slug)
	return fmt.Sprintf("Switched to project: %s", p.Slug)
}

// applyProjectSwitch routes the binding change through
// workspace.SwitchSessionProject when the orchestrator deps are wired,
// otherwise falls back to a bare DB UpdateProject. The fallback exists for
// callers (e.g. early tests) that have not been updated to provide the
// FS-side stores.
func applyProjectSwitch(ctx context.Context, deps ProjectCommandDeps, sessionKey string, newProjectID *uuid.UUID) error {
	if deps.Episodics != nil && deps.BaseDir != "" {
		return workspace.SwitchSessionProject(ctx, workspace.ProjectSwitchDeps{
			Sessions:  deps.Sessions,
			Projects:  deps.Projects,
			Episodics: deps.Episodics,
			BaseDir:   deps.BaseDir,
		}, sessionKey, newProjectID)
	}
	return deps.Sessions.UpdateProject(ctx, sessionKey, newProjectID)
}

func handleProjectClear(ctx context.Context, deps ProjectCommandDeps, req ProjectCommandRequest) string {
	if err := applyProjectSwitch(ctx, deps, req.SessionKey, nil); err != nil {
		slog.Warn("project_command.clear_failed",
			"session_key", req.SessionKey, "err", err)
		return "Failed to clear project binding."
	}
	slog.Info("project_command.cleared",
		"session_key", req.SessionKey, "user_id", req.UserID)
	return "Cleared project binding for this session."
}
