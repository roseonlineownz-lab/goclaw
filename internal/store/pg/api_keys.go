package pg

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// PGAPIKeyStore implements store.APIKeyStore using PostgreSQL.
type PGAPIKeyStore struct {
	db *sql.DB
}

// NewPGAPIKeyStore creates a new PostgreSQL-backed API key store.
func NewPGAPIKeyStore(db *sql.DB) *PGAPIKeyStore {
	return &PGAPIKeyStore{db: db}
}

func (s *PGAPIKeyStore) Create(ctx context.Context, key *store.APIKeyData) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO api_keys (id, name, prefix, key_hash, scopes, owner_user_id, expires_at, created_by, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		key.ID, key.Name, key.Prefix, key.KeyHash, pq.Array(key.Scopes),
		nilStr(key.OwnerID), key.ExpiresAt, nilStr(key.CreatedBy), key.CreatedAt, key.UpdatedAt,
	)
	return err
}

// Get fetches a key by ID without revoked/expired filtering.
// No ownership enforcement at store layer — callers must enforce their own rules.
func (s *PGAPIKeyStore) Get(ctx context.Context, id uuid.UUID) (*store.APIKeyData, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, prefix, key_hash, scopes, owner_user_id, expires_at, last_used_at, revoked, created_by, created_at, updated_at
		 FROM api_keys
		 WHERE id = $1`,
		id,
	)
	return scanAPIKey(row)
}

func (s *PGAPIKeyStore) GetByHash(ctx context.Context, keyHash string) (*store.APIKeyData, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, prefix, key_hash, scopes, owner_user_id, expires_at, last_used_at, revoked, created_by, created_at, updated_at
		 FROM api_keys
		 WHERE key_hash = $1 AND NOT revoked AND (expires_at IS NULL OR expires_at > now())`,
		keyHash,
	)
	return scanAPIKey(row)
}

func (s *PGAPIKeyStore) List(ctx context.Context, ownerID string) ([]store.APIKeyData, error) {
	var conditions []string
	var args []any
	argIdx := 1

	if ownerID != "" {
		conditions = append(conditions, fmt.Sprintf("owner_user_id = $%d", argIdx))
		args = append(args, ownerID)
		argIdx++
	}

	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, prefix, scopes, owner_user_id, expires_at, last_used_at, revoked, created_by, created_at, updated_at
		 FROM api_keys`+where+`
		 ORDER BY created_at DESC`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []store.APIKeyData
	for rows.Next() {
		var k store.APIKeyData
		var createdBy *string
		var ownerUID *string
		if err := rows.Scan(
			&k.ID, &k.Name, &k.Prefix, pq.Array(&k.Scopes),
			&ownerUID, &k.ExpiresAt, &k.LastUsedAt, &k.Revoked, &createdBy,
			&k.CreatedAt, &k.UpdatedAt,
		); err != nil {
			return nil, err
		}
		if createdBy != nil {
			k.CreatedBy = *createdBy
		}
		if ownerUID != nil {
			k.OwnerID = *ownerUID
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

func (s *PGAPIKeyStore) Revoke(ctx context.Context, id uuid.UUID, ownerID string) error {
	q := "UPDATE api_keys SET revoked = true, updated_at = $1 WHERE id = $2"
	args := []any{time.Now(), id}
	argIdx := 3

	if ownerID != "" {
		q += fmt.Sprintf(" AND owner_user_id = $%d", argIdx)
		args = append(args, ownerID)
	}

	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *PGAPIKeyStore) TouchLastUsed(ctx context.Context, id uuid.UUID) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE api_keys SET last_used_at = $2 WHERE id = $1`,
		id, time.Now(),
	)
	return err
}

// scanAPIKey scans a single api_keys row (with key_hash column present).
func scanAPIKey(row *sql.Row) (*store.APIKeyData, error) {
	var k store.APIKeyData
	var createdBy *string
	var ownerUID *string
	err := row.Scan(
		&k.ID, &k.Name, &k.Prefix, &k.KeyHash, pq.Array(&k.Scopes),
		&ownerUID, &k.ExpiresAt, &k.LastUsedAt, &k.Revoked, &createdBy,
		&k.CreatedAt, &k.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if createdBy != nil {
		k.CreatedBy = *createdBy
	}
	if ownerUID != nil {
		k.OwnerID = *ownerUID
	}
	return &k, nil
}
