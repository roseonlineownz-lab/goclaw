package audio

import (
	"sync"
	"time"
)

// VoiceCache is a thread-safe in-memory cache of TTS voices.
// Single-tenant: holds one voice list with TTL eviction.
//
// Multi-instance note: each gateway process maintains its own cache. Redis
// invalidation is deferred until scale-out is on the roadmap (P2-H1).
type VoiceCache struct {
	mu        sync.Mutex
	voices    []Voice
	expiresAt time.Time
	hasEntry  bool
	ttl       time.Duration
}

// NewVoiceCache creates a cache with the given TTL.
// ttl=0 means entries never expire.
func NewVoiceCache(ttl time.Duration) *VoiceCache {
	return &VoiceCache{ttl: ttl}
}

// Get returns the cached voices. Returns (nil, false) on miss
// (absent or TTL-expired).
func (c *VoiceCache) Get() ([]Voice, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.hasEntry {
		return nil, false
	}
	if c.ttl > 0 && time.Now().After(c.expiresAt) {
		c.voices = nil
		c.hasEntry = false
		return nil, false
	}
	return c.voices, true
}

// Set stores voices and refreshes TTL.
func (c *VoiceCache) Set(voices []Voice) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.voices = voices
	c.hasEntry = true
	if c.ttl > 0 {
		c.expiresAt = time.Now().Add(c.ttl)
	}
}

// Invalidate removes the cached entry. No-op if absent.
// Called by POST /v1/voices/refresh to force a live refetch.
func (c *VoiceCache) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.voices = nil
	c.hasEntry = false
}
