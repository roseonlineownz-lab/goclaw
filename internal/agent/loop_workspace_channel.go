package agent

import (
	"context"
	"errors"
	"log/slog"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/workspace"
)

// buildChannelResolveCtx assembles the input for the 12-scenario channel
// resolver from the live RunRequest plus runtime store lookups (UserKey,
// TeamKey, Merged status). The result feeds workspace.ResolveChannel.
//
// Inputs that fail to resolve (DB miss, malformed UUID) collapse to "" — the
// resolver then falls through to the appropriate non-user-scoped branch.
func (l *Loop) buildChannelResolveCtx(ctx context.Context, req *RunRequest, teamKey string) workspace.ChannelResolveCtx {
	ccx := workspace.ChannelResolveCtx{
		BaseDir:     l.dataDir,
		AgentKey:    l.id,
		TeamKey:     teamKey,
		ChannelType: req.ChannelType,
		ChatID:      req.ChatID,
		SenderID:    req.SenderID,
		SenderKind:  classifySenderKind(req.ChannelType, req.PeerKind),
	}

	// SenderID fallback: channel inbound sets SenderID (per-individual id),
	// but legacy/web paths leave it empty. For DM zones we need a stable
	// segment — fall back to UserID.
	if ccx.SenderID == "" {
		ccx.SenderID = req.UserID
	}

	// UserKey lookup — only meaningful when UserID is a real users.id UUID.
	// Channel inbound passes the merged-or-channel-user UUID; web auth passes
	// the human user UUID. Empty / non-UUID → leave UserKey blank.
	if l.usersStore != nil && req.UserID != "" {
		if uid, err := uuid.Parse(req.UserID); err == nil {
			if u, gerr := l.usersStore.Get(ctx, uid); gerr == nil && u != nil {
				ccx.UserKey = u.UserKey
			}
		}
	}

	// Merged: only relevant for channel sessions (web sessions have no contact).
	// channel_contacts.merged_id IS NOT NULL → contact merged into canonical user.
	if l.contactStore != nil && req.ChannelType != "" && req.ChatID != "" {
		contact, err := l.contactStore.GetContactByChannelAndChatID(ctx, req.ChannelType, req.ChatID)
		if err == nil && contact != nil && contact.MergedID != nil {
			ccx.Merged = true
			// Resolve canonical user_key from merged_id when not already set.
			if ccx.UserKey == "" && l.usersStore != nil {
				if u, gerr := l.usersStore.Get(ctx, *contact.MergedID); gerr == nil && u != nil {
					ccx.UserKey = u.UserKey
				}
			}
		} else if err != nil && !errors.Is(err, store.ErrContactNotFound) {
			slog.Warn("workspace: contact lookup failed",
				"channel", req.ChannelType, "chat_id", req.ChatID, "err", err)
		}
	}

	return ccx
}

// classifySenderKind derives SenderKind from the inbound RunRequest fields:
//   - empty ChannelType → web sender
//   - PeerKind == "group" → channel group
//   - otherwise (PeerKind == "direct" or empty) → channel DM
//
// "http" / "web" channel types are also treated as web (synthetic web channels
// from the HTTP API path).
func classifySenderKind(channelType, peerKind string) workspace.SenderKind {
	switch channelType {
	case "", "http", "web":
		return workspace.SenderWeb
	}
	if peerKind == "group" {
		return workspace.SenderChannelGroup
	}
	return workspace.SenderChannelDM
}

// channelToWorkspace converts the (path, ChannelScope) pair returned by
// ResolveChannel into a *WorkspaceContext for the rest of the pipeline.
//
// Only ActivePath is read in production today (see plans/reports/
// scout-260507-workspace-context-consumers.md). Other fields are set to
// sensible defaults so downstream readers (system prompt label, future
// enforcement) get a coherent value.
func channelToWorkspace(path string, scope workspace.ChannelScope, ccx workspace.ChannelResolveCtx, shared bool) *workspace.WorkspaceContext {
	wsScope := workspace.ScopePersonal
	switch scope.ZoneKind {
	case "user-team", "team-contact", "team-group", "team-system":
		wsScope = workspace.ScopeTeam
	}

	memScope := "user"
	if shared {
		memScope = "shared"
	}

	owner := ccx.UserKey
	if owner == "" {
		owner = ccx.ChatID
	}

	return &workspace.WorkspaceContext{
		ActivePath:       path,
		Scope:            wsScope,
		MemoryScope:      memScope,
		KGScope:          memScope,
		OwnerID:          owner,
		EnforcementLabel: workspace.DefaultEnforcementLabel(wsScope, shared),
	}
}
