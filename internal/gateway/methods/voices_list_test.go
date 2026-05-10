package methods_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/audio"
	"github.com/nextlevelbuilder/goclaw/internal/audio/elevenlabs"
	"github.com/nextlevelbuilder/goclaw/internal/gateway/methods"
)

// TestVoicesMethods_CacheHit verifies a warm cache entry is returned without
// calling the upstream provider.
func TestVoicesMethods_CacheHit(t *testing.T) {
	cache := audio.NewVoiceCache(time.Hour)
	voices := []audio.Voice{{ID: "v1", Name: "Bella"}}
	cache.Set(voices)

	m := methods.NewVoicesMethods(cache, nil)

	got, err := m.FetchVoices(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].ID != "v1" {
		t.Errorf("unexpected voices: %+v", got)
	}
}

// TestVoicesMethods_NoProvider verifies an error is returned when the cache
// misses and no provider is configured.
func TestVoicesMethods_NoProvider(t *testing.T) {
	cache := audio.NewVoiceCache(time.Hour)
	m := methods.NewVoicesMethods(cache, nil)

	_, err := m.FetchVoices(t.Context())
	if err == nil {
		t.Fatal("expected error when no provider configured")
	}
}

// TestVoicesMethods_LiveFetch verifies a cache miss triggers a live fetch via
// the provider and the result is stored in the cache.
func TestVoicesMethods_LiveFetch(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"voices": []map[string]any{
				{"voice_id": "v2", "name": "Adam", "category": "premade"},
			},
		})
	}))
	defer upstream.Close()

	cache := audio.NewVoiceCache(time.Hour)
	p := elevenlabs.NewTTSProvider(elevenlabs.Config{APIKey: "k", BaseURL: upstream.URL})
	m := methods.NewVoicesMethods(cache, p)

	got, err := m.FetchVoices(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].ID != "v2" {
		t.Errorf("unexpected voices: %+v", got)
	}

	cached, ok := cache.Get()
	if !ok || len(cached) != 1 {
		t.Error("expected live fetch result to be cached")
	}
}
