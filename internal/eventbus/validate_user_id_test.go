package eventbus

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestValidateUserID_EmptyUserID_NoWarn(t *testing.T) {
	buf, restore := captureSlog(t)
	defer restore()

	validateUserID(DomainEvent{
		Type:     EventRunCompleted,
		SourceID: "r1",
		UserID:   "",
	})

	if strings.Contains(buf.String(), "non_uuid_user_id") {
		t.Errorf("empty UserID should not trigger warning, got log:\n%s", buf.String())
	}
}

func TestValidateUserID_ValidUUID_NoWarn(t *testing.T) {
	buf, restore := captureSlog(t)
	defer restore()

	validateUserID(DomainEvent{
		Type:     EventEpisodicCreated,
		SourceID: "s1",
		UserID:   uuid.New().String(),
	})

	if strings.Contains(buf.String(), "non_uuid_user_id") {
		t.Errorf("valid UUID should not trigger warning, got log:\n%s", buf.String())
	}
}

func TestValidateUserID_NonUUIDWarns(t *testing.T) {
	buf, restore := captureSlog(t)
	defer restore()

	validateUserID(DomainEvent{
		Type:     EventEpisodicCreated,
		SourceID: "s1",
		UserID:   "alice@example.com", // email/username, not UUID
	})

	out := buf.String()
	if !strings.Contains(out, "non_uuid_user_id") {
		t.Errorf("non-UUID UserID should emit warning with distinct field, got log:\n%s", out)
	}
	if !strings.Contains(out, "alice@example.com") {
		t.Errorf("warning should include the offending value, got log:\n%s", out)
	}
	if !strings.Contains(out, "eventbus.non_uuid_user_id") {
		t.Errorf("warning message should be eventbus.non_uuid_user_id, got log:\n%s", out)
	}
}

func TestValidateUserID_DistinctFieldName_NoCollision(t *testing.T) {
	buf, restore := captureSlog(t)
	defer restore()

	validateUserID(DomainEvent{
		Type:     EventRunCompleted,
		SourceID: "r1",
		UserID:   "bad-user",
	})

	out := buf.String()
	if !strings.Contains(out, "non_uuid_user_id=") {
		t.Errorf("expected field name `non_uuid_user_id=`, got log:\n%s", out)
	}
}

// TestBusPublish_InvokesUserIDValidator verifies the validator is wired into Publish.
func TestBusPublish_InvokesUserIDValidator(t *testing.T) {
	buf, restore := captureSlog(t)
	defer restore()

	bus := newTestBus()
	ctx := t.Context()
	bus.Start(ctx)
	defer func() { _ = bus.Drain(500 * time.Millisecond) }()

	var received atomic.Int32
	bus.Subscribe(EventRunCompleted, func(_ context.Context, _ DomainEvent) error {
		received.Add(1)
		return nil
	})

	// Non-UUID UserID — must still dispatch (observability only).
	bus.Publish(DomainEvent{
		Type:     EventRunCompleted,
		SourceID: "user-validator-test",
		UserID:   "not-a-uuid",
	})

	deadline := time.Now().Add(200 * time.Millisecond)
	for received.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}

	if received.Load() != 1 {
		t.Errorf("expected event to still be dispatched (non-blocking), got received=%d", received.Load())
	}
	if !strings.Contains(buf.String(), "non_uuid_user_id") {
		t.Errorf("expected bus.Publish to invoke validateUserID, got log:\n%s", buf.String())
	}
}
