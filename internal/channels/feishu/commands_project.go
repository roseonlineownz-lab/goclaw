package feishu

import (
	"context"
	"log/slog"
	"time"

	sessions "github.com/nextlevelbuilder/goclaw/internal/agentsessions"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
)

// projectCommandTimeout bounds DB lookups + session update on the Feishu
// inbound goroutine. Mirrors the writer-command timeout for symmetry.
const projectCommandTimeout = 10 * time.Second

// handleProjectCommand handles the /project subcommands inside Feishu/Lark.
// Replies in-chat using the same sendText path as other writer commands so
// thread routing stays consistent.
func (c *Channel) handleProjectCommand(ctx context.Context, mc *messageContext) {
	if c.sessionStore == nil || c.projectStore == nil || c.projectGrantStore == nil {
		c.sendCommandReply(ctx, mc, "Project switching is not configured for this bot.")
		return
	}

	ctx, cancel := context.WithTimeout(ctx, projectCommandTimeout)
	defer cancel()

	userUUID := c.resolveProjectCommandUserID(ctx, mc.SenderID)
	sessionKey := c.buildProjectSessionKey(mc)
	if sessionKey == "" {
		c.sendCommandReply(ctx, mc, "Cannot resolve session for this chat.")
		return
	}

	reply := channels.HandleProjectCommand(ctx, channels.ProjectCommandDeps{
		Sessions:      c.sessionStore,
		Projects:      c.projectStore,
		ProjectGrants: c.projectGrantStore,
		Episodics:     c.episodicStore,
		BaseDir:       c.baseDir,
	}, channels.ProjectCommandRequest{
		SessionKey: sessionKey,
		UserID:     userUUID,
		RawText:    mc.Content,
	})
	c.sendCommandReply(ctx, mc, reply)
}

// resolveProjectCommandUserID maps a Feishu sender open_id to the canonical
// users.id UUID via the contact store. Returns "" when no mapping exists —
// permission-gated subcommands will then deny the action.
func (c *Channel) resolveProjectCommandUserID(ctx context.Context, openID string) string {
	cc := c.ContactCollector()
	if cc == nil || openID == "" {
		return ""
	}
	uid, err := cc.ResolveTenantUserID(ctx, c.Name(), openID)
	if err != nil {
		slog.Debug("feishu.project_command.resolve_user_failed",
			"sender", openID, "err", err)
		return ""
	}
	return uid
}

// buildProjectSessionKey computes the agent session key used by the dispatch
// layer for inbound traffic on this chat. Must match the consumer's key
// rules — divergence means /project writes to a different row than the
// agent reads.
func (c *Channel) buildProjectSessionKey(mc *messageContext) string {
	agentID := c.AgentID()
	if agentID == "" {
		return ""
	}
	channel := c.Name()
	if mc.ChatType == "group" {
		// Feishu threads (mc.ThreadID) are not modelled at the session-key
		// level today — the dispatcher uses the chat-level group key. Keep
		// /project writes consistent with that.
		return sessions.BuildSessionKey(agentID, channel, sessions.PeerGroup, mc.ChatID)
	}
	// p2p: peer is the sender's open_id (DM peer = sender).
	peer := mc.SenderID
	if peer == "" {
		peer = mc.ChatID
	}
	return sessions.BuildSessionKey(agentID, channel, sessions.PeerDirect, peer)
}
