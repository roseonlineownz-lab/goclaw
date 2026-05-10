package telegram

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"

	sessions "github.com/nextlevelbuilder/goclaw/internal/agentsessions"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
)

// projectCommandTimeout bounds DB lookups + session update so a flaky DB
// cannot block the inbound message goroutine.
const projectCommandTimeout = 10 * time.Second

// handleProjectCommand handles /project subcommands (list/current/switch/clear).
// It runs synchronously and replies in the same chat. The handler is a no-op
// (returns false to let the caller know it didn't consume the message) when
// the required stores are unwired.
func (c *Channel) handleProjectCommand(
	ctx context.Context,
	chatID int64,
	chatIDStr, text, senderID string,
	isGroup, isForum bool,
	messageThreadID int,
	setThread func(*telego.SendMessageParams),
) bool {
	if c.sessionStore == nil || c.projectStore == nil || c.projectGrantStore == nil {
		c.replyText(chatID, "Project switching is not configured for this bot.", setThread)
		return true
	}

	ctx, cancel := context.WithTimeout(ctx, projectCommandTimeout)
	defer cancel()

	// Resolve user UUID from Telegram numeric sender ID.
	userUUID := c.resolveCommandUserID(ctx, senderID)

	// Build canonical session key. Telegram session-key shape mirrors the
	// rules the gateway consumer applies (consumer_normal builds the same
	// keys for inbound traffic). Keeping these in sync is a hard invariant —
	// if they diverge, the bot writes to a different session row than the
	// agent reads from.
	sessionKey := c.buildProjectSessionKey(senderID, chatIDStr, isGroup, isForum, messageThreadID)
	if sessionKey == "" {
		c.replyText(chatID, "Cannot resolve session for this chat.", setThread)
		return true
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
		RawText:    text,
	})
	c.replyText(chatID, reply, setThread)
	return true
}

// resolveCommandUserID maps a Telegram sender (numeric ID, possibly suffixed
// "<id>|<username>") to the canonical users.id UUID via the contact store.
// Returns "" when no mapping exists — callers must reject permission-gated
// subcommands in that case.
func (c *Channel) resolveCommandUserID(ctx context.Context, senderID string) string {
	cc := c.ContactCollector()
	if cc == nil {
		return ""
	}
	numeric := senderID
	if i := strings.IndexByte(numeric, '|'); i > 0 {
		numeric = numeric[:i]
	}
	uid, err := cc.ResolveTenantUserID(ctx, c.Name(), numeric)
	if err != nil {
		slog.Debug("project_command.resolve_user_failed",
			"channel", c.Name(), "sender", numeric, "err", err)
		return ""
	}
	return uid
}

// buildProjectSessionKey computes the agent session key the dispatch layer
// uses for inbound messages on this chat. Mirrors gateway_consumer_normal's
// key-construction switch so /project writes to the row the agent reads.
func (c *Channel) buildProjectSessionKey(senderID, chatIDStr string, isGroup, isForum bool, messageThreadID int) string {
	agentID := c.AgentID()
	if agentID == "" {
		return ""
	}
	channel := c.Name()
	if isGroup {
		if isForum {
			return sessions.BuildGroupTopicSessionKey(agentID, channel, chatIDStr, messageThreadID)
		}
		return sessions.BuildSessionKey(agentID, channel, sessions.PeerGroup, chatIDStr)
	}
	// DM: peer ID is the sender's numeric ID (chatID and senderID are the
	// same for direct chats). Trim the optional username suffix.
	peer := senderID
	if i := strings.IndexByte(peer, '|'); i > 0 {
		peer = peer[:i]
	}
	if peer == "" {
		peer = chatIDStr
	}
	if messageThreadID > 0 {
		return sessions.BuildDMThreadSessionKey(agentID, channel, peer, messageThreadID)
	}
	return sessions.BuildSessionKey(agentID, channel, sessions.PeerDirect, peer)
}

// replyText sends a plain-text reply to chatID, applying forum thread
// routing via the caller-provided setThread closure (handleBotCommand
// builds the same closure for every subcommand for consistency).
func (c *Channel) replyText(chatID int64, text string, setThread func(*telego.SendMessageParams)) {
	if text == "" {
		return
	}
	msg := tu.Message(tu.ID(chatID), text)
	if setThread != nil {
		setThread(msg)
	}
	if _, err := c.bot.SendMessage(context.Background(), msg); err != nil {
		slog.Debug("project_command.reply_send_failed", "err", err)
	}
}
