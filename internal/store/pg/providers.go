package pg

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/crypto"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// PGProviderStore implements store.ProviderStore backed by Postgres.
type PGProviderStore struct {
	db     *sql.DB
	encKey string // AES-256 encryption key for API keys (empty = plain text)
}

func NewPGProviderStore(db *sql.DB, encryptionKey string) *PGProviderStore {
	if encryptionKey != "" {
		slog.Info("provider store: API key encryption enabled")
	} else {
		slog.Warn("provider store: API key encryption disabled (plain text storage)")
	}
	return &PGProviderStore{db: db, encKey: encryptionKey}
}

func (s *PGProviderStore) CreateProvider(ctx context.Context, p *store.LLMProviderData) error {
	if p.ID == uuid.Nil {
		p.ID = store.GenNewID()
	}

	apiKey := p.APIKey
	if s.encKey != "" && apiKey != "" {
		encrypted, err := crypto.Encrypt(apiKey, s.encKey)
		if err != nil {
			return fmt.Errorf("encrypt api key: %w", err)
		}
		apiKey = encrypted
	}

	settings := p.Settings
	if len(settings) == 0 {
		settings = []byte("{}")
	}
	meta := p.Metadata
	if len(meta) == 0 {
		meta = []byte("{}")
	}

	now := time.Now()
	p.CreatedAt = now
	p.UpdatedAt = now
	// UPSERT: if provider with same name exists, update it and return its ID.
	// This handles orphaned providers left after agent deletion.
	var actualID uuid.UUID
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO llm_providers (id, name, display_name, provider_type, api_base, api_key, enabled, settings, metadata, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		 ON CONFLICT (name) DO UPDATE SET
			display_name = EXCLUDED.display_name, provider_type = EXCLUDED.provider_type,
			api_base = EXCLUDED.api_base, api_key = EXCLUDED.api_key,
			enabled = EXCLUDED.enabled, settings = EXCLUDED.settings, updated_at = EXCLUDED.updated_at
		 RETURNING id`,
		p.ID, p.Name, p.DisplayName, p.ProviderType, p.APIBase, apiKey, p.Enabled, settings, meta, now, now,
	).Scan(&actualID)
	if err == nil {
		p.ID = actualID // sync in-memory ID with actual DB row
	}
	return err
}

func (s *PGProviderStore) GetProvider(ctx context.Context, id uuid.UUID) (*store.LLMProviderData, error) {
	var p store.LLMProviderData
	err := pkgSqlxDB.GetContext(ctx, &p,
		`SELECT id, name, display_name, provider_type, api_base, api_key, enabled, settings, metadata, created_at, updated_at
		 FROM llm_providers WHERE id = $1`, id)
	if err != nil {
		return nil, fmt.Errorf("provider not found: %s", id)
	}
	p.APIKey = s.decryptKey(p.APIKey, p.Name)
	return &p, nil
}

func (s *PGProviderStore) GetProviderByName(ctx context.Context, name string) (*store.LLMProviderData, error) {
	var p store.LLMProviderData
	err := pkgSqlxDB.GetContext(ctx, &p,
		`SELECT id, name, display_name, provider_type, api_base, api_key, enabled, settings, metadata, created_at, updated_at
		 FROM llm_providers WHERE name = $1`, name)
	if err != nil {
		return nil, fmt.Errorf("provider not found: %s", name)
	}
	p.APIKey = s.decryptKey(p.APIKey, p.Name)
	return &p, nil
}

func (s *PGProviderStore) ListProviders(ctx context.Context) ([]store.LLMProviderData, error) {
	var result []store.LLMProviderData
	err := pkgSqlxDB.SelectContext(ctx, &result,
		`SELECT id, name, display_name, provider_type, api_base, api_key, enabled, settings, metadata, created_at, updated_at
		 FROM llm_providers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	for i := range result {
		result[i].APIKey = s.decryptKey(result[i].APIKey, result[i].Name)
	}
	return result, nil
}

// ListAllProviders returns all providers. Kept for interface compatibility.
func (s *PGProviderStore) ListAllProviders(ctx context.Context) ([]store.LLMProviderData, error) {
	return s.ListProviders(ctx)
}

func (s *PGProviderStore) UpdateProvider(ctx context.Context, id uuid.UUID, updates map[string]any) error {
	if apiKey, ok := updates["api_key"]; ok && s.encKey != "" {
		if keyStr, ok := apiKey.(string); ok && keyStr != "" {
			encrypted, err := crypto.Encrypt(keyStr, s.encKey)
			if err != nil {
				return fmt.Errorf("encrypt api key: %w", err)
			}
			updates["api_key"] = encrypted
		}
	}
	return execMapUpdate(ctx, s.db, "llm_providers", id, updates)
}

func (s *PGProviderStore) DeleteProvider(ctx context.Context, id uuid.UUID) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Disable heartbeats so the next scheduler tick after delete cannot fire stale config.
	// FK ON DELETE SET NULL clears provider_id auto.
	res, err := tx.ExecContext(ctx,
		"UPDATE agent_heartbeats SET enabled = false WHERE provider_id = $1", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		slog.Warn("heartbeat.provider_cleared",
			"provider_id", id, "heartbeats_disabled", n)
	}

	if _, err := tx.ExecContext(ctx, "DELETE FROM llm_providers WHERE id = $1", id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *PGProviderStore) decryptKey(apiKey, providerName string) string {
	if s.encKey != "" && apiKey != "" {
		decrypted, err := crypto.Decrypt(apiKey, s.encKey)
		if err != nil {
			slog.Warn("failed to decrypt provider API key", "provider", providerName, "error", err)
			return apiKey
		}
		return decrypted
	}
	return apiKey
}
