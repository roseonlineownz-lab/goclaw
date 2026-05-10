package pg

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// PGSystemConfigStore implements store.SystemConfigStore backed by Postgres.
type PGSystemConfigStore struct {
	db *sql.DB
}

func NewPGSystemConfigStore(db *sql.DB) *PGSystemConfigStore {
	return &PGSystemConfigStore{db: db}
}

func (s *PGSystemConfigStore) Get(ctx context.Context, key string) (string, error) {
	var val string
	err := s.db.QueryRowContext(ctx,
		"SELECT value FROM system_configs WHERE key = $1",
		key,
	).Scan(&val)
	if err == nil {
		return val, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("system config get: %w", err)
	}
	return "", fmt.Errorf("system config not found: %s", key)
}

func (s *PGSystemConfigStore) Set(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO system_configs (key, value, updated_at)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = EXCLUDED.updated_at`,
		key, value, time.Now(),
	)
	return err
}

func (s *PGSystemConfigStore) Delete(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx,
		"DELETE FROM system_configs WHERE key = $1",
		key,
	)
	return err
}

func (s *PGSystemConfigStore) List(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT key, value FROM system_configs ORDER BY key",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("system config scan: %w", err)
		}
		result[k] = v
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("system config list: %w", err)
	}
	return result, nil
}
