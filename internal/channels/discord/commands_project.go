package discord

import (
	"context"
	"log/slog"
	"time"

	"github.com/bwmarrin/discordgo"

	sessions "github.com/nextlevelbuilder/goclaw/internal/agentsessions"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
)

// projectCommandTimeout bounds DB lookups + session update on the Discord
// inbound goroutine.
const projectCommandTimeout = 10 * time.Second

// handleProjectCommand handles the /project subcommands inside Discord.
// Replies in-channel via ChannelMessageSend so the user sees feedback in
// the same channel they ran the command in.
func (c *Channel) handleProjectCommand(m *discordgo.MessageCreate) {
	if c.sessionStore == nil || c.projectStore == nil || c.projectGrantStore == nil {
		c.session.ChannelMessageSend(m.ChannelID, "Project switching is not configured for this bot.")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), projectCommandTimeout)
	defer cancel()

	userUUID := c.resolveProjectCommandUserID(ctx, m.Author.ID)
	sessionKey := c.buildProjectSessionKey(m)
	if sessionKey == "" {
		c.session.ChannelMessageSend(m.ChannelID, "Cannot resolve session for this chat.")
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
		// Discord accepts both ! and / prefixes; normalize the leading ! so the
		// shared handler's "/project" prefix detection works either way.
		RawText: normalizeProjectRawText(m.Content),
	})
	if reply != "" {
		if _, err := c.session.ChannelMessageSend(m.ChannelID, reply); err != nil {
			slog.Debug("discord.project_command.reply_failed", "err", err)
		}
	}
}

// normalizeProjectRawText accepts content starting with either "!project" or
// "/project" and rewrites the leading "!" to "/" so the shared handler's
// prefix scanner finds "/project". Anything else is passed through as-is.
func normalizeProjectRawText(text string) string {
	if len(text) > 0 && text[0] == '!' {
		return "/" + text[1:]
	}
	return text
}

// resolveProjectCommandUserID maps a Discord author ID to the canonical
// users.id UUID via the contact store. Returns "" when no mapping exists —
// permission-gated subcommands will then deny.
func (c *Channel) resolveProjectCommandUserID(ctx context.Context, authorID string) string {
	cc := c.ContactCollector()
	if cc == nil || authorID == "" {
		return ""
	}
	uid, err := cc.ResolveTenantUserID(ctx, c.Name(), authorID)
	if err != nil {
		slog.Debug("discord.project_command.resolve_user_failed",
			"sender", authorID, "err", err)
		return ""
	}
	return uid
}

// buildProjectSessionKey reproduces the session-key shape the gateway
// consumer uses for inbound Discord traffic. Discord's "group" semantics are
// guild channels (m.GuildID != ""), and the chat ID for session purposes is
// the channel ID — matching how the consumer routes messages.
func (c *Channel) buildProjectSessionKey(m *discordgo.MessageCreate) string {
	agentID := c.AgentID()
	if agentID == "" {
		return ""
	}
	channel := c.Name()
	if m.GuildID != "" {
		return sessions.BuildSessionKey(agentID, channel, sessions.PeerGroup, m.ChannelID)
	}
	// DM: peer = author ID; channel ID is the DM channel.
	peer := m.Author.ID
	if peer == "" {
		peer = m.ChannelID
	}
	return sessions.BuildSessionKey(agentID, channel, sessions.PeerDirect, peer)
}
