package pg

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

const permCacheTTL = 60 * time.Second

// permCacheEntry holds cached permission rows for an agent+user pair.
type permCacheEntry struct {
	rows    []permRow
	fetched time.Time
}

type permRow struct {
	Scope      string `json:"scope" db:"scope"`
	ConfigType string `json:"config_type" db:"config_type"`
	Permission string `json:"permission" db:"permission"`
	UserID     string `json:"user_id" db:"user_id"` // individual user ID or "*" (group wildcard)
}

// writerCacheEntry holds cached ConfigPermission rows for a scope+configType pair.
type writerCacheEntry struct {
	rows    []store.ConfigPermission
	fetched time.Time
}

// PGConfigPermissionStore implements store.ConfigPermissionStore backed by Postgres.
// Includes a TTL cache for CheckPermission to avoid per-request DB queries.
type PGConfigPermissionStore struct {
	db        *sql.DB
	mu        sync.RWMutex
	cache     map[string]permCacheEntry   // key: "agentID:userID"
	writerMu  sync.RWMutex
	writerCac map[string]writerCacheEntry // key: "agentID:scope:configType"

	grantHooksMu sync.RWMutex
	grantHooks   []func(agentID uuid.UUID) // called after Grant/Revoke with the affected agentID
}

func NewPGConfigPermissionStore(db *sql.DB) *PGConfigPermissionStore {
	return &PGConfigPermissionStore{
		db:        db,
		cache:     make(map[string]permCacheEntry),
		writerCac: make(map[string]writerCacheEntry),
	}
}

// RegisterGrantHook registers a callback invoked after each Grant or Revoke with
// the affected agentID. Used to invalidate derivative caches (e.g. glob cache)
// without polling the 60s TTL.
func (s *PGConfigPermissionStore) RegisterGrantHook(fn func(agentID uuid.UUID)) {
	s.grantHooksMu.Lock()
	s.grantHooks = append(s.grantHooks, fn)
	s.grantHooksMu.Unlock()
}

func (s *PGConfigPermissionStore) runGrantHooks(agentID uuid.UUID) {
	s.grantHooksMu.RLock()
	hooks := s.grantHooks
	s.grantHooksMu.RUnlock()
	for _, fn := range hooks {
		fn(agentID)
	}
}

// InvalidateCache clears all cached permission entries.
func (s *PGConfigPermissionStore) InvalidateCache() {
	s.mu.Lock()
	s.cache = make(map[string]permCacheEntry)
	s.mu.Unlock()

	s.writerMu.Lock()
	s.writerCac = make(map[string]writerCacheEntry)
	s.writerMu.Unlock()
}

// CheckPermission evaluates deny-first, allow-second permission with Go-level wildcard matching.
func (s *PGConfigPermissionStore) CheckPermission(ctx context.Context, agentID uuid.UUID, scope, configType, userID string) (bool, error) {
	cacheKey := agentID.String() + ":" + userID

	// Check cache.
	s.mu.RLock()
	if entry, ok := s.cache[cacheKey]; ok && time.Since(entry.fetched) < permCacheTTL {
		s.mu.RUnlock()
		return evalPermRows(entry.rows, scope, configType, userID), nil
	}
	s.mu.RUnlock()

	// Fetch from DB.
	var permRows []permRow
	err := pkgSqlxDB.SelectContext(ctx, &permRows,
		`SELECT scope, config_type, permission, user_id FROM agent_config_permissions
		 WHERE agent_id = $1 AND (user_id = $2 OR user_id = '*')`,
		agentID, userID,
	)
	if err != nil {
		return false, err
	}

	// Update cache.
	s.mu.Lock()
	s.cache[cacheKey] = permCacheEntry{rows: permRows, fetched: time.Now()}
	s.mu.Unlock()

	return evalPermRows(permRows, scope, configType, userID), nil
}

// evalPermRows evaluates cached permission rows against scope and configType.
// Priority-based evaluation: individual permissions override group wildcards (user_id="*").
//
//  1. Individual DENY  → REJECT (highest priority)
//  2. Individual ALLOW → ACCEPT
//  3. Group (*) DENY   → REJECT
//  4. Group (*) ALLOW  → ACCEPT
//  5. No match         → REJECT (default)
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

	// Individual takes priority over group
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
// Pattern examples: "*", "group:*", "group:telegram:*", "group:telegram:-100456"
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

func (s *PGConfigPermissionStore) Grant(ctx context.Context, perm *store.ConfigPermission) error {
	meta := perm.Metadata
	if meta == nil {
		meta = json.RawMessage("{}")
	}
	denyGlobs := perm.DenyGlobs
	if denyGlobs == nil {
		denyGlobs = store.DefaultDenyGlobs
	}
	now := time.Now()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_config_permissions
		   (agent_id, scope, config_type, user_id, permission, granted_by, metadata, deny_globs, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9)
		 ON CONFLICT (agent_id, scope, config_type, user_id) DO UPDATE SET
		        permission  = EXCLUDED.permission,
		        granted_by  = EXCLUDED.granted_by,
		        metadata    = EXCLUDED.metadata,
		        deny_globs  = EXCLUDED.deny_globs,
		        updated_at  = EXCLUDED.updated_at`,
		perm.AgentID, perm.Scope, perm.ConfigType, perm.UserID, perm.Permission, perm.GrantedBy, meta,
		pq.Array(denyGlobs), now,
	)
	if err == nil {
		s.InvalidateCache()
		s.runGrantHooks(perm.AgentID)
	}
	return err
}

func (s *PGConfigPermissionStore) Revoke(ctx context.Context, agentID uuid.UUID, scope, configType, userID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM agent_config_permissions WHERE agent_id = $1 AND scope = $2 AND config_type = $3 AND user_id = $4`,
		agentID, scope, configType, userID,
	)
	if err == nil {
		s.InvalidateCache()
		s.runGrantHooks(agentID)
	}
	return err
}

func (s *PGConfigPermissionStore) List(ctx context.Context, agentID uuid.UUID, configType, scope string) ([]store.ConfigPermission, error) {
	query := `SELECT id, agent_id, scope, config_type, user_id, permission, granted_by, metadata, deny_globs, created_at, updated_at
	          FROM agent_config_permissions WHERE agent_id = $1`
	args := []any{agentID}

	if configType != "" {
		args = append(args, configType)
		query += ` AND config_type = $` + itoa(len(args))
	}
	if scope != "" {
		args = append(args, scope)
		query += ` AND scope = $` + itoa(len(args))
	}
	query += ` ORDER BY created_at`

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
func (s *PGConfigPermissionStore) ListWriters(ctx context.Context, agentID uuid.UUID, scope string, configType string) ([]store.ConfigPermission, error) {
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
		 WHERE agent_id = $1 AND config_type = $2 AND scope = $3 AND permission = 'allow'
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

// GetDenyGlobs returns the deduplicated union of deny_globs across all grant rows
// matching (agentID, scope, userID). Returns the baseline default list when no row matches.
func (s *PGConfigPermissionStore) GetDenyGlobs(ctx context.Context, agentID uuid.UUID, scope, userID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT deny_globs FROM agent_config_permissions
		 WHERE agent_id = $1 AND (user_id = $2 OR user_id = '*') AND scope = $3 AND permission = 'allow'`,
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
		var globs pq.StringArray
		if err := rows.Scan(&globs); err != nil {
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
		var globs pq.StringArray
		if err := rows.Scan(
			&p.ID, &p.AgentID, &p.Scope, &p.ConfigType, &p.UserID, &p.Permission, &p.GrantedBy, &metadata, &globs, &p.CreatedAt, &p.UpdatedAt,
		); err != nil {
			return nil, err
		}
		p.Metadata = metadata
		p.DenyGlobs = []string(globs)
		perms = append(perms, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return perms, nil
}

// itoa converts an int to its decimal string representation.
func itoa(n int) string {
	return strconv.Itoa(n)
}
