package store

import "context"

// SystemConfigStore manages system-wide configuration settings.
// Non-secret, plain-text key-value pairs. Use ConfigSecretsStore for secrets.
//
// In v4 single-user world there is no tenant scoping; all entries share the
// same system scope (no fallback logic needed).
type SystemConfigStore interface {
	// Get returns the config value for the given key.
	Get(ctx context.Context, key string) (string, error)
	// Set stores a config value.
	Set(ctx context.Context, key, value string) error
	// Delete removes a config value for the current tenant.
	Delete(ctx context.Context, key string) error
	// List returns all configs visible to the current tenant (master merged with tenant overrides).
	List(ctx context.Context) (map[string]string, error)
}
