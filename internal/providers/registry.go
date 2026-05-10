package providers

import (
	"fmt"
	"io"
	"log/slog"
	"sync"
)

// Registry manages available LLM providers.
type Registry struct {
	providers map[string]Provider
	mu        sync.RWMutex

	// roundRobinCounters stores shared round-robin state keyed by "providerName/modality"
	// so that ChatGPTOAuthRouter instances (created per-request) share rotation state
	// within a modality (e.g. chat) while keeping distinct modalities (e.g. image)
	// rotating on independent counters — see RoundRobinNext.
	roundRobinMu       sync.Mutex
	roundRobinCounters map[string]int
}

// NewRegistry creates a provider registry.
func NewRegistry() *Registry {
	return &Registry{
		providers:          make(map[string]Provider),
		roundRobinCounters: make(map[string]int),
	}
}

// RoundRobinNext returns the current round-robin index for the given
// (base provider, modality) pair and optionally advances it.
// Used by ChatGPTOAuthRouter to persist rotation state across per-request
// router instances. The modality segment (e.g. "chat", "image") keeps
// independent counters per modality so that bursty traffic on one modality
// cannot skew the offset used by another.
func (r *Registry) RoundRobinNext(baseProviderName, modality string, poolSize int, advance bool) int {
	if poolSize <= 0 {
		return 0
	}
	key := baseProviderName + "/" + modality
	r.roundRobinMu.Lock()
	defer r.roundRobinMu.Unlock()
	idx := r.roundRobinCounters[key] % poolSize
	if advance {
		r.roundRobinCounters[key] = (idx + 1) % poolSize
	}
	return idx
}

// Register adds a provider to the registry.
// If a provider with the same name already exists, it is closed before replacement.
func (r *Registry) Register(provider Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := provider.Name()
	if old, ok := r.providers[name]; ok {
		if c, ok := old.(io.Closer); ok {
			c.Close()
		}
	}
	r.providers[name] = provider
}

// Unregister removes a provider by name.
func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if old, ok := r.providers[name]; ok {
		if c, ok := old.(io.Closer); ok {
			c.Close()
		}
		delete(r.providers, name)
	}
}

// Get returns a provider by name using context (kept for compatibility with context-aware callers).
func (r *Registry) Get(name string) (Provider, error) {
	return r.GetByName(name)
}

// GetByName returns a provider by name.
func (r *Registry) GetByName(name string) (Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if p, ok := r.providers[name]; ok {
		return p, nil
	}
	return nil, fmt.Errorf("provider not found: %s", name)
}

// Close calls Close() on all providers that implement io.Closer.
func (r *Registry) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, p := range r.providers {
		if c, ok := p.(io.Closer); ok {
			if err := c.Close(); err != nil {
				slog.Warn("provider close error", "key", key, "error", err)
			}
		}
	}
}

// List returns all registered provider names.
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	return names
}
