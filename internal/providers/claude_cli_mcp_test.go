package providers

import (
	"testing"
)

// --- SignBridgeContext tests ---

func TestSignBridgeContext_Deterministic(t *testing.T) {
	key := "test-secret"
	sig1 := SignBridgeContext(key, "agent1", "user1", "telegram", "chat1", "direct", "/workspace")
	sig2 := SignBridgeContext(key, "agent1", "user1", "telegram", "chat1", "direct", "/workspace")
	if sig1 != sig2 {
		t.Errorf("expected deterministic output, got %q and %q", sig1, sig2)
	}
	if sig1 == "" {
		t.Error("expected non-empty signature")
	}
}

func TestSignBridgeContext_DifferentKey(t *testing.T) {
	sig1 := SignBridgeContext("key-a", "agent1", "user1", "", "", "", "")
	sig2 := SignBridgeContext("key-b", "agent1", "user1", "", "", "", "")
	if sig1 == sig2 {
		t.Error("different keys should produce different signatures")
	}
}

func TestSignBridgeContext_FieldOrder(t *testing.T) {
	key := "test-secret"
	sig1 := SignBridgeContext(key, "a", "b", "c", "d", "e", "f")
	sig2 := SignBridgeContext(key, "b", "a", "c", "d", "e", "f")
	if sig1 == sig2 {
		t.Error("swapping field values should produce different signatures")
	}
}

// --- VerifyBridgeContext tests ---

func TestVerifyBridgeContext_Valid(t *testing.T) {
	key := "gateway-token"
	sig := SignBridgeContext(key, "agent1", "user1", "telegram", "chat1", "direct", "/ws")

	if !VerifyBridgeContext(key, "agent1", "user1", "telegram", "chat1", "direct", "/ws", sig) {
		t.Error("expected true for valid signature")
	}
}

func TestVerifyBridgeContext_InvalidSig(t *testing.T) {
	if VerifyBridgeContext("key", "agent1", "user1", "", "", "", "", "invalid-sig") {
		t.Error("expected false for invalid signature")
	}
}

func TestVerifyBridgeContext_EmptyFields(t *testing.T) {
	key := "test-key"
	sig := SignBridgeContext(key, "", "", "", "", "", "")

	if !VerifyBridgeContext(key, "", "", "", "", "", "", sig) {
		t.Error("expected true for empty fields with valid signature")
	}
}

// --- Extra params (localKey, sessionKey) tests ---

func TestSignBridgeContext_WithExtraParams(t *testing.T) {
	key := "test-secret"
	sig1 := SignBridgeContext(key, "agent1", "user1", "telegram", "chat1", "direct", "/ws")
	sig2 := SignBridgeContext(key, "agent1", "user1", "telegram", "chat1", "direct", "/ws", "-100123:topic:42", "session-abc")

	if sig1 == sig2 {
		t.Error("signature with extra params should differ from signature without")
	}
}

func TestVerifyBridgeContext_WithExtraParams(t *testing.T) {
	key := "gateway-token"
	localKey := "-100123:topic:42"
	sessionKey := "session-abc"
	sig := SignBridgeContext(key, "agent1", "user1", "telegram", "chat1", "direct", "/ws", localKey, sessionKey)

	if !VerifyBridgeContext(key, "agent1", "user1", "telegram", "chat1", "direct", "/ws", sig, localKey, sessionKey) {
		t.Error("expected true for valid signature with extra params")
	}
}

func TestVerifyBridgeContext_FallbackWithoutExtraParams(t *testing.T) {
	key := "gateway-token"
	// Pre-localKey session: signed without extra params
	sig := SignBridgeContext(key, "agent1", "user1", "telegram", "chat1", "direct", "/ws")

	// New code passes localKey/sessionKey but signature was created without them
	if !VerifyBridgeContext(key, "agent1", "user1", "telegram", "chat1", "direct", "/ws", sig, "-100123:topic:42", "session-abc") {
		t.Error("expected true for fallback (pre-localKey session)")
	}
}

func TestVerifyBridgeContext_ExtraParamOrderMatters(t *testing.T) {
	key := "gateway-token"
	sig := SignBridgeContext(key, "agent1", "user1", "", "", "", "", "localKey", "sessionKey")

	if !VerifyBridgeContext(key, "agent1", "user1", "", "", "", "", sig, "localKey", "sessionKey") {
		t.Error("expected true for same order")
	}

	if VerifyBridgeContext(key, "agent1", "user1", "", "", "", "", sig, "sessionKey", "localKey") {
		t.Error("expected false for swapped extra param order")
	}
}

// FallbackOldTenantSig: sessions signed with old format including tenantID should still verify.
func TestVerifyBridgeContext_FallbackOldTenantSig(t *testing.T) {
	key := "gateway-token"
	// Old-format: payload was agentID|userID|channel|chatID|peerKind|workspace|tenantID
	// Simulate by calling old SignBridgeContext manually via extra param (tenantID as first extra)
	oldSig := SignBridgeContext(key, "agent1", "user1", "telegram", "chat1", "direct", "/ws", "tenant-abc")

	// New VerifyBridgeContext must accept this (backward compat fallback inserts "" as tenantID slot)
	if !VerifyBridgeContext(key, "agent1", "user1", "telegram", "chat1", "direct", "/ws", oldSig, "tenant-abc") {
		t.Error("expected true for backward compat with old tenantID-signed sessions (tenantID as first extra)")
	}
}
