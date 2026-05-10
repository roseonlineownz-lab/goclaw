package auth

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// encodePHC returns the PHC-encoded Argon2id string for the given salt and
// derived-key bytes. Base64 uses standard encoding without padding.
func encodePHC(salt, hash []byte) string {
	enc := base64.RawStdEncoding
	return fmt.Sprintf(
		"$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		argonMemory, argonTime, argonThreads,
		enc.EncodeToString(salt),
		enc.EncodeToString(hash),
	)
}

// parsePHC decodes a PHC-encoded Argon2id string produced by encodePHC.
// Returns the raw salt and hash bytes, or an error if the string is malformed.
// Does not panic on corrupt input — all failures return a descriptive error.
func parsePHC(encoded string) (salt, hash []byte, err error) {
	// Expected format:
	//   $argon2id$v=19$m=65536,t=3,p=4$<salt-b64>$<hash-b64>
	if !strings.HasPrefix(encoded, "$argon2id$") {
		return nil, nil, errors.New("auth: not an argon2id PHC string")
	}
	parts := strings.Split(encoded, "$")
	// parts[0]="" parts[1]="argon2id" parts[2]="v=19" parts[3]="m=...,t=...,p=..." parts[4]=salt parts[5]=hash
	if len(parts) != 6 {
		return nil, nil, fmt.Errorf("auth: malformed PHC string: expected 6 '$'-separated segments, got %d", len(parts))
	}
	enc := base64.RawStdEncoding
	salt, err = enc.DecodeString(parts[4])
	if err != nil {
		return nil, nil, fmt.Errorf("auth: decode PHC salt: %w", err)
	}
	hash, err = enc.DecodeString(parts[5])
	if err != nil {
		return nil, nil, fmt.Errorf("auth: decode PHC hash: %w", err)
	}
	return salt, hash, nil
}
