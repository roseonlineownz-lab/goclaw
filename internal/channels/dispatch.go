package channels

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// WebhookRoute holds a path and handler pair for mounting on the main gateway mux.
type WebhookRoute struct {
	Path    string
	Handler http.Handler
}

// dispatchOutbound consumes outbound messages from the bus and routes them
// to the appropriate channel. Internal channels are silently skipped.
func (m *Manager) dispatchOutbound(ctx context.Context) {
	slog.Info("outbound dispatcher started")

	for {
		select {
		case <-ctx.Done():
			slog.Info("outbound dispatcher stopped")
			return
		default:
			msg, ok := m.bus.SubscribeOutbound(ctx)
			if !ok {
				continue
			}

			// Skip internal channels
			if IsInternalChannel(msg.Channel) {
				continue
			}

			m.mu.RLock()
			channel, exists := m.channels[msg.Channel]
			m.mu.RUnlock()

			if !exists {
				slog.Warn("unknown channel for outbound message", "channel", msg.Channel)
				continue
			}

			// Filter out temp media files that no longer exist (already sent by another dispatch).
			if len(msg.Media) > 0 {
				tmpDir := os.TempDir()
				filtered := msg.Media[:0]
				for _, media := range msg.Media {
					if media.URL != "" && strings.HasPrefix(media.URL, tmpDir) {
						if _, err := os.Stat(media.URL); err != nil {
							slog.Debug("skipping already-delivered temp media", "path", media.URL)
							continue
						}
					}
					filtered = append(filtered, media)
				}
				msg.Media = filtered
				// If only media was in this message and all files are gone, skip entirely.
				if len(msg.Media) == 0 && msg.Content == "" {
					continue
				}
			}

			// Resolve target chat_id: if the inbound contact has been merged into a
			// canonical user, re-route DM replies to the canonical contact's chat_id.
			// Group-originated replies always stay in the original group chat (privacy
			// zone applies to FS/memory only, not channel addressability).
			// Composite-key lookup at dispatch time — no ContactID on OutboundMessage.
			msg.ChatID = m.resolveTargetChatID(ctx, msg)

			sendCtx := ctx

			// Add agent audio context for per-agent TTS voice override
			if msg.AgentID != uuid.Nil && len(msg.AgentOtherConfig) > 0 {
				sendCtx = store.WithAgentAudio(sendCtx, store.AgentAudioSnapshot{
					AgentID:     msg.AgentID,
					OtherConfig: msg.AgentOtherConfig,
				})
			}

			if err := channel.Send(sendCtx, msg); err != nil {
				slog.Error("error sending message to channel",
					"channel", msg.Channel,
					"chat_id", msg.ChatID,
					"content_len", len(msg.Content),
					"content_preview", Truncate(msg.Content, 160),
					"error", err,
				)
				// Try to send a text-only error notification back to the chat.
				// Only for media failures — text-only failures likely mean the chat
				// is inaccessible (kicked, blocked, etc.) so retrying won't help.
				if len(msg.Media) > 0 {
					notifyMsg := bus.OutboundMessage{
						Channel:  msg.Channel,
						ChatID:   msg.ChatID,
						Content:  formatChannelSendError(err),
						Metadata: sendErrorMeta(msg.Metadata),
					}
					if err2 := channel.Send(sendCtx, notifyMsg); err2 != nil {
						slog.Warn("failed to send error notification",
							"channel", msg.Channel, "error", err2)
					}
				}
			}

			// Clean up temp media files only. Workspace-generated files are preserved
			// so they remain accessible via workspace/web UI after delivery.
			tmpDir := os.TempDir()
			for _, media := range msg.Media {
				if media.URL != "" && strings.HasPrefix(media.URL, tmpDir) {
					if err := os.Remove(media.URL); err != nil {
						slog.Debug("failed to clean up media file", "path", media.URL, "error", err)
					}
				}
			}
		}
	}
}

// WebhookHandlers returns all webhook handlers from channels that implement WebhookChannel.
// Used to mount webhook routes on the main gateway mux.
func (m *Manager) WebhookHandlers() []WebhookRoute {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var routes []WebhookRoute
	for _, ch := range m.channels {
		if wh, ok := ch.(WebhookChannel); ok {
			if path, handler := wh.WebhookHandler(); path != "" && handler != nil {
				routes = append(routes, WebhookRoute{Path: path, Handler: handler})
			}
		}
	}
	return routes
}

// SendToChannel delivers a message to a specific channel by name.
func (m *Manager) SendToChannel(ctx context.Context, channelName, chatID, content string) error {
	m.mu.RLock()
	channel, exists := m.channels[channelName]
	m.mu.RUnlock()

	if !exists {
		return fmt.Errorf("channel %s not found", channelName)
	}

	msg := bus.OutboundMessage{
		Channel: channelName,
		ChatID:  chatID,
		Content: content,
	}

	return channel.Send(ctx, msg)
}

// resolveTargetChatID returns the effective chat_id for an outbound message,
// re-routing to a canonical contact's chat_id when the original sender contact
// has been merged. Group contacts always keep their original chat_id.
// Falls back to msg.ChatID on any lookup failure (system messages, no contact row, etc.).
func (m *Manager) resolveTargetChatID(ctx context.Context, msg bus.OutboundMessage) string {
	if m.contactStore == nil {
		return msg.ChatID
	}

	contact, err := m.contactStore.GetContactByChannelAndChatID(ctx, msg.Channel, msg.ChatID)
	if err != nil {
		if !errors.Is(err, store.ErrContactNotFound) {
			slog.Warn("dispatch: contact lookup failed, using original chat_id",
				"channel", msg.Channel, "chat_id", msg.ChatID, "error", err)
		}
		return msg.ChatID
	}

	// No merge — route normally.
	if contact.MergedID == nil {
		return msg.ChatID
	}

	// Group contacts: privacy zone applies to FS/memory only.
	// The bot remains a group member — reply stays in the original group chat.
	if contact.PeerKind != nil && *contact.PeerKind == "group" {
		return msg.ChatID
	}

	// DM contact merged: find the canonical user's DM on the same channel.
	canonical, err := m.contactStore.GetCanonicalDMContact(ctx, *contact.MergedID, msg.Channel)
	if err != nil {
		if !errors.Is(err, store.ErrContactIDNotFound) {
			slog.Warn("dispatch: canonical DM lookup failed, using original chat_id",
				"channel", msg.Channel, "merged_id", contact.MergedID, "error", err)
		}
		// No canonical DM on this channel — fall back to original.
		return msg.ChatID
	}

	return canonical.SenderID
}

// --- Send error notification helpers ---

// telegramAPIDescRe extracts the human-readable description from Telegram Bot API errors.
// Example: `telego: sendPhoto: api: 400 "Bad Request: not enough rights to send photos to the chat"`
//
//	→ "not enough rights to send photos to the chat"
var telegramAPIDescRe = regexp.MustCompile(`"Bad Request:\s*(.+?)"`)

// formatChannelSendError converts a channel.Send error into a user-friendly message.
// Never exposes raw library/HTTP details.
func formatChannelSendError(err error) string {
	raw := err.Error()
	lower := strings.ToLower(raw)

	// Telegram "Bad Request: <description>" — extract description
	if m := telegramAPIDescRe.FindStringSubmatch(raw); len(m) == 2 {
		return fmt.Sprintf("⚠️ Send failed: %s", m[1])
	}

	// Common Telegram API errors (non-Bad Request)
	switch {
	case strings.Contains(lower, "not enough rights"):
		return "⚠️ Send failed: bot doesn't have permission to send this type of message."
	case strings.Contains(lower, "chat not found"):
		return "⚠️ Send failed: chat not found."
	case strings.Contains(lower, "bot was blocked"):
		return "⚠️ Send failed: bot was blocked by the user."
	case strings.Contains(lower, "user is deactivated"):
		return "⚠️ Send failed: user account is deactivated."
	case strings.Contains(lower, "too many requests") || strings.Contains(lower, "flood"):
		return "⚠️ Send failed: rate limited by Telegram. Please try again later."
	case strings.Contains(lower, "file is too big") || strings.Contains(lower, "wrong file"):
		return "⚠️ Send failed: file is too large or invalid for Telegram."
	}

	// Generic fallback — don't expose internals
	return "⚠️ Failed to deliver message. Check bot logs for details."
}

// sendErrorMeta copies only the routing fields from outbound metadata.
// Strips reply_to_message_id, placeholder_key, audio_as_voice, etc.
// that could cause unintended side effects on the error notification.
func sendErrorMeta(orig map[string]string) map[string]string {
	if orig == nil {
		return nil
	}
	meta := make(map[string]string)
	for _, k := range []string{"local_key", "message_thread_id"} {
		if v := orig[k]; v != "" {
			meta[k] = v
		}
	}
	if len(meta) == 0 {
		return nil
	}
	return meta
}
