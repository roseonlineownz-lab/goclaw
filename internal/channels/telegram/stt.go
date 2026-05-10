package telegram

import (
	"context"

	"github.com/nextlevelbuilder/goclaw/internal/channels/media"
)

// transcribeAudio calls the configured STT proxy service with the given audio file and returns
// the transcribed text. Returns ("", nil) silently when STT is not configured or filePath is empty.
// Delegates to the shared media.TranscribeAudio implementation.
func (c *Channel) transcribeAudio(ctx context.Context, filePath string) (string, error) {
	return media.TranscribeAudio(ctx, media.STTConfig{
		ProxyURL:       c.config.STTProxyURL,
		APIKey:         c.config.STTAPIKey,
		STTTenantID:    c.config.STTTenantID,
		TimeoutSeconds: c.config.STTTimeoutSeconds,
	}, filePath)
}
