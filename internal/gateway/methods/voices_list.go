package methods

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/nextlevelbuilder/goclaw/internal/audio"
	"github.com/nextlevelbuilder/goclaw/internal/audio/elevenlabs"
	"github.com/nextlevelbuilder/goclaw/internal/gateway"
	"github.com/nextlevelbuilder/goclaw/internal/i18n"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

// VoicesMethods handles voices.list and voices.refresh WS RPC methods.
// provider is optional — when nil, voices.list returns an error
// because no ElevenLabs key is pre-configured.
type VoicesMethods struct {
	cache    *audio.VoiceCache
	provider *elevenlabs.TTSProvider
}

// NewVoicesMethods creates a VoicesMethods handler.
// provider may be nil when the gateway has no global ElevenLabs key.
func NewVoicesMethods(cache *audio.VoiceCache, provider *elevenlabs.TTSProvider) *VoicesMethods {
	return &VoicesMethods{cache: cache, provider: provider}
}

// Register wires voices.list and voices.refresh onto the MethodRouter.
func (m *VoicesMethods) Register(router *gateway.MethodRouter) {
	router.Register(protocol.MethodVoicesList, m.handleList)
	router.Register(protocol.MethodVoicesRefresh, m.handleRefresh)
}

// FetchVoices returns cached voices, or fetches live on miss.
// Exported so it can be called from HTTP handler tests and integration code.
func (m *VoicesMethods) FetchVoices(ctx context.Context) ([]audio.Voice, error) {
	if voices, ok := m.cache.Get(); ok {
		return voices, nil
	}
	if m.provider == nil {
		return nil, fmt.Errorf("no ElevenLabs provider configured")
	}
	voices, err := m.provider.ListVoices(ctx)
	if err != nil {
		return nil, err
	}
	m.cache.Set(voices)
	return voices, nil
}

func (m *VoicesMethods) handleList(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	locale := store.LocaleFromContext(ctx)

	voices, err := m.FetchVoices(ctx)
	if err != nil {
		slog.Warn("voices.list: fetch failed", "error", err)
		client.SendResponse(protocol.NewErrorResponse(req.ID,
			protocol.ErrInternal,
			i18n.T(locale, i18n.MsgVoicesListFailed, err.Error())))
		return
	}

	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{"voices": voices}))
}

func (m *VoicesMethods) handleRefresh(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	locale := store.LocaleFromContext(ctx)

	m.cache.Invalidate()

	if m.provider == nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID,
			protocol.ErrInternal,
			i18n.T(locale, i18n.MsgVoicesListFailed, "no provider configured")))
		return
	}

	voices, err := m.provider.ListVoices(ctx)
	if err != nil {
		slog.Warn("voices.refresh: fetch failed", "error", err)
		client.SendResponse(protocol.NewErrorResponse(req.ID,
			protocol.ErrInternal,
			i18n.T(locale, i18n.MsgVoicesListFailed, err.Error())))
		return
	}

	m.cache.Set(voices)
	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{"voices": voices}))
}
