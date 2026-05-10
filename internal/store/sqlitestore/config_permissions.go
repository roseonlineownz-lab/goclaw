//go:build sqlite || sqliteonly

package sqlitestore

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

const permCacheTTL = 60 * time.Second

type permRow struct {
	Scope      string
	ConfigType string
	Permission string
	UserID     string
}

type permCacheEntry struct {
	rows    []permRow
	fetched time.Time
}

type writerCacheEntry struct {
	rows    []store.ConfigPermission
	fetched time.Time
}

// SQLiteConfigPermissionStore implements store.ConfigPermissionStore backed by SQLite.
// Includes a TTL cache for CheckPermission to avoid per-request DB queries.
type SQLiteConfigPermissionStore struct {
	db        *sql.DB
	mu        sync.RWMutex
	cache     map[string]permCacheEntry   // key: "agentID:userID"
	writerMu  sync.RWMutex
	writerCac map[string]writerCacheEntry // key: "agentID:scope:configType"

	grantHooksMu sync.RWMutex
	grantHooks   []func(agentID uuid.UUID) // called after Grant/Revoke with the affected agentID
}

func NewSQLiteConfigPermissionStore(db *sql.DB) *SQLiteConfigPermissionStore {
	return &SQLiteConfigPermissionStore{
		db:        db,
		cache:     make(map[string]permCacheEntry),
		writerCac: make(map[string]writerCacheEntry),
	}
}

// RegisterGrantHook registers a callback invoked after each Grant or Revoke with
// the affected agentID. Used to invalidate derivative caches (e.g. glob cache)
// without polling the 60s TTL.
func (s *SQLiteConfigPermissionStore) RegisterGrantHook(fn func(agentID uuid.UUID)) {
	s.grantHooksMu.Lock()
	s.grantHooks = append(s.grantHooks, fn)
	s.grantHooksMu.Unlock()
}

func (s *SQLiteConfigPermissionStore) runGrantHooks(agentID uuid.UUID) {
	s.grantHooksMu.RLock()
	hooks := s.grantHooks
	s.grantHooksMu.RUnlock()
	for _, fn := range hooks {
		fn(agentID)
	}
}

// InvalidateCache clears all cached permission entries.
func (s *SQLiteConfigPermissionStore) InvalidateCache() {
	s.mu.Lock()
	s.cache = make(map[string]permCacheEntry)
	s.mu.Unlock()

	s.writerMu.Lock()
	s.writerCac = make(map[string]writerCacheEntry)
	s.writerMu.Unlock()
}

func (s *SQLiteConfigPermissionStore) CheckPermission(ctx context.Context, agentID uuid.UUID, scope, configType, userID string) (bool, error) {
	cacheKey := agentID.String() + ":" + userID

	s.mu.RLock()
	if entry, ok := s.cache[cacheKey]; ok && time.Since(entry.fetched) < permCacheTTL {
		s.mu.RUnlock()
		return evalPermRows(entry.rows, scope, configType, userID), nil
	}
	s.mu.RUnlock()

	rows, err := s.db.QueryContext(ctx,
		`SELECT scope, config_type, permission, user_id FROM agent_config_permissions
		 WHERE agent_id = ? AND (user_id = ? OR user_id = '*')`,
		agentID, userID,
	)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	var permRows []permRow
	for rows.Next() {
		var r permRow
		if err := rows.Scan(&r.Scope, &r.ConfigType, &r.Permission, &r.UserID); err != nil {
			return false, err
		}
		permRows = append(permRows, r)
	}
	if err := rows.Err(); err != nil {
		return false, err
	}

	s.mu.Lock()
	s.cache[cacheKey] = permCacheEntry{rows: permRows, fetched: time.Now()}
	s.mu.Unlock()

	return evalPermRows(permRows, scope, configType, userID), nil
}

func (s *SQLiteConfigPermissionStore) Grant(ctx context.Context, perm *store.ConfigPermission) error {
	meta := perm.Metadata
	if meta == nil {
		meta = json.RawMessage("{}")
	}
	denyGlobs := perm.DenyGlobs
	if denyGlobs == nil {
		denyGlobs = store.DefaultDenyGlobs
	}
	globsJSON, err := json.Marshal(denyGlobs)
	if err != nil {
		globsJSON = []byte(`[".env*","secrets/**",".git/**","*.key","*.pem"]`)
	}
	now := time.Now()
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO agent_config_permissions
		   (agent_id, scope, config_type, user_id, permission, granted_by, metadata, deny_globs, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT (agent_id, scope, config_type, user_id) DO UPDATE SET
		        permission  = excluded.permission,
		        granted_by  = excluded.granted_by,
		        metadata    = excluded.metadata,
		        deny_globs  = excluded.deny_globs,
		        updated_at  = excluded.updated_at`,
		perm.AgentID, perm.Scope, perm.ConfigType, perm.UserID, perm.Permission, perm.GrantedBy, meta,
		string(globsJSON), now, now,
	)
	if err == nil {
		s.InvalidateCache()
		s.runGrantHooks(perm.AgentID)
	}
	return err
}

func (s *SQLiteConfigPermissionStore) Revoke(ctx context.Context, agentID uuid.UUID, scope, configType, userID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM agent_config_permissions WHERE agent_id = ? AND scope = ? AND config_type = ? AND user_id = ?`,
		agentID, scope, configType, userID,
	)
	if err == nil {
		s.InvalidateCache()
		s.runGrantHooks(agentID)
	}
	return err
}

func (s *SQLiteConfigPermissionStore) List(ctx context.Context, agentID uuid.UUID, configType, scope string) ([]store.ConfigPermission, error) {
	query := `SELECT id, agent_id, scope, config_type, user_id, permission, granted_by, metadata, deny_globs, created_at, updated_at
	          FROM agent_config_permissions WHERE agent_id = ?`
	args := []any{agentID}

	if configType != "" {
		query += " AND config_type = ?"
		args = append(args, configType)
	}
	if scope != "" {
		query += " AND scope = ?"
		args = append(args, scope)
	}

	query += " ORDER BY created_at"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanConfigPermissions(rows)
}

// ListWriters returns cached allow permissions for a given agentID+scope+configType.
// Hot-path: called during system prompt injection for every group message.
// Post-split semantic: callers pass ConfigTypeEditFile for the "writers" surface.
func (s *SQLiteConfigPermissionStore) ListWriters(ctx context.Context, agentID uuid.UUID, scope string, configType string) ([]store.ConfigPermission, error) {
	cacheKey := agentID.String() + ":" + scope + ":" + configType

	s.writerMu.RLock()
	if entry, ok := s.writerCac[cacheKey]; ok && time.Since(entry.fetched) < permCacheTTL {
		s.writerMu.RUnlock()
		return entry.rows, nil
	}
	s.writerMu.RUnlock()

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, agent_id, scope, config_type, user_id, permission, granted_by, metadata, deny_globs, created_at, updated_at
		 FROM agent_config_permissions
		 WHERE agent_id = ? AND config_type = ? AND scope = ? AND permission = 'allow'
		 ORDER BY created_at`,
		agentID, configType, scope,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	perms, err := scanConfigPermissions(rows)
	if err != nil {
		return nil, err
	}

	s.writerMu.Lock()
	s.writerCac[cacheKey] = writerCacheEntry{rows: perms, fetched: time.Now()}
	s.writerMu.Unlock()

	return perms, nil
}

// GetDenyGlobs returns the deduplicated union of deny_globs across all matching grant rows.
// Returns the baseline default list when no row matches.
func (s *SQLiteConfigPermissionStore) GetDenyGlobs(ctx context.Context, agentID uuid.UUID, scope, userID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT deny_globs FROM agent_config_permissions
		 WHERE agent_id = ? AND (user_id = ? OR user_id = '*') AND scope = ? AND permission = 'allow'`,
		agentID, userID, scope,
	)
	if err != nil {
		return store.DefaultDenyGlobs, nil // fail-open: return baseline
	}
	defer rows.Close()

	seen := make(map[string]struct{})
	for _, g := range store.DefaultDenyGlobs {
		seen[g] = struct{}{}
	}
	result := make([]string, len(store.DefaultDenyGlobs))
	copy(result, store.DefaultDenyGlobs)

	for rows.Next() {
		var globsJSON string
		if err := rows.Scan(&globsJSON); err != nil {
			continue
		}
		var globs []string
		if err := json.Unmarshal([]byte(globsJSON), &globs); err != nil {
			continue
		}
		for _, g := range globs {
			if _, dup := seen[g]; !dup {
				seen[g] = struct{}{}
				result = append(result, g)
			}
		}
	}
	return result, nil
}

func scanConfigPermissions(rows *sql.Rows) ([]store.ConfigPermission, error) {
	var perms []store.ConfigPermission
	for rows.Next() {
		var p store.ConfigPermission
		var metadata []byte
		var globsJSON string
		createdAt, updatedAt := scanTimePair()
		if err := rows.Scan(
			&p.ID, &p.AgentID, &p.Scope, &p.ConfigType, &p.UserID, &p.Permission, &p.GrantedBy, &metadata, &globsJSON, createdAt, updatedAt,
		); err != nil {
			return nil, err
		}
		p.CreatedAt = createdAt.Time
		p.UpdatedAt = updatedAt.Time
		p.Metadata = metadata
		if globsJSON != "" {
			_ = json.Unmarshal([]byte(globsJSON), &p.DenyGlobs)
		}
		perms = append(perms, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return perms, nil
}

// evalPermRows evaluates cached permission rows with priority-based evaluation.
// Individual permissions (matching targetUserID) override group wildcards (user_id="*").
func evalPermRows(rows []permRow, scope, configType, targetUserID string) bool {
	var individualDeny, individualAllow bool
	var groupDeny, groupAllow bool

	for _, r := range rows {
		if !matchWildcard(r.Scope, scope) || !matchWildcard(r.ConfigType, configType) {
			continue
		}
		if r.UserID == targetUserID {
			switch r.Permission {
			case "deny":
				individualDeny = true
			case "allow":
				individualAllow = true
			}
		} else if r.UserID == "*" {
			switch r.Permission {
			case "deny":
				groupDeny = true
			case "allow":
				groupAllow = true
			}
		}
	}

	if individualDeny {
		return false
	}
	if individualAllow {
		return true
	}
	if groupDeny {
		return false
	}
	return groupAllow
}

// matchWildcard performs simple wildcard matching for scope/config_type.
func matchWildcard(pattern, value string) bool {
	if pattern == "*" {
		return true
	}
	if strings.HasSuffix(pattern, ":*") {
		prefix := strings.TrimSuffix(pattern, "*")
		return strings.HasPrefix(value, prefix) || value == strings.TrimSuffix(prefix, ":")
	}
	return pattern == value
}

// ensure interface is satisfied at compile time
var _ store.ConfigPermissionStore = (*SQLiteConfigPermissionStore)(nil)
