package providers

// VoyageEmbeddingProvider wraps OpenAIEmbeddingProvider with Voyage AI base URL.
// Voyage is Anthropic's embedding partner and uses the same wire format as OpenAI.
type VoyageEmbeddingProvider struct {
	*OpenAIEmbeddingProvider
}

// NewVoyageEmbeddingProvider creates an embedding provider for Voyage AI.
// NOTE: Voyage models top out at 1024 dims (voyage-3) or 1536 dims (voyage-3-large).
// The system requires halfvec(3072) — use text-embedding-3-large via OpenAI instead.
// Voyage is retained here for caller compatibility; callers must pass a 3072-dim model.
func NewVoyageEmbeddingProvider(apiKey, model string) *VoyageEmbeddingProvider {
	if model == "" {
		model = "voyage-3-large" // 1536 dims — caller must supply a 3072-dim model override
	}
	p := NewOpenAIEmbeddingProvider(apiKey, "https://api.voyageai.com/v1", model)
	p.providerName = "voyage"
	return &VoyageEmbeddingProvider{OpenAIEmbeddingProvider: p}
}
