// Package auth provides password hashing, JWT signing, and refresh-token
// management for the GoClaw gateway.
//
// RAM budget note for ops teams: each concurrent Argon2id verify allocates
// ~64 MB. The default semaphore size (N=10) caps peak at 640 MB headroom
// above the gateway baseline. Override with GOCLAW_PASSWORD_VERIFY_CONCURRENCY
// (range 4–32) if the host has a different memory profile.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"os"
	"strconv"
	"unicode"

	"golang.org/x/crypto/argon2"

	"github.com/nextlevelbuilder/goclaw/internal/i18n"
)

// Argon2id parameters — fixed per Q-C decision. NOT env-tunable.
const (
	argonTime    = 3
	argonMemory  = 64 * 1024 // 65536 KiB = 64 MiB
	argonThreads = 4
	argonKeyLen  = 32
	argonSaltLen = 16
)

// verifySem limits concurrent Argon2id calls to cap memory usage.
// Initialized in init() from GOCLAW_PASSWORD_VERIFY_CONCURRENCY (default 10,
// clamped to [4, 32]). Peak memory = 64 MB × N.
var verifySem chan struct{}

func init() {
	n := 10
	if v := os.Getenv("GOCLAW_PASSWORD_VERIFY_CONCURRENCY"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil {
			switch {
			case parsed < 4:
				n = 4
			case parsed > 32:
				n = 32
			default:
				n = parsed
			}
		}
	}
	verifySem = make(chan struct{}, n)
}

// HashPassword hashes plaintext using Argon2id and returns a PHC-encoded string:
//
//	$argon2id$v=19$m=65536,t=3,p=4$<base64-salt>$<base64-hash>
//
// Both base64 segments use standard encoding without padding.
func HashPassword(plaintext string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: generate salt: %w", err)
	}
	hash := argon2.IDKey([]byte(plaintext), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return encodePHC(salt, hash), nil
}

// VerifyPassword checks plaintext against an Argon2id PHC-encoded hash.
// Returns (false, nil) for wrong password; non-nil error only for parse or
// crypto failures. Does not panic on corrupt input.
//
// Acquires the process-level semaphore before running Argon2id so that no more
// than N concurrent verify calls run simultaneously (N set by
// GOCLAW_PASSWORD_VERIFY_CONCURRENCY, default 10).
func VerifyPassword(plaintext, encodedHash string) (bool, error) {
	salt, expected, err := parsePHC(encodedHash)
	if err != nil {
		return false, err
	}

	// Acquire semaphore before the expensive Argon2id call.
	verifySem <- struct{}{}
	defer func() { <-verifySem }()

	actual := argon2.IDKey([]byte(plaintext), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	if subtle.ConstantTimeCompare(actual, expected) != 1 {
		return false, nil
	}
	return true, nil
}

// ValidatePasswordComplexity returns an error if plaintext does not meet the
// minimum policy: ≥12 chars, ≥1 letter, ≥1 digit, ≥1 symbol (non-letter,
// non-digit, non-whitespace). The error message is the i18n key
// i18n.MsgWeakPassword; the HTTP layer translates it via i18n.T(locale, key).
func ValidatePasswordComplexity(plaintext string) error {
	if len(plaintext) < 12 {
		return errors.New(i18n.MsgWeakPassword)
	}
	var hasLetter, hasDigit, hasSymbol bool
	for _, r := range plaintext {
		switch {
		case unicode.IsLetter(r):
			hasLetter = true
		case unicode.IsDigit(r):
			hasDigit = true
		case !unicode.IsSpace(r):
			hasSymbol = true
		}
	}
	if !hasLetter || !hasDigit || !hasSymbol {
		return errors.New(i18n.MsgWeakPassword)
	}
	return nil
}
