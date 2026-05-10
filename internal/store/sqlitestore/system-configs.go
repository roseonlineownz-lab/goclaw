//go:build sqlite || sqliteonly

package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// SQLiteSystemConfigStore implements store.SystemConfigStore backed by SQLite.
type SQLiteSystemConfigStore struct {
	db *sql.DB
}

func NewSQLiteSystemConfigStore(db *sql.DB) *SQLiteSystemConfigStore {
	return &SQLiteSystemConfigStore{db: db}
}

func (s *SQLiteSystemConfigStore) Get(ctx context.Context, key string) (string, error) {
	var val string
	err := s.db.QueryRowContext(ctx,
		"SELECT value FROM system_configs WHERE key = ?", key,
	).Scan(&val)
	if err == nil {
		return val, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("system config not found: %s", key)
	}
	return "", fmt.Errorf("system config get: %w", err)
}

func (s *SQLiteSystemConfigStore) Set(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO system_configs (key, value, updated_at)
		 VALUES (?, ?, ?)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, time.Now(),
	)
	if err != nil {
		slog.Warn("system_config.set: failed", "key", key, "error", err)
	}
	return err
}

func (s *SQLiteSystemConfigStore) Delete(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx,
		"DELETE FROM system_configs WHERE key = ?", key,
	)
	return err
}

func (s *SQLiteSystemConfigStore) List(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT key, value FROM system_configs ORDER BY key")
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
