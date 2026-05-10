package pg

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/crypto"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

func (s *PGSecureCLIStore) GetUserCredentials(ctx context.Context, binaryID uuid.UUID, userID string) (*store.SecureCLIUserCredential, error) {
	var uc store.SecureCLIUserCredential
	var env []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT id, binary_id, user_id, encrypted_env, metadata, created_at, updated_at
		 FROM secure_cli_user_credentials
		 WHERE binary_id = $1 AND user_id = $2`,
		binaryID, userID,
	).Scan(&uc.ID, &uc.BinaryID, &uc.UserID, &env, &uc.Metadata, &uc.CreatedAt, &uc.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// Decrypt env.
	if len(env) > 0 && s.encKey != "" {
		if decrypted, err := crypto.Decrypt(string(env), s.encKey); err == nil {
			uc.EncryptedEnv = []byte(decrypted)
		}
	} else {
		uc.EncryptedEnv = env
	}
	return &uc, nil
}

func (s *PGSecureCLIStore) SetUserCredentials(ctx context.Context, binaryID uuid.UUID, userID string, encryptedEnv []byte) error {
	// Encrypt env.
	var envBytes []byte
	if len(encryptedEnv) > 0 && s.encKey != "" {
		encrypted, err := crypto.Encrypt(string(encryptedEnv), s.encKey)
		if err != nil {
			return fmt.Errorf("encrypt env: %w", err)
		}
		envBytes = []byte(encrypted)
	} else {
		envBytes = encryptedEnv
	}

	now := time.Now()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO secure_cli_user_credentials (binary_id, user_id, encrypted_env, metadata, created_at, updated_at)
		 VALUES ($1, $2, $3, '{}', $4, $4)
		 ON CONFLICT (binary_id, user_id) DO UPDATE SET
		   encrypted_env = EXCLUDED.encrypted_env,
		   updated_at = EXCLUDED.updated_at`,
		binaryID, userID, envBytes, now,
	)
	return err
}

func (s *PGSecureCLIStore) DeleteUserCredentials(ctx context.Context, binaryID uuid.UUID, userID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM secure_cli_user_credentials WHERE binary_id = $1 AND user_id = $2`,
		binaryID, userID,
	)
	return err
}

func (s *PGSecureCLIStore) ListUserCredentials(ctx context.Context, binaryID uuid.UUID) ([]store.SecureCLIUserCredential, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, binary_id, user_id, encrypted_env, metadata, created_at, updated_at
		 FROM secure_cli_user_credentials
		 WHERE binary_id = $1
		 ORDER BY created_at`, binaryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []store.SecureCLIUserCredential
	for rows.Next() {
		var uc store.SecureCLIUserCredential
		var env []byte
		if err := rows.Scan(&uc.ID, &uc.BinaryID, &uc.UserID, &env, &uc.Metadata, &uc.CreatedAt, &uc.UpdatedAt); err != nil {
			return nil, err
		}
		if len(env) > 0 && s.encKey != "" {
			if decrypted, err := crypto.Decrypt(string(env), s.encKey); err == nil {
				uc.EncryptedEnv = []byte(decrypted)
			}
		} else {
			uc.EncryptedEnv = env
		}
		result = append(result, uc)
	}
	return result, rows.Err()
}
