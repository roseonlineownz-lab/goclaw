package http

import (
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

// TestSkillsHandler_EmitCacheInvalidate_CarriesKindAndKey verifies the emit
// helper populates Kind and Key in the broadcast payload.
func TestSkillsHandler_EmitCacheInvalidate_CarriesKindAndKey(t *testing.T) {
	mb := bus.New()
	defer mb.Close()

	h := &SkillsHandler{msgBus: mb}

	var captured bus.CacheInvalidatePayload
	var gotEvent bool
	mb.Subscribe("test-capture", func(e bus.Event) {
		if e.Name != protocol.EventCacheInvalidate {
			return
		}
		if p, ok := e.Payload.(bus.CacheInvalidatePayload); ok {
			captured = p
			gotEvent = true
		}
	})

	skillID := uuid.New().String()
	h.emitCacheInvalidate(bus.CacheKindSkills, skillID)

	if !gotEvent {
		t.Fatal("no cache invalidate event received")
	}
	if captured.Kind != bus.CacheKindSkills {
		t.Errorf("kind = %q, want %q", captured.Kind, bus.CacheKindSkills)
	}
	if captured.Key != skillID {
		t.Errorf("key = %q, want %q", captured.Key, skillID)
	}
}

// TestSkillsHandler_EmitCacheInvalidate_GrantsGlobal verifies grant-change
// callers emit with empty key (global invalidation).
func TestSkillsHandler_EmitCacheInvalidate_GrantsGlobal(t *testing.T) {
	mb := bus.New()
	defer mb.Close()

	h := &SkillsHandler{msgBus: mb}

	var captured bus.CacheInvalidatePayload
	mb.Subscribe("test-capture", func(e bus.Event) {
		if p, ok := e.Payload.(bus.CacheInvalidatePayload); ok {
			captured = p
		}
	})

	h.emitCacheInvalidate(bus.CacheKindSkillGrants, "")

	if captured.Kind != bus.CacheKindSkillGrants {
		t.Errorf("kind = %q, want %q", captured.Kind, bus.CacheKindSkillGrants)
	}
	if captured.Key != "" {
		t.Errorf("key = %q, want empty (global)", captured.Key)
	}
}

// TestSkillsHandler_EmitCacheInvalidate_NilMsgBusNoop verifies the helper is
// safe with an unset msgBus.
func TestSkillsHandler_EmitCacheInvalidate_NilMsgBusNoop(t *testing.T) {
	h := &SkillsHandler{msgBus: nil}
	// Must not panic.
	h.emitCacheInvalidate(bus.CacheKindSkills, "x")
}
