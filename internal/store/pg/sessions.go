package pg

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"maps"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// PGSessionStore implements store.SessionStore backed by Postgres.
type PGSessionStore struct {
	db *sql.DB
	mu sync.RWMutex
	// In-memory cache for hot sessions (reduces DB reads during tool loops)
	cache map[string]*store.SessionData
	// OnDelete is called with the session key when a session is deleted.
	// Used for media file cleanup.
	OnDelete func(sessionKey string)
}

func NewPGSessionStore(db *sql.DB) *PGSessionStore {
	s := &PGSessionStore{
		db:    db,
		cache: make(map[string]*store.SessionData),
	}
	s.migrateLegacyWSKeys()
	s.migrateUUIDSessionKeys()
	return s
}

// migrateLegacyWSKeys renames old WS session keys from non-canonical format
// (agent:X:ws-userId-ts) to canonical format (agent:X:ws:direct:ts).
// The last hyphen-delimited segment is the base36 timestamp used as convId.
// Idempotent — no-op if no legacy keys exist.
func (s *PGSessionStore) migrateLegacyWSKeys() {
	res, err := s.db.ExecContext(context.Background(), `
		UPDATE agent_sessions
		SET session_key = regexp_replace(
			session_key,
			'^(agent:[^:]+):ws-.+-([^-]+)$',
			'\1:ws:direct:\2'
		)
		WHERE session_key ~ '^agent:[^:]+:ws-'
	`)
	if err != nil {
		slog.Warn("sessions.migrate_legacy_ws_keys", "error", err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		slog.Info("sessions.migrate_legacy_ws_keys", "migrated", n)
	}
}

// migrateUUIDSessionKeys fixes legacy heartbeat/cron session keys that used the agent's
// UUID instead of agentKey. The old format "agent:{UUID}:heartbeat" or "agent:{UUID}:cron:..."
// is replaced with "agent:{agentKey}:..." by JOINing with the agents table.
// Idempotent — no-op if no UUID-based keys exist.
func (s *PGSessionStore) migrateUUIDSessionKeys() {
	res, err := s.db.ExecContext(context.Background(), `
		UPDATE agent_sessions s
		SET session_key = 'agent:' || a.agent_key || ':' || split_part(s.session_key, ':', 3)
			|| CASE WHEN array_length(string_to_array(s.session_key, ':'), 1) > 3
				THEN ':' || (SELECT string_agg(part, ':') FROM unnest((string_to_array(s.session_key, ':'))[4:]) AS part)
				ELSE '' END
		FROM agents a
		WHERE s.session_key ~ '^agent:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}:'
		  AND a.id = (split_part(s.session_key, ':', 2))::uuid
		  AND a.deleted_at IS NULL
		  AND NOT EXISTS (
		    SELECT 1 FROM agent_sessions s2
		    WHERE s2.session_key = 'agent:' || a.agent_key || ':' || split_part(s.session_key, ':', 3)
		      || CASE WHEN array_length(string_to_array(s.session_key, ':'), 1) > 3
		          THEN ':' || (SELECT string_agg(p, ':') FROM unnest((string_to_array(s.session_key, ':'))[4:]) AS p)
		          ELSE '' END
		  )
	`)
	if err != nil {
		slog.Warn("sessions.migrate_uuid_keys", "error", err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		slog.Info("sessions.migrate_uuid_keys", "migrated", n)
	}
}

// sessionCacheKey returns the cache key for a session.
func sessionCacheKey(_ context.Context, key string) string {
	return key
}

func (s *PGSessionStore) GetOrCreate(ctx context.Context, key string) *store.SessionData {
	s.mu.Lock()
	defer s.mu.Unlock()

	if cached, ok := s.cache[sessionCacheKey(ctx, key)]; ok {
		return cached
	}

	data := s.loadFromDB(ctx, key)
	if data != nil {
		s.cache[sessionCacheKey(ctx, key)] = data
		return data
	}

	// Create new
	now := time.Now()
	data = &store.SessionData{
		Key:      key,
		Messages: []providers.Message{},
		Created:  now,
		Updated:  now,
	}

	// Extract team_id from team session keys (agent:{agentId}:team:{teamId}:{chatId}).
	var teamID *uuid.UUID
	if parts := strings.SplitN(key, ":", 5); len(parts) >= 4 && parts[2] == "team" {
		if tid, err := uuid.Parse(parts[3]); err == nil {
			teamID = &tid
			data.TeamID = teamID
		}
	}
	s.cache[sessionCacheKey(ctx, key)] = data

	msgsJSON, _ := json.Marshal([]providers.Message{})
	s.db.ExecContext(ctx,
		`INSERT INTO agent_sessions (id, session_key, messages, created_at, updated_at, team_id, project_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT (session_key) DO NOTHING`,
		uuid.Must(uuid.NewV7()), key, msgsJSON, now, now, teamID, nil,
	)

	return data
}

// Get returns the session if it exists (cache or DB), nil otherwise. Never creates.
func (s *PGSessionStore) Get(ctx context.Context, key string) *store.SessionData {
	s.mu.RLock()
	if cached, ok := s.cache[sessionCacheKey(ctx, key)]; ok {
		s.mu.RUnlock()
		return cached
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()

	// Double-check after acquiring write lock
	if cached, ok := s.cache[sessionCacheKey(ctx, key)]; ok {
		return cached
	}

	data := s.loadFromDB(ctx, key)
	if data != nil {
		s.cache[sessionCacheKey(ctx, key)] = data
	}
	return data
}

func (s *PGSessionStore) AddMessage(ctx context.Context, key string, msg providers.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Stamp message creation time if not already set.
	if msg.CreatedAt == nil {
		now := time.Now().UTC()
		msg.CreatedAt = &now
	}

	data := s.getOrInit(ctx, key)
	data.Messages = append(data.Messages, msg)
	data.Updated = time.Now()
}

func (s *PGSessionStore) GetHistory(ctx context.Context, key string) []providers.Message {
	s.mu.RLock()
	if data, ok := s.cache[sessionCacheKey(ctx, key)]; ok {
		msgs := make([]providers.Message, len(data.Messages))
		copy(msgs, data.Messages)
		s.mu.RUnlock()
		return msgs
	}
	s.mu.RUnlock()

	// Not in cache — load from DB and cache it
	s.mu.Lock()
	defer s.mu.Unlock()

	// Double-check after acquiring write lock
	if data, ok := s.cache[sessionCacheKey(ctx, key)]; ok {
		msgs := make([]providers.Message, len(data.Messages))
		copy(msgs, data.Messages)
		return msgs
	}

	data := s.loadFromDB(ctx, key)
	if data == nil {
		return nil
	}
	s.cache[sessionCacheKey(ctx, key)] = data
	msgs := make([]providers.Message, len(data.Messages))
	copy(msgs, data.Messages)
	return msgs
}

func (s *PGSessionStore) GetSummary(ctx context.Context, key string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if data, ok := s.cache[sessionCacheKey(ctx, key)]; ok {
		return data.Summary
	}
	return ""
}

func (s *PGSessionStore) SetSummary(ctx context.Context, key, summary string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if data, ok := s.cache[sessionCacheKey(ctx, key)]; ok {
		data.Summary = summary
		data.Updated = time.Now()
	}
}

func (s *PGSessionStore) GetLabel(ctx context.Context, key string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if data, ok := s.cache[sessionCacheKey(ctx, key)]; ok {
		return data.Label
	}
	return ""
}

func (s *PGSessionStore) SetLabel(ctx context.Context, key, label string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if data, ok := s.cache[sessionCacheKey(ctx, key)]; ok {
		data.Label = label
		data.Updated = time.Now()
	}
}

func (s *PGSessionStore) GetSessionMetadata(ctx context.Context, key string) map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if data, ok := s.cache[sessionCacheKey(ctx, key)]; ok && data.Metadata != nil {
		out := make(map[string]string, len(data.Metadata))
		maps.Copy(out, data.Metadata)
		return out
	}
	return nil
}

func (s *PGSessionStore) SetSessionMetadata(ctx context.Context, key string, metadata map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data := s.getOrInit(ctx, key)
	if data.Metadata == nil {
		data.Metadata = make(map[string]string)
	}
	maps.Copy(data.Metadata, metadata)
	data.Updated = time.Now()
}

func (s *PGSessionStore) SetAgentInfo(ctx context.Context, key string, agentUUID uuid.UUID, userID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data := s.getOrInit(ctx, key)
	if agentUUID != uuid.Nil {
		data.AgentUUID = agentUUID
	}
	if userID != "" {
		data.UserID = userID
	}
}

func (s *PGSessionStore) UpdateMetadata(ctx context.Context, key, model, provider, channel string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if data, ok := s.cache[sessionCacheKey(ctx, key)]; ok {
		if model != "" {
			data.Model = model
		}
		if provider != "" {
			data.Provider = provider
		}
		if channel != "" {
			data.Channel = channel
		}
	}
}

// --- helpers ---

func (s *PGSessionStore) getOrInit(ctx context.Context, key string) *store.SessionData {
	if data, ok := s.cache[sessionCacheKey(ctx, key)]; ok {
		return data
	}

	// Try loading from DB first to avoid overwriting existing messages
	data := s.loadFromDB(ctx, key)
	if data != nil {
		s.cache[sessionCacheKey(ctx, key)] = data
		return data
	}

	// Not in DB — create new
	now := time.Now()
	data = &store.SessionData{
		Key:      key,
		Messages: []providers.Message{},
		Created:  now,
		Updated:  now,
	}
	s.cache[sessionCacheKey(ctx, key)] = data

	msgsJSON, _ := json.Marshal([]providers.Message{})
	s.db.ExecContext(ctx,
		`INSERT INTO agent_sessions (id, session_key, messages, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5) ON CONFLICT (session_key) DO NOTHING`,
		uuid.Must(uuid.NewV7()), key, msgsJSON, now, now,
	)
	return data
}

func (s *PGSessionStore) loadFromDB(ctx context.Context, key string) *store.SessionData {
	var sessionKey string
	var msgsJSON []byte
	var summary, model, provider, channel, label, spawnedBy *string
	var agentID, userID, teamID, projectID *uuid.UUID
	var inputTokens, outputTokens int64
	var compactionCount, memoryFlushCompactionCount, spawnDepth int
	var memoryFlushAt int64
	var createdAt, updatedAt time.Time
	var metaJSON *[]byte

	err := s.db.QueryRowContext(ctx,
		`SELECT session_key, messages, summary, model, provider, channel,
		 input_tokens, output_tokens, compaction_count,
		 memory_flush_compaction_count, memory_flush_at,
		 label, spawned_by, spawn_depth, agent_id, user_id,
		 COALESCE(metadata, '{}'), created_at, updated_at, team_id, project_id
		 FROM agent_sessions WHERE session_key = $1`, key,
	).Scan(&sessionKey, &msgsJSON, &summary, &model, &provider, &channel,
		&inputTokens, &outputTokens, &compactionCount,
		&memoryFlushCompactionCount, &memoryFlushAt,
		&label, &spawnedBy, &spawnDepth, &agentID, &userID,
		&metaJSON, &createdAt, &updatedAt, &teamID, &projectID)
	if err != nil {
		return nil
	}

	var msgs []providers.Message
	json.Unmarshal(msgsJSON, &msgs)

	var meta map[string]string
	if metaJSON != nil {
		json.Unmarshal(*metaJSON, &meta)
	}

	// Restore adaptive-throttle fields from metadata so GetLastPromptTokens()
	// returns the persisted value after a server restart (clean cache).
	var lastPromptTokens, lastMessageCount int
	if meta != nil {
		if v := meta["last_prompt_tokens"]; v != "" {
			lastPromptTokens, _ = strconv.Atoi(v)
		}
		if v := meta["last_message_count"]; v != "" {
			lastMessageCount, _ = strconv.Atoi(v)
		}
	}

	return &store.SessionData{
		Key:                        sessionKey,
		Messages:                   msgs,
		Summary:                    derefStr(summary),
		Created:                    createdAt,
		Updated:                    updatedAt,
		AgentUUID:                  derefUUID(agentID),
		UserID:                     uuidPtrToStr(userID),
		TeamID:                     teamID,
		ProjectID:                  projectID,
		Model:                      derefStr(model),
		Provider:                   derefStr(provider),
		Channel:                    derefStr(channel),
		InputTokens:                inputTokens,
		OutputTokens:               outputTokens,
		CompactionCount:            compactionCount,
		MemoryFlushCompactionCount: memoryFlushCompactionCount,
		MemoryFlushAt:              memoryFlushAt,
		Label:                      derefStr(label),
		SpawnedBy:                  derefStr(spawnedBy),
		SpawnDepth:                 spawnDepth,
		Metadata:                   meta,
		LastPromptTokens:           lastPromptTokens,
		LastMessageCount:           lastMessageCount,
	}
}

func nilSessionUUID(u uuid.UUID) *uuid.UUID {
	if u == uuid.Nil {
		return nil
	}
	return &u
}

// uuidPtrToStr converts a nullable UUID pointer to its string representation.
// Returns empty string for nil (no user_id set).
func uuidPtrToStr(u *uuid.UUID) string {
	if u == nil {
		return ""
	}
	return u.String()
}
