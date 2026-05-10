package audio_test

import (
	"sync"
	"testing"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/audio"
)

func TestVoiceCache_SetGetHit(t *testing.T) {
	c := audio.NewVoiceCache(time.Hour)
	voices := []audio.Voice{{ID: "v1", Name: "Bella"}}

	c.Set(voices)
	got, ok := c.Get()
	if !ok {
		t.Fatal("expected cache hit")
	}
	if len(got) != 1 || got[0].ID != "v1" {
		t.Errorf("unexpected voices: %+v", got)
	}
}

func TestVoiceCache_ExpiredMiss(t *testing.T) {
	c := audio.NewVoiceCache(10 * time.Millisecond)
	c.Set([]audio.Voice{{ID: "v1"}})

	time.Sleep(20 * time.Millisecond)
	_, ok := c.Get()
	if ok {
		t.Fatal("expected cache miss after TTL expiry")
	}
}

func TestVoiceCache_NoTTLNeverExpires(t *testing.T) {
	c := audio.NewVoiceCache(0)
	c.Set([]audio.Voice{{ID: "v1"}})
	time.Sleep(15 * time.Millisecond)
	if _, ok := c.Get(); !ok {
		t.Fatal("ttl=0 should never expire")
	}
}

func TestVoiceCache_Invalidate(t *testing.T) {
	c := audio.NewVoiceCache(time.Hour)
	c.Set([]audio.Voice{{ID: "v1"}})
	c.Invalidate()
	if _, ok := c.Get(); ok {
		t.Fatal("expected miss after invalidate")
	}
}

func TestVoiceCache_OverwriteRefreshesEntry(t *testing.T) {
	c := audio.NewVoiceCache(time.Hour)
	c.Set([]audio.Voice{{ID: "v1"}})
	c.Set([]audio.Voice{{ID: "v2"}, {ID: "v3"}})

	got, ok := c.Get()
	if !ok || len(got) != 2 || got[0].ID != "v2" {
		t.Fatalf("expected overwrite to replace voices, got %+v ok=%v", got, ok)
	}
}

func TestVoiceCache_ConcurrentSafe(t *testing.T) {
	c := audio.NewVoiceCache(time.Hour)
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() {
			c.Set([]audio.Voice{{ID: "v"}})
			c.Get()
			c.Invalidate()
		})
	}
	wg.Wait()
}
