package auth

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/nextlevelbuilder/goclaw/internal/i18n"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
)

// testKeyset builds a JWTKeyset from a JSON env value without touching the
// real environment. It sets t.Setenv so cleanup is automatic.
func testKeysetFromJSON(t *testing.T, jsonVal string) *JWTKeyset {
	t.Helper()
	t.Setenv("GOCLAW_JWT_SECRETS_JSON", jsonVal)
	t.Setenv("GOCLAW_JWT_SECRET", "") // ensure legacy path is not taken
	ks, err := NewJWTKeyset()
	if err != nil {
		t.Fatalf("NewJWTKeyset: %v", err)
	}
	return ks
}

const singleActiveJSON = `[{"kid":"k1","secret":"0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20","status":"active"}]`

func TestIssueVerify_RoundTrip(t *testing.T) {
	ks := testKeysetFromJSON(t, singleActiveJSON)

	token, err := IssueAccess(ks, "user-uuid-1", permissions.RoleAdmin, 15*time.Minute)
	if err != nil {
		t.Fatalf("IssueAccess: %v", err)
	}

	claims, err := VerifyAccess(ks, token)
	if err != nil {
		t.Fatalf("VerifyAccess: %v", err)
	}
	if claims.Sub != "user-uuid-1" {
		t.Errorf("sub: got %q, want %q", claims.Sub, "user-uuid-1")
	}
	if claims.Role != permissions.RoleAdmin {
		t.Errorf("role: got %q, want %q", claims.Role, permissions.RoleAdmin)
	}
}

func TestVerify_ExpiredToken(t *testing.T) {
	ks := testKeysetFromJSON(t, singleActiveJSON)

	token, err := IssueAccess(ks, "user-uuid-1", permissions.RoleMember, -time.Nanosecond)
	if err != nil {
		t.Fatalf("IssueAccess: %v", err)
	}

	_, err = VerifyAccess(ks, token)
	if err == nil {
		t.Fatal("expected error for expired token, got nil")
	}
	if !strings.Contains(err.Error(), i18n.MsgAccessTokenExpired) {
		t.Errorf("expected %q in error, got %q", i18n.MsgAccessTokenExpired, err.Error())
	}
}

func TestVerify_TamperedSig(t *testing.T) {
	ks := testKeysetFromJSON(t, singleActiveJSON)

	token, err := IssueAccess(ks, "user-uuid-1", permissions.RoleViewer, 15*time.Minute)
	if err != nil {
		t.Fatalf("IssueAccess: %v", err)
	}

	// Flip last byte of the signature segment.
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("unexpected JWT structure")
	}
	sig := []byte(parts[2])
	sig[len(sig)-1] ^= 0xFF
	tampered := parts[0] + "." + parts[1] + "." + string(sig)

	_, err = VerifyAccess(ks, tampered)
	if err == nil {
		t.Fatal("expected error for tampered token, got nil")
	}
}

func TestKidRotation_BothActive(t *testing.T) {
	twoKeyJSON := `[
		{"kid":"k0","secret":"0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20","status":"verify-only"},
		{"kid":"k1","secret":"2122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f40","status":"active"}
	]`
	ks := testKeysetFromJSON(t, twoKeyJSON)

	// Issue under active key (k1).
	tokenK1, err := IssueAccess(ks, "u1", permissions.RoleRoot, 15*time.Minute)
	if err != nil {
		t.Fatalf("issue k1: %v", err)
	}
	// Verify k1 token before reload.
	if _, err := VerifyAccess(ks, tokenK1); err != nil {
		t.Fatalf("verify k1 before reload: %v", err)
	}

	// Simulate a manually created token signed under k0 (verify-only).
	secret0 := mustHex("0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20")
	tokenK0 := issueWithSecret(t, secret0, "k0", "u2", 15*time.Minute)
	if _, err := VerifyAccess(ks, tokenK0); err != nil {
		t.Fatalf("verify-only k0 token should still verify: %v", err)
	}

	// Reload (env already set with both keys — no change here, just confirming
	// the code path doesn't break).
	if err := ks.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if _, err := VerifyAccess(ks, tokenK1); err != nil {
		t.Fatalf("verify k1 after reload: %v", err)
	}
}

func TestVerify_UnknownKid(t *testing.T) {
	ks := testKeysetFromJSON(t, singleActiveJSON)

	secret := mustHex("aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899")
	ghostToken := issueWithSecret(t, secret, "ghost", "u1", 15*time.Minute)

	_, err := VerifyAccess(ks, ghostToken)
	if err == nil {
		t.Fatal("expected error for unknown kid, got nil")
	}
}

func TestLegacyFallback_GOCLAW_JWT_SECRET_only(t *testing.T) {
	t.Setenv("GOCLAW_JWT_SECRETS_JSON", "")
	t.Setenv("GOCLAW_JWT_SECRET", "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20")

	ks, err := NewJWTKeyset()
	if err != nil {
		t.Fatalf("NewJWTKeyset with legacy secret: %v", err)
	}

	token, err := IssueAccess(ks, "u-legacy", permissions.RoleMember, 5*time.Minute)
	if err != nil {
		t.Fatalf("IssueAccess: %v", err)
	}
	claims, err := VerifyAccess(ks, token)
	if err != nil {
		t.Fatalf("VerifyAccess: %v", err)
	}
	if claims.Sub != "u-legacy" {
		t.Errorf("sub mismatch: %q", claims.Sub)
	}
}

func TestReject_AlgNone(t *testing.T) {
	ks := testKeysetFromJSON(t, singleActiveJSON)

	// Build an unsigned "none" token manually.
	noneToken := "eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0" +
		".eyJzdWIiOiJ1MSIsInJvbGUiOiJhZG1pbiJ9" +
		"."

	_, err := VerifyAccess(ks, noneToken)
	if err == nil {
		t.Fatal("expected error for alg=none token, got nil")
	}
}

// --- helpers used by tests ---

// mustHex decodes a hex string, panicking on error (test setup only).
func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic("mustHex: " + err.Error())
	}
	return b
}

// issueWithSecret builds a JWT signed with an arbitrary secret + kid,
// bypassing the JWTKeyset (for rotation tests).
func issueWithSecret(t *testing.T, secret []byte, kid, sub string, ttl time.Duration) string {
	t.Helper()
	now := time.Now()
	c := Claims{
		Sub:  sub,
		Role: permissions.RoleMember,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "goclaw",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, c)
	tok.Header["kid"] = kid
	signed, err := tok.SignedString(secret)
	if err != nil {
		t.Fatalf("issueWithSecret: %v", err)
	}
	return signed
}
