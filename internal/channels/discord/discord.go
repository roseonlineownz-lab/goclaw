package discord

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/nextlevelbuilder/goclaw/internal/audio"
	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
	"github.com/nextlevelbuilder/goclaw/internal/channels/typing"
	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

const pairingDebounceTime = 60 * time.Second

// Channel connects to Discord via the Bot API using gateway events.
type Channel struct {
	*channels.BaseChannel
	session           *discordgo.Session
	config            config.DiscordConfig
	botUserID         string   // populated on start
	placeholders      sync.Map // placeholderKey string → messageID string
	typingCtrls       sync.Map // channelID string → *typing.Controller
	agentStore        store.AgentStore            // for agent key lookup (nil = writer commands disabled)
	configPermStore   store.ConfigPermissionStore // for group file writer management (nil = writer commands disabled)
	pendingStore      store.PendingMessageStore   // for group history buffering (nil = no pending storage)
	sessionStore      store.SessionCoreStore      // for /project session binding (nil = /project disabled)
	projectStore      store.ProjectStore          // for /project slug lookup (nil = /project disabled)
	projectGrantStore store.ProjectGrantStore     // for /project switch RBAC (nil = /project disabled)
	episodicStore     store.EpisodicStore         // for /project switch episodic retag (nil → DB-only)
	baseDir           string                      // workspace root for /project FS relocation
	audioMgr          *audio.Manager              // unified STT via audio.Manager (nil = no STT)
	// pairingService, pairingDebounce, approvedGroups, groupHistory, historyLimit, requireMention
	// are inherited from channels.BaseChannel.
}

// Option configures optional dependencies for the Discord channel.
// Mirrors the Telegram/Feishu pattern so callers can wire stores
// post-construction without breaking the New() signature when new optional
// dependencies are added.
type Option func(*Channel)

// WithAgentStore enables agent key → UUID resolution for writer + project
// commands.
func WithAgentStore(s store.AgentStore) Option {
	return func(c *Channel) { c.agentStore = s }
}

// WithConfigPermStore enables the group file writer ACL used by /addwriter,
// /removewriter, /writers.
func WithConfigPermStore(s store.ConfigPermissionStore) Option {
	return func(c *Channel) { c.configPermStore = s }
}

// WithPendingMessageStore wires the group-history buffer.
func WithPendingMessageStore(s store.PendingMessageStore) Option {
	return func(c *Channel) { c.pendingStore = s }
}

// WithSessionStore enables /project session-binding writes.
func WithSessionStore(s store.SessionCoreStore) Option {
	return func(c *Channel) { c.sessionStore = s }
}

// WithProjectStore enables /project slug lookups.
func WithProjectStore(s store.ProjectStore) Option {
	return func(c *Channel) { c.projectStore = s }
}

// WithProjectGrantStore enables /project switch RBAC checks.
func WithProjectGrantStore(s store.ProjectGrantStore) Option {
	return func(c *Channel) { c.projectGrantStore = s }
}

// WithEpisodicStore wires episodic retag for /project switch.
func WithEpisodicStore(s store.EpisodicStore) Option {
	return func(c *Channel) { c.episodicStore = s }
}

// WithBaseDir sets the workspace root used for /project FS relocation.
func WithBaseDir(dir string) Option {
	return func(c *Channel) { c.baseDir = dir }
}

// New creates a new Discord channel. audioMgr is optional (nil = STT disabled).
// Optional stores (agent, config-perm, pending, session, project, project-grant)
// flow in via Options.
func New(cfg config.DiscordConfig, msgBus *bus.MessageBus, pairingSvc store.PairingStore, audioMgr *audio.Manager, opts ...Option) (*Channel, error) {
	session, err := discordgo.New("Bot " + cfg.Token)
	if err != nil {
		return nil, fmt.Errorf("create discord session: %w", err)
	}

	// Request necessary intents
	session.Identify.Intents = discordgo.IntentsGuildMessages |
		discordgo.IntentsDirectMessages |
		discordgo.IntentsMessageContent

	base := channels.NewBaseChannel(channels.TypeDiscord, msgBus, cfg.AllowFrom)
	base.ValidatePolicy(cfg.DMPolicy, cfg.GroupPolicy)

	requireMention := true
	if cfg.RequireMention != nil {
		requireMention = *cfg.RequireMention
	}

	historyLimit := cfg.HistoryLimit
	if historyLimit == 0 {
		historyLimit = channels.DefaultGroupHistoryLimit
	}

	ch := &Channel{
		BaseChannel: base,
		session:     session,
		config:      cfg,
		audioMgr:    audioMgr,
	}
	for _, opt := range opts {
		opt(ch)
	}
	ch.SetRequireMention(requireMention)
	ch.SetPairingService(pairingSvc)
	ch.SetGroupHistory(channels.MakeHistory(channels.TypeDiscord, ch.pendingStore))
	ch.SetHistoryLimit(historyLimit)
	return ch, nil
}

// Start opens the Discord gateway connection and begins receiving events.
func (c *Channel) Start(_ context.Context) error {
	c.GroupHistory().StartFlusher()
	slog.Info("starting discord bot")

	c.session.AddHandler(c.handleMessage)

	if err := c.session.Open(); err != nil {
		return fmt.Errorf("open discord session: %w", err)
	}

	// Fetch bot identity
	user, err := c.session.User("@me")
	if err != nil {
		c.session.Close()
		return fmt.Errorf("fetch discord bot identity: %w", err)
	}
	c.botUserID = user.ID

	c.SetRunning(true)
	slog.Info("discord bot connected", "username", user.Username, "id", user.ID)

	return nil
}

// BlockReplyEnabled returns the per-channel block_reply override (nil = inherit gateway default).
func (c *Channel) BlockReplyEnabled() *bool { return c.config.BlockReply }

// SetPendingCompaction configures LLM-based auto-compaction for pending messages.
func (c *Channel) SetPendingCompaction(cfg *channels.CompactionConfig) {
	if gh := c.GroupHistory(); gh != nil {
		gh.SetCompactionConfig(cfg)
	}
}

// Stop closes the Discord gateway connection.
func (c *Channel) Stop(_ context.Context) error {
	c.GroupHistory().StopFlusher()
	slog.Info("stopping discord bot")
	c.SetRunning(false)
	return c.session.Close()
}

// Send delivers an outbound message to a Discord channel.
func (c *Channel) Send(ctx context.Context, msg bus.OutboundMessage) (err error) {
	if !c.IsRunning() {
		return fmt.Errorf("discord bot not running")
	}

	channelID := msg.ChatID
	if channelID == "" {
		return fmt.Errorf("empty chat ID for discord send")
	}

	// Resolve placeholder key from metadata (inbound message ID), fall back to channelID.
	// Keying by message ID prevents race conditions when multiple messages
	// arrive in the same channel before the first response is sent.
	placeholderKey := channelID
	if pk := msg.Metadata["placeholder_key"]; pk != "" {
		placeholderKey = pk
	}

	// Placeholder update (e.g. LLM retry notification): edit the placeholder
	// but keep it alive for the final response. Don't stop typing or cleanup.
	if msg.Metadata["placeholder_update"] == "true" {
		if pID, ok := c.placeholders.Load(placeholderKey); ok {
			if msgID, ok := pID.(string); ok {
				_, _ = c.session.ChannelMessageEdit(channelID, msgID, msg.Content)
			}
		}
		return nil
	}

	typingCtrl := c.currentTypingCtrl(channelID)
	defer func() {
		c.finishTyping(channelID, typingCtrl, err)
	}()

	content := msg.Content

	// TTS auto-apply: convert [[tts]] tagged responses to voice
	if c.audioMgr != nil && content != "" {
		isVoiceInbound := msg.Metadata["is_voice_inbound"] == "true"
		ttsResult, ttsErr := c.audioMgr.AutoApplyToText(ctx, content, "discord", isVoiceInbound, "")
		if ttsErr != nil {
			slog.Debug("discord: tts auto-apply error", "error", ttsErr)
		}
		if ttsResult != nil && ttsResult.AudioPath != "" {
			// Send voice file via media API
			if err := c.sendMediaMessage(channelID, "", []bus.MediaAttachment{{
				URL:         ttsResult.AudioPath,
				ContentType: ttsResult.AudioMime,
			}}); err != nil {
				slog.Warn("discord: tts auto-apply voice send failed, falling back to text", "error", err)
			} else {
				// Voice sent successfully
				strippedText := strings.TrimSpace(ttsResult.Text)
				if strippedText == "" {
					// Voice-only: delete placeholder (no text to show)
					if pID, ok := c.placeholders.LoadAndDelete(placeholderKey); ok {
						if msgID, ok := pID.(string); ok {
							_ = c.session.ChannelMessageDelete(channelID, msgID)
						}
					}
					return nil
				}
				// Has remaining text: let normal flow handle placeholder edit
				content = strippedText
			}
		}
		// Update content with directives stripped (even if TTS not applied)
		if ttsResult != nil {
			content = ttsResult.Text
		}
	}

	// Handle outbound media attachments: send files via Discord's file upload API.
	if len(msg.Media) > 0 {
		// Delete placeholder if present
		if pID, ok := c.placeholders.Load(placeholderKey); ok {
			c.placeholders.Delete(placeholderKey)
			if msgID, ok := pID.(string); ok {
				_ = c.session.ChannelMessageDelete(channelID, msgID)
			}
		}
		return c.sendMediaMessage(channelID, content, msg.Media)
	}

	// NO_REPLY cleanup: content is empty when agent suppresses reply.
	// Delete placeholder and return without sending any message.
	if content == "" {
		if pID, ok := c.placeholders.Load(placeholderKey); ok {
			c.placeholders.Delete(placeholderKey)
			if msgID, ok := pID.(string); ok {
				_ = c.session.ChannelMessageDelete(channelID, msgID)
			}
		}
		return nil
	}

	// Try to edit the placeholder "Thinking..." message with the first chunk,
	// then send the rest as follow-up messages.
	if pID, ok := c.placeholders.Load(placeholderKey); ok {
		c.placeholders.Delete(placeholderKey)
		if msgID, ok := pID.(string); ok {
			const maxLen = 2000
			editContent := content
			remaining := ""

			if len(editContent) > maxLen {
				// Break at a newline if possible
				cutAt := maxLen
				if idx := lastIndexByte(content[:maxLen], '\n'); idx > maxLen/2 {
					cutAt = idx + 1
				}
				editContent = content[:cutAt]
				remaining = content[cutAt:]
			}

			if _, editErr := c.session.ChannelMessageEdit(channelID, msgID, editContent); editErr == nil {
				// Send remaining content as follow-up messages
				if remaining != "" {
					return c.sendChunked(channelID, remaining)
				}
				return nil
			} else {
				slog.Warn("discord: placeholder edit failed, sending new message",
					"channel_id", channelID, "placeholder_id", msgID, "error", editErr)
			}
		}
		// Fall through to send new message if edit fails
	}

	// Send as new message(s), chunking if needed
	return c.sendChunked(channelID, content)
}

// sendChunked sends a message, splitting into multiple messages if over 2000 chars.
// Uses markdown-aware chunking to avoid splitting inside fenced code blocks.
func (c *Channel) sendChunked(channelID, content string) error {
	const maxLen = 2000

	for _, chunk := range channels.ChunkMarkdown(content, maxLen) {
		if _, err := c.session.ChannelMessageSend(channelID, chunk); err != nil {
			return fmt.Errorf("send discord message: %w", err)
		}
	}

	return nil
}

// lastIndexByte returns the last index of byte c in s, or -1.
func lastIndexByte(s string, c byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == c {
			return i
		}
	}
	return -1
}

func (c *Channel) currentTypingCtrl(channelID string) *typing.Controller {
	ctrl, ok := c.typingCtrls.Load(channelID)
	if !ok {
		return nil
	}

	typed, ok := ctrl.(*typing.Controller)
	if !ok {
		c.typingCtrls.Delete(channelID)
		return nil
	}

	return typed
}

func (c *Channel) finishTyping(channelID string, expected *typing.Controller, sendErr error) {
	if expected == nil {
		return
	}
	if sendErr != nil {
		slog.Warn("discord: outbound send failed; keeping typing indicator active until TTL",
			"channel_id", channelID, "error", sendErr)
		return
	}

	current, ok := c.typingCtrls.Load(channelID)
	if !ok {
		return
	}

	typed, ok := current.(*typing.Controller)
	if !ok {
		c.typingCtrls.Delete(channelID)
		return
	}
	if typed != expected {
		return
	}

	c.typingCtrls.Delete(channelID)
	typed.Stop()
}
