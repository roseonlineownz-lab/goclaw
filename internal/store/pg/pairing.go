package pg

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

const (
	codeAlphabet         = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	codeLength           = 8
	codeTTL              = 60 * time.Minute
	pairedDeviceTTL      = 30 * 24 * time.Hour // 30 days
	maxPendingPerAccount = 3
)

// PGPairingStore implements store.PairingStore backed by Postgres.
type PGPairingStore struct {
	db        *sql.DB
	onRequest func(code, senderID, channel, chatID string)
}

func NewPGPairingStore(db *sql.DB) *PGPairingStore {
	return &PGPairingStore{db: db}
}

// SetOnRequest sets a callback fired after a new pairing request is created.
func (s *PGPairingStore) SetOnRequest(cb func(code, senderID, channel, chatID string)) {
	s.onRequest = cb
}

func (s *PGPairingStore) RequestPairing(ctx context.Context, senderID, channel, chatID, accountID string, metadata map[string]string) (string, error) {
	// Prune expired
	s.db.ExecContext(ctx, "DELETE FROM pairing_requests WHERE expires_at < $1", time.Now())

	// Check max pending
	var count int64
	s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM pairing_requests WHERE account_id = $1", accountID).Scan(&count)
	if count >= maxPendingPerAccount {
		return "", fmt.Errorf("max pending pairing requests (%d) exceeded", maxPendingPerAccount)
	}

	// Check existing
	var existingCode string
	err := s.db.QueryRowContext(ctx, "SELECT code FROM pairing_requests WHERE sender_id = $1 AND channel = $2", senderID, channel).Scan(&existingCode)
	if err == nil {
		return existingCode, nil
	}

	metaJSON := []byte("{}")
	if len(metadata) > 0 {
		metaJSON, _ = json.Marshal(metadata)
	}

	code := generatePairingCode()
	now := time.Now()
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO pairing_requests (id, code, sender_id, channel, chat_id, account_id, expires_at, created_at, metadata)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		uuid.Must(uuid.NewV7()), code, senderID, channel, chatID, accountID, now.Add(codeTTL), now, metaJSON,
	)
	if err != nil {
		return "", fmt.Errorf("create pairing request: %w", err)
	}
	if s.onRequest != nil {
		go s.onRequest(code, senderID, channel, chatID)
	}
	return code, nil
}

// ApprovePairing looks up by code (globally unique random token) and creates paired device.
func (s *PGPairingStore) ApprovePairing(ctx context.Context, code, approvedBy string) (*store.PairedDeviceData, error) {
	// Prune expired
	s.db.ExecContext(ctx, "DELETE FROM pairing_requests WHERE expires_at < $1", time.Now())

	var reqID uuid.UUID
	var senderID, channel, chatID string
	var metaJSON []byte
	err := s.db.QueryRowContext(ctx,
		"SELECT id, sender_id, channel, chat_id, COALESCE(metadata, '{}') FROM pairing_requests WHERE code = $1 AND expires_at > NOW()", code,
	).Scan(&reqID, &senderID, &channel, &chatID, &metaJSON)
	if err != nil {
		return nil, fmt.Errorf("pairing code %s not found or expired", code)
	}

	// Remove from pending
	s.db.ExecContext(ctx, "DELETE FROM pairing_requests WHERE id = $1", reqID)

	// Add to paired
	now := time.Now()
	expiresAt := now.Add(pairedDeviceTTL)
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO paired_devices (id, sender_id, channel, chat_id, paired_by, paired_at, metadata, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		uuid.Must(uuid.NewV7()), senderID, channel, chatID, approvedBy, now, metaJSON, expiresAt,
	)
	if err != nil {
		return nil, fmt.Errorf("create paired device: %w", err)
	}

	var meta map[string]string
	if len(metaJSON) > 0 {
		json.Unmarshal(metaJSON, &meta)
	}

	return &store.PairedDeviceData{
		SenderID: senderID,
		Channel:  channel,
		ChatID:   chatID,
		PairedAt: now.UnixMilli(),
		PairedBy: approvedBy,
		Metadata: meta,
	}, nil
}

func (s *PGPairingStore) DenyPairing(ctx context.Context, code string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM pairing_requests WHERE code = $1", code)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("pairing code %s not found or expired", code)
	}
	return nil
}

func (s *PGPairingStore) RevokePairing(ctx context.Context, senderID, channel string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM paired_devices WHERE sender_id = $1 AND channel = $2", senderID, channel)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("paired device not found: %s/%s", channel, senderID)
	}
	return nil
}

// BindUser stamps user_id on an existing paired_devices row. v4 channels
// call this on first authenticated message so subsequent HTTP/WS access
// carries the resolved user scope.
//
// Idempotent: re-binding the same user is a no-op. Rejects (returns
// ErrPairingBoundToDifferentUser) if the device is already bound to a
// different user — prevents silent account hijack via SenderID collision
// across users.
func (s *PGPairingStore) BindUser(ctx context.Context, senderID, channel string, userID uuid.UUID) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE paired_devices SET user_id = $1
		   WHERE sender_id = $2 AND channel = $3
		     AND (user_id IS NULL OR user_id = $1)`,
		userID, senderID, channel,
	)
	if err != nil {
		return err
	}
	rows, _ := res.RowsAffected()
	if rows > 0 {
		return nil
	}
	// Row missing OR bound to different user — distinguish.
	var existing sql.NullString
	err = s.db.QueryRowContext(ctx,
		`SELECT user_id::text FROM paired_devices WHERE sender_id = $1 AND channel = $2`,
		senderID, channel,
	).Scan(&existing)
	if err != nil {
		return nil // no row at all — silent no-op (idempotent)
	}
	if existing.Valid && existing.String != "" && existing.String != userID.String() {
		return store.ErrPairingBoundToDifferentUser
	}
	return nil
}

func (s *PGPairingStore) IsPaired(ctx context.Context, senderID, channel string) (bool, error) {
	var count int64
	err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM paired_devices WHERE sender_id = $1 AND channel = $2 AND (expires_at IS NULL OR expires_at > NOW())",
		senderID, channel,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("pairing check query: %w", err)
	}
	return count > 0, nil
}

// pairingRequestRow is an sqlx scan struct for pairing_requests.
type pairingRequestRow struct {
	Code      string    `json:"code" db:"code"`
	SenderID  string    `json:"sender_id" db:"sender_id"`
	Channel   string    `json:"channel" db:"channel"`
	ChatID    string    `json:"chat_id" db:"chat_id"`
	AccountID string    `json:"account_id" db:"account_id"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
	ExpiresAt time.Time `json:"expires_at" db:"expires_at"`
	Metadata  []byte    `json:"metadata" db:"metadata"`
}

// pairedDeviceRow is an sqlx scan struct for paired_devices.
type pairedDeviceRow struct {
	SenderID string    `json:"sender_id" db:"sender_id"`
	Channel  string    `json:"channel" db:"channel"`
	ChatID   string    `json:"chat_id" db:"chat_id"`
	PairedBy string    `json:"paired_by" db:"paired_by"`
	PairedAt time.Time `json:"paired_at" db:"paired_at"`
	Metadata []byte    `json:"metadata" db:"metadata"`
}

func (s *PGPairingStore) ListPending(ctx context.Context) []store.PairingRequestData {
	// Prune expired
	s.db.ExecContext(ctx, "DELETE FROM pairing_requests WHERE expires_at < $1", time.Now())

	var rows []pairingRequestRow
	err := pkgSqlxDB.SelectContext(ctx, &rows,
		`SELECT code, sender_id, channel, chat_id, account_id, created_at, expires_at, COALESCE(metadata, '{}') AS metadata
		 FROM pairing_requests ORDER BY created_at DESC`)
	if err != nil {
		return []store.PairingRequestData{}
	}

	result := make([]store.PairingRequestData, len(rows))
	for i, r := range rows {
		result[i] = store.PairingRequestData{
			Code: r.Code, SenderID: r.SenderID, Channel: r.Channel,
			ChatID: r.ChatID, AccountID: r.AccountID,
			CreatedAt: r.CreatedAt.UnixMilli(), ExpiresAt: r.ExpiresAt.UnixMilli(),
		}
		if len(r.Metadata) > 0 {
			json.Unmarshal(r.Metadata, &result[i].Metadata)
		}
	}
	return result
}

func (s *PGPairingStore) ListPaired(ctx context.Context) []store.PairedDeviceData {
	// Prune expired paired devices
	s.db.ExecContext(ctx, "DELETE FROM paired_devices WHERE expires_at IS NOT NULL AND expires_at < NOW()")

	var rows []pairedDeviceRow
	err := pkgSqlxDB.SelectContext(ctx, &rows,
		`SELECT sender_id, channel, chat_id, paired_by, paired_at, COALESCE(metadata, '{}') AS metadata
		 FROM paired_devices ORDER BY paired_at DESC`)
	if err != nil {
		return []store.PairedDeviceData{}
	}

	result := make([]store.PairedDeviceData, len(rows))
	for i, r := range rows {
		result[i] = store.PairedDeviceData{
			SenderID: r.SenderID, Channel: r.Channel, ChatID: r.ChatID,
			PairedBy: r.PairedBy, PairedAt: r.PairedAt.UnixMilli(),
		}
		if len(r.Metadata) > 0 {
			json.Unmarshal(r.Metadata, &result[i].Metadata)
		}
	}
	return result
}

func (s *PGPairingStore) MigrateGroupChatID(ctx context.Context, channel, oldChatID, newChatID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migrate tx: %w", err)
	}
	defer tx.Rollback()

	// 1. paired_devices: update sender_id and chat_id
	if _, err := tx.ExecContext(ctx,
		`UPDATE paired_devices
		 SET sender_id = REPLACE(sender_id, $1, $2),
		     chat_id = REPLACE(chat_id, $1, $2)
		 WHERE sender_id LIKE '%' || $1 || '%'
		   AND channel = $3`,
		oldChatID, newChatID, channel,
	); err != nil {
		return fmt.Errorf("migrate paired_devices: %w", err)
	}

	// 2. agent_sessions: update session_key and user_id
	if _, err := tx.ExecContext(ctx,
		`UPDATE agent_sessions
		 SET session_key = REPLACE(session_key, ':' || $1, ':' || $2),
		     user_id = REPLACE(user_id, ':' || $1, ':' || $2)
		 WHERE session_key LIKE '%:telegram:%:' || $1 || '%'`,
		oldChatID, newChatID,
	); err != nil {
		return fmt.Errorf("migrate agent_sessions: %w", err)
	}

	// 3. channel_contacts: update sender_id
	if _, err := tx.ExecContext(ctx,
		`UPDATE channel_contacts
		 SET sender_id = REPLACE(sender_id, $1, $2)
		 WHERE sender_id LIKE '%' || $1 || '%'
		   AND channel_type = 'telegram'`,
		oldChatID, newChatID,
	); err != nil {
		return fmt.Errorf("migrate channel_contacts: %w", err)
	}

	// 4. channel_pending_messages: update history_key
	if _, err := tx.ExecContext(ctx,
		`UPDATE channel_pending_messages
		 SET history_key = REPLACE(history_key, $1, $2)
		 WHERE history_key LIKE '%' || $1 || '%'
		   AND channel_name = $3`,
		oldChatID, newChatID, channel,
	); err != nil {
		return fmt.Errorf("migrate channel_pending_messages: %w", err)
	}

	return tx.Commit()
}

func generatePairingCode() string {
	b := make([]byte, codeLength)
	rand.Read(b)
	code := make([]byte, codeLength)
	for i := range code {
		code[i] = codeAlphabet[int(b[i])%len(codeAlphabet)]
	}
	return string(code)
}
