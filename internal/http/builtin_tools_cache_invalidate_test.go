package http

import (
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

// TestBuiltinToolsHandler_EmitCacheInvalidate_CarriesKindAndKey verifies the
// emit helper populates Kind and Key in the broadcast payload.
func TestBuiltinToolsHandler_EmitCacheInvalidate_CarriesKindAndKey(t *testing.T) {
	mb := bus.New()
	defer mb.Close()

	h := &BuiltinToolsHandler{msgBus: mb}

	var captured bus.CacheInvalidatePayload
	var gotEvent bool
	mb.Subscribe("test-capture", func(e bus.Event) {
		if e.Name != protocol.EventCacheInvalidate {
			return
		}
		p, ok := e.Payload.(bus.CacheInvalidatePayload)
		if !ok {
			return
		}
		captured = p
		gotEvent = true
	})

	h.emitCacheInvalidate("tts")

	if !gotEvent {
		t.Fatal("no cache invalidate event received")
	}
	if captured.Kind != bus.CacheKindBuiltinTools {
		t.Errorf("kind = %q, want %q", captured.Kind, bus.CacheKindBuiltinTools)
	}
	if captured.Key != "tts" {
		t.Errorf("key = %q, want %q", captured.Key, "tts")
	}
}

// TestBuiltinToolsHandler_EmitCacheInvalidate_EmptyKeyBroadcasts verifies
// passing an empty key broadcasts a global invalidation (all builtin tools).
func TestBuiltinToolsHandler_EmitCacheInvalidate_EmptyKeyBroadcasts(t *testing.T) {
	mb := bus.New()
	defer mb.Close()

	h := &BuiltinToolsHandler{msgBus: mb}

	var captured bus.CacheInvalidatePayload
	mb.Subscribe("test-capture", func(e bus.Event) {
		if p, ok := e.Payload.(bus.CacheInvalidatePayload); ok {
			captured = p
		}
	})

	h.emitCacheInvalidate("")

	if captured.Kind != bus.CacheKindBuiltinTools {
		t.Errorf("kind = %q, want %q", captured.Kind, bus.CacheKindBuiltinTools)
	}
	if captured.Key != "" {
		t.Errorf("key = %q, want empty (global)", captured.Key)
	}
}

// TestBuiltinToolsHandler_EmitCacheInvalidate_NilMsgBusNoop verifies the
// helper is safe to call when msgBus is unset (e.g., in tests/desktop lite).
func TestBuiltinToolsHandler_EmitCacheInvalidate_NilMsgBusNoop(t *testing.T) {
	h := &BuiltinToolsHandler{msgBus: nil}
	// Must not panic.
	h.emitCacheInvalidate("tts")
}
