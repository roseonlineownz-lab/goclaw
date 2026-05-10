package pg

import (
	"encoding/json"
	"log/slog"

	"github.com/nextlevelbuilder/goclaw/internal/crypto"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// decryptServerFields decrypts api_key, headers, and env after sqlx scan.
func (s *PGMCPServerStore) decryptServerFields(srv *store.MCPServerData) {
	srv.Headers = s.decryptJSONB(srv.Headers)
	srv.Env = s.decryptJSONB(srv.Env)
	if srv.APIKey != "" && s.encKey != "" {
		if decrypted, err := crypto.Decrypt(srv.APIKey, s.encKey); err == nil {
			srv.APIKey = decrypted
		} else {
			slog.Warn("mcp: failed to decrypt api key", "server", srv.Name, "error", err)
		}
	}
}

// encryptJSONB encrypts a JSONB blob (env, headers) by converting it to a JSON string literal.
// Unencrypted: {"key":"val"} (JSONB object). Encrypted: "aes-gcm:..." (JSONB string).
func (s *PGMCPServerStore) encryptJSONB(data []byte) []byte {
	if s.encKey == "" || len(data) == 0 || string(data) == "{}" || string(data) == "null" {
		return data
	}
	enc, err := crypto.Encrypt(string(data), s.encKey)
	if err != nil {
		slog.Warn("mcp: failed to encrypt jsonb", "error", err)
		return data
	}
	// Wrap as JSON string so it's valid JSONB.
	wrapped, _ := json.Marshal(enc)
	return wrapped
}

// decryptJSONB decrypts a JSONB blob if it's an encrypted JSON string.
// Returns the original bytes if unencrypted (JSON object) or on error.
func (s *PGMCPServerStore) decryptJSONB(data []byte) []byte {
	if s.encKey == "" || len(data) == 0 || data[0] != '"' {
		return data // not a JSON string → unencrypted JSONB object
	}
	var encStr string
	if json.Unmarshal(data, &encStr) != nil {
		return data
	}
	dec, err := crypto.Decrypt(encStr, s.encKey)
	if err != nil {
		slog.Warn("mcp: failed to decrypt jsonb", "error", err)
		return data
	}
	return []byte(dec)
}
