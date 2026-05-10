//go:build sqlite || sqliteonly

package sqlitestore

import (
	"encoding/json"
	"log/slog"

	"github.com/nextlevelbuilder/goclaw/internal/crypto"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// decryptServerFields decrypts api_key, headers, and env after scan.
func (s *SQLiteMCPServerStore) decryptServerFields(srv *store.MCPServerData) {
	srv.Headers = s.decryptJSON(srv.Headers)
	srv.Env = s.decryptJSON(srv.Env)
	if srv.APIKey != "" && s.encKey != "" {
		if decrypted, err := crypto.Decrypt(srv.APIKey, s.encKey); err == nil {
			srv.APIKey = decrypted
		} else {
			slog.Warn("mcp: failed to decrypt api key", "server", srv.Name, "error", err)
		}
	}
}

// encryptJSON encrypts a JSON blob by wrapping ciphertext as a JSON string.
// Unencrypted: {"key":"val"} (JSON object). Encrypted: "aes-gcm:..." (JSON string).
func (s *SQLiteMCPServerStore) encryptJSON(data []byte) []byte {
	if s.encKey == "" || len(data) == 0 || string(data) == "{}" || string(data) == "null" {
		return data
	}
	enc, err := crypto.Encrypt(string(data), s.encKey)
	if err != nil {
		slog.Warn("mcp: failed to encrypt json", "error", err)
		return data
	}
	wrapped, _ := json.Marshal(enc)
	return wrapped
}

// decryptJSON decrypts a JSON blob if it is an encrypted JSON string.
func (s *SQLiteMCPServerStore) decryptJSON(data []byte) []byte {
	if s.encKey == "" || len(data) == 0 || data[0] != '"' {
		return data
	}
	var encStr string
	if json.Unmarshal(data, &encStr) != nil {
		return data
	}
	dec, err := crypto.Decrypt(encStr, s.encKey)
	if err != nil {
		slog.Warn("mcp: failed to decrypt json", "error", err)
		return data
	}
	return []byte(dec)
}
