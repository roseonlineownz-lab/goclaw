package auth

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/nextlevelbuilder/goclaw/internal/i18n"
	"github.com/nextlevelbuilder/goclaw/internal/permissions"
)

// minHMACKeyBytes is the minimum HMAC key length in bytes for HS256.
// RFC 7518 §3.2 mandates ≥256 bits (32 bytes); shorter keys weaken the MAC.
const minHMACKeyBytes = 32

// Claims represents JWT access-token claims embedded in every issued token.
type Claims struct {
	Sub  string          `json:"sub"`
	Role permissions.Role `json:"role"`
	jwt.RegisteredClaims
}

// jwtKey is one entry in the keyset.
type jwtKey struct {
	Kid    string `json:"kid"`
	Secret []byte `json:"-"`
	Status string `json:"status"` // "active" | "verify-only"
}

// jwtKeyRaw is used only for JSON unmarshalling of GOCLAW_JWT_SECRETS_JSON.
type jwtKeyRaw struct {
	Kid    string `json:"kid"`
	Secret string `json:"secret"` // hex-encoded
	Status string `json:"status"`
}

// JWTKeyset holds one or more signing keys indexed by kid.
// Thread-safe via RWMutex; hot-reloadable via Reload() on SIGHUP.
type JWTKeyset struct {
	mu   sync.RWMutex
	keys []jwtKey
}

// NewJWTKeyset reads the keyset from environment variables and returns a ready
// JWTKeyset.  At least one key must be present.
//
// Primary source: GOCLAW_JWT_SECRETS_JSON — JSON array of key objects.
// Fallback:       GOCLAW_JWT_SECRET — single hex secret; assigned kid="legacy"
//
//	and status="active" for upgrade-window compatibility.
func NewJWTKeyset() (*JWTKeyset, error) {
	ks := &JWTKeyset{}
	if err := ks.load(); err != nil {
		return nil, err
	}
	return ks, nil
}

// Reload re-reads environment variables. Call on SIGHUP. Thread-safe.
func (ks *JWTKeyset) Reload() error {
	keys, err := loadKeysFromEnv()
	if err != nil {
		return err
	}
	ks.mu.Lock()
	ks.keys = keys
	ks.mu.Unlock()
	return nil
}

// Issue signs a new JWT for the given Claims using the newest active key.
// The JWT header will contain a "kid" field. ttl controls ExpiresAt.
func (ks *JWTKeyset) Issue(claims Claims, ttl time.Duration) (string, error) {
	ks.mu.RLock()
	key, err := ks.newestActive()
	ks.mu.RUnlock()
	if err != nil {
		return "", err
	}

	now := time.Now()
	claims.RegisteredClaims = jwt.RegisteredClaims{
		Issuer:    "goclaw",
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
	}

	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tok.Header["kid"] = key.Kid
	return tok.SignedString(key.Secret)
}

// Verify parses and validates a JWT, returning the embedded Claims.
// Returns an error wrapping i18n.MsgAccessTokenInvalid for unknown kid or
// tampered tokens, and i18n.MsgAccessTokenExpired for expired tokens.
// Rejects alg=none unconditionally.
func (ks *JWTKeyset) Verify(token string) (*Claims, error) {
	var claims Claims

	tok, err := jwt.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
		// Reject alg=none and any non-HMAC algorithm.
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("%s: unexpected signing method %q", i18n.MsgAccessTokenInvalid, t.Header["alg"])
		}

		kidVal, ok := t.Header["kid"]
		if !ok {
			return nil, errors.New(i18n.MsgAccessTokenInvalid)
		}
		kid, ok := kidVal.(string)
		if !ok || kid == "" {
			return nil, errors.New(i18n.MsgAccessTokenInvalid)
		}

		ks.mu.RLock()
		secret, found := ks.secretByKid(kid)
		ks.mu.RUnlock()
		if !found {
			return nil, errors.New(i18n.MsgAccessTokenInvalid)
		}
		return secret, nil
	})

	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, errors.New(i18n.MsgAccessTokenExpired)
		}
		return nil, fmt.Errorf("%s: %w", i18n.MsgAccessTokenInvalid, err)
	}
	if !tok.Valid {
		return nil, errors.New(i18n.MsgAccessTokenInvalid)
	}
	return &claims, nil
}

// IssueAccess is a convenience wrapper that builds Claims from userID + role
// and calls ks.Issue. ttl is typically read from GOCLAW_JWT_ACCESS_TTL by
// the caller (default 15 min).
func IssueAccess(ks *JWTKeyset, userID string, role permissions.Role, ttl time.Duration) (string, error) {
	return ks.Issue(Claims{Sub: userID, Role: role}, ttl)
}

// VerifyAccess is a convenience wrapper around ks.Verify.
func VerifyAccess(ks *JWTKeyset, token string) (*Claims, error) {
	return ks.Verify(token)
}

// --- internal helpers ---

func (ks *JWTKeyset) load() error {
	keys, err := loadKeysFromEnv()
	if err != nil {
		return err
	}
	ks.keys = keys
	return nil
}

func loadKeysFromEnv() ([]jwtKey, error) {
	raw := os.Getenv("GOCLAW_JWT_SECRETS_JSON")
	if raw != "" {
		return parseSecretsJSON(raw)
	}
	// Legacy single-secret fallback (upgrade window).
	legacy := os.Getenv("GOCLAW_JWT_SECRET")
	if legacy != "" {
		secret, err := hex.DecodeString(legacy)
		if err != nil {
			return nil, fmt.Errorf("auth: GOCLAW_JWT_SECRET must be hex-encoded: %w", err)
		}
		if len(secret) < minHMACKeyBytes {
			return nil, fmt.Errorf("auth: GOCLAW_JWT_SECRET too short (got %d bytes, need ≥%d for HS256)", len(secret), minHMACKeyBytes)
		}
		return []jwtKey{{Kid: "legacy", Secret: secret, Status: "active"}}, nil
	}
	return nil, errors.New("auth: no JWT keys configured — set GOCLAW_JWT_SECRETS_JSON or GOCLAW_JWT_SECRET")
}

func parseSecretsJSON(raw string) ([]jwtKey, error) {
	var entries []jwtKeyRaw
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, fmt.Errorf("auth: parse GOCLAW_JWT_SECRETS_JSON: %w", err)
	}
	if len(entries) == 0 {
		return nil, errors.New("auth: GOCLAW_JWT_SECRETS_JSON contains no keys")
	}
	keys := make([]jwtKey, 0, len(entries))
	for _, e := range entries {
		secret, err := hex.DecodeString(e.Secret)
		if err != nil {
			return nil, fmt.Errorf("auth: decode secret for kid=%q: %w", e.Kid, err)
		}
		if len(secret) < minHMACKeyBytes {
			return nil, fmt.Errorf("auth: kid=%q secret too short (got %d bytes, need ≥%d for HS256)", e.Kid, len(secret), minHMACKeyBytes)
		}
		if e.Status != "active" && e.Status != "verify-only" {
			return nil, fmt.Errorf("auth: kid=%q has invalid status %q (must be active or verify-only)", e.Kid, e.Status)
		}
		keys = append(keys, jwtKey{Kid: e.Kid, Secret: secret, Status: e.Status})
	}
	return keys, nil
}

// newestActive returns the last key in the slice with status="active".
// Callers hold at least an RLock.
func (ks *JWTKeyset) newestActive() (jwtKey, error) {
	for i := len(ks.keys) - 1; i >= 0; i-- {
		if ks.keys[i].Status == "active" {
			return ks.keys[i], nil
		}
	}
	return jwtKey{}, errors.New("auth: no active JWT key in keyset")
}

// secretByKid returns the HMAC secret for kid. Callers hold at least an RLock.
func (ks *JWTKeyset) secretByKid(kid string) ([]byte, bool) {
	for _, k := range ks.keys {
		if k.Kid == kid {
			return k.Secret, true
		}
	}
	return nil, false
}
