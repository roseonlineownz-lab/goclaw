package discord

import (
	"context"

	"github.com/nextlevelbuilder/goclaw/internal/channels/media"
)

// transcribeAudio calls the shared STT proxy service with the given audio file.
// Returns ("", nil) silently when STT is not configured or filePath is empty.
func (c *Channel) transcribeAudio(ctx context.Context, filePath string) (string, error) {
	return media.TranscribeAudio(ctx, media.STTConfig{
		ProxyURL:       c.config.STTProxyURL,
		APIKey:         c.config.STTAPIKey,
		STTTenantID:    c.config.STTTenantID,
		TimeoutSeconds: c.config.STTTimeoutSeconds,
	}, filePath)
}
