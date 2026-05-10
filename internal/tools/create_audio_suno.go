package tools

import (
	"context"
	"fmt"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
)

// callSunoMusicGen generates music via the Suno API.
// Suno must be registered as an LLM provider with provider_type "suno".
func callSunoMusicGen(ctx context.Context, apiKey, apiBase, model, prompt string, params map[string]any) ([]byte, *providers.Usage, error) {
	// TODO: Implement Suno music generation
	return nil, nil, fmt.Errorf("suno music generation not yet implemented")
}
