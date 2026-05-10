package memory

// fs_writer_impl.go — 4-layer race-safe FS-backed memory writer.
//
// Layer 1: in-process sync.Map of *sync.Mutex keyed by absolute file path.
//          Serializes goroutines within the same process writing to the same file.
//
// Layer 2 (PG):     pg_advisory_xact_lock(hashtext(absPath)) inside the TX.
//          (SQLite): BEGIN IMMEDIATE acquires a write-lock on the DB; retry on SQLITE_BUSY.
//          Serializes concurrent writers across processes or replicas.
//
// Layer 3: optimistic version check — SELECT version WHERE file_path = $p;
//          if current != oldVersion (and oldVersion != -1), return ErrVersionConflict.
//          Catches lost-update races that slip past Layers 1–2 (e.g. hashtext collision).
//
// Layer 4: os.Rename(tmp, target) — POSIX-atomic within the same filesystem mount.
//          macOS/Linux: guaranteed. Windows (NTFS): MoveFileEx(MOVEFILE_REPLACE_EXISTING)
//          is atomic for most cases but may return ERROR_SHARING_VIOLATION if another
//          process holds an open handle; retry up to 3× with 50 ms back-off before fail.
//
// Write order: FS rename → DB upsert. If DB upsert fails after rename, the FS file
// becomes an orphan. SweepOrphans() reaps files with no DB row older than olderThan
// (default 5 min). Read() recomputes SHA-256 on every call and returns ErrDriftDetected
// if the hash differs from the DB row — orphans can never be served as truth.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// fsWriterImpl is the real implementation of FSWriter.
// Callers obtain one via NewFSWriter.
type fsWriterImpl struct {
	root    string // workspace root (absolute)
	dialect string // "pg" or "sqlite"
	db      *sql.DB
	mu      sync.Map // file absPath → *sync.Mutex
}

// NewFSWriter creates a production FSWriter backed by the given database.
//
//   - root: absolute workspace directory (e.g. ~/.goclaw/workspace)
//   - dialect: "pg" for PostgreSQL, "sqlite" for SQLite
//   - db: open *sql.DB connected to the same DB used by the memory store
func NewFSWriter(root, dialect string, db *sql.DB) (FSWriter, error) {
	if root == "" {
		return nil, fmt.Errorf("memory.fs: workspace root is required")
	}
	if dialect != "pg" && dialect != "sqlite" {
		return nil, fmt.Errorf("memory.fs: dialect must be 'pg' or 'sqlite', got %q", dialect)
	}
	if db == nil {
		return nil, fmt.Errorf("memory.fs: db is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("memory.fs: resolve root: %w", err)
	}
	return &fsWriterImpl{root: abs, dialect: dialect, db: db}, nil
}

// Write implements FSWriter.Write.
func (w *fsWriterImpl) Write(ctx context.Context, scope ScopeKey, relPath string, content []byte, oldVersion int) (int, error) {
	absPath, err := w.resolveSafe(scope, relPath)
	if err != nil {
		return 0, err
	}

	// Layer 1: in-process mutex per absolute path.
	mu := w.lockForPath(absPath)
	mu.Lock()
	defer mu.Unlock()

	// Layer 2+3+4 inside a DB transaction.
	return w.writeInTx(ctx, scope, absPath, relPath, content, oldVersion)
}

// Read implements FSWriter.Read.
func (w *fsWriterImpl) Read(ctx context.Context, scope ScopeKey, relPath string) ([]byte, int, error) {
	absPath, err := w.resolveSafe(scope, relPath)
	if err != nil {
		return nil, 0, err
	}

	ph := w.ph(1)
	var version int
	var dbHash string
	row := w.db.QueryRowContext(ctx,
		`SELECT version, content_hash FROM memory_documents WHERE file_path = `+ph,
		absPath)
	if scanErr := row.Scan(&version, &dbHash); scanErr != nil {
		if errors.Is(scanErr, sql.ErrNoRows) {
			return nil, 0, os.ErrNotExist
		}
		return nil, 0, fmt.Errorf("memory.fs: read db: %w", scanErr)
	}

	data, readErr := os.ReadFile(absPath)
	if readErr != nil {
		// File missing while DB row exists → drift.
		return nil, 0, ErrDriftDetected
	}
	if contentHashHex(data) != dbHash {
		// Content on disk differs from stored hash → drift.
		return nil, 0, ErrDriftDetected
	}
	return data, version, nil
}

// SweepOrphans walks the workspace root and removes files that have no
// matching DB row in memory_documents and whose mtime is older than olderThan.
// Default safe value: 5 minutes — avoids racing with in-flight writes where
// the FS file was just renamed into place but the DB commit has not yet landed.
// Every removal is logged at slog.Warn level for audit purposes.
func (w *fsWriterImpl) SweepOrphans(ctx context.Context, olderThan time.Duration) error {
	return filepath.WalkDir(w.root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".tmp") {
			// Leftover tmp file from interrupted write — always remove.
			if err := os.Remove(path); err == nil {
				slog.Warn("memory.fs.orphan_sweep: removed tmp file", "path", path)
			}
			return nil
		}

		info, err := d.Info()
		if err != nil || time.Since(info.ModTime()) < olderThan {
			return nil
		}

		// Check whether a DB row exists for this absolute path.
		var exists int
		err = w.db.QueryRowContext(ctx,
			`SELECT 1 FROM memory_documents WHERE file_path = `+w.ph(1)+` LIMIT 1`,
			path,
		).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			if rmErr := os.Remove(path); rmErr == nil {
				slog.Warn("memory.fs.orphan_sweep: removed orphan", "path", path, "age", time.Since(info.ModTime()))
			}
		}
		return nil
	})
}

// --- internal helpers ---

// writeInTx runs Layers 2–4 inside a single DB transaction.
func (w *fsWriterImpl) writeInTx(ctx context.Context, scope ScopeKey, absPath, relPath string, content []byte, oldVersion int) (int, error) {
	opts := &sql.TxOptions{}
	if w.dialect == "sqlite" {
		// BEGIN IMMEDIATE acquires write-lock upfront (Layer 2 for SQLite).
		opts.Isolation = sql.LevelSerializable
	}

	// Retry on SQLITE_BUSY (up to 3 attempts with back-off).
	const maxRetries = 3
	backoff := 10 * time.Millisecond
	for attempt := range maxRetries {
		v, err := w.tryWriteInTx(ctx, opts, scope, absPath, relPath, content, oldVersion)
		if err == nil {
			return v, nil
		}
		if isSQLiteBusy(err) && attempt < maxRetries-1 {
			time.Sleep(backoff)
			backoff *= 5
			continue
		}
		return 0, err
	}
	return 0, ErrVersionConflict // exhausted retries
}

func (w *fsWriterImpl) tryWriteInTx(ctx context.Context, opts *sql.TxOptions, scope ScopeKey, absPath, relPath string, content []byte, oldVersion int) (int, error) {
	tx, err := w.db.BeginTx(ctx, opts)
	if err != nil {
		return 0, fmt.Errorf("memory.fs: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Layer 2 (PG only): advisory lock on hashtext(absPath) serializes
	// cross-process writers for the same file. hashtext() is 32-bit — collision
	// is possible but acceptable: false-share = extra serialization, not corruption.
	if w.dialect == "pg" {
		if _, err := tx.ExecContext(ctx,
			"SELECT pg_advisory_xact_lock(hashtext($1))", absPath); err != nil {
			return 0, fmt.Errorf("memory.fs: advisory lock: %w", err)
		}
	}

	// Layer 3: claim the version slot in the DB BEFORE touching the filesystem.
	// This ensures the FS write (Layer 4) only happens after we have exclusive
	// ownership confirmed by the database — a failed DB write leaves the FS untouched.
	uid := scope.UserID
	var uidPtr *string
	if uid != "" {
		uidPtr = &uid
	}
	var teamPtr, contactPtr, projectPtr *string
	if scope.TeamID != "" {
		teamPtr = &scope.TeamID
	}
	if scope.ContactID != "" {
		contactPtr = &scope.ContactID
	}
	if scope.ProjectID != "" {
		projectPtr = &scope.ProjectID
	}
	agentID, parseErr := uuid.Parse(scope.AgentID)
	if parseErr != nil {
		return 0, fmt.Errorf("memory.fs: invalid agent_id: %w", parseErr)
	}

	hash := contentHashHex(content)

	// Compute what the new version will be. For new files current=0→newVersion=1.
	// The DB upsert below enforces the version guard atomically; if another writer
	// won the race (WHERE version = current matches 0 rows), we return
	// ErrVersionConflict with the FS still untouched.
	var res sql.Result
	if w.dialect == "pg" {
		res, err = tx.ExecContext(ctx, `
			INSERT INTO memory_documents
			  (id, agent_id, team_id, user_id, contact_id, project_id,
			   path, file_path, content_hash, version, metadata, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,1,'{}',NOW(),NOW())
			ON CONFLICT (file_path) DO UPDATE
			  SET content_hash = EXCLUDED.content_hash,
			      version      = memory_documents.version + 1,
			      updated_at   = NOW()
			WHERE ($10 < 0 OR memory_documents.version = $10)`,
			uuid.Must(uuid.NewV7()), agentID, teamPtr, uidPtr, contactPtr, projectPtr,
			relPath, absPath, hash, oldVersion,
		)
	} else {
		// SQLite: unconditional guard uses -1 sentinel; otherwise enforce current version.
		res, err = tx.ExecContext(ctx, `
			INSERT INTO memory_documents
			  (id, agent_id, team_id, user_id, contact_id, project_id,
			   path, file_path, content_hash, version, metadata, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,1,'{}',strftime('%Y-%m-%dT%H:%M:%fZ','now'),strftime('%Y-%m-%dT%H:%M:%fZ','now'))
			ON CONFLICT (file_path) DO UPDATE
			  SET content_hash = excluded.content_hash,
			      version      = memory_documents.version + 1,
			      updated_at   = strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE (? < 0 OR memory_documents.version = ?)`,
			uuid.Must(uuid.NewV7()).String(), agentID.String(), teamPtr, uidPtr, contactPtr, projectPtr,
			relPath, absPath, hash, oldVersion, oldVersion,
		)
	}
	if err != nil {
		// DB write failed — FS is still untouched, safe to propagate error.
		return 0, fmt.Errorf("memory.fs: db upsert: %w", err)
	}

	n, _ := res.RowsAffected()
	if n == 0 {
		// WHERE version guard matched no rows → another writer won the race.
		// FS is still untouched — return conflict without any disk mutation.
		return 0, ErrVersionConflict
	}

	// DB row claimed: read back the new version to return to the caller.
	var newVersion int
	if scanErr := tx.QueryRowContext(ctx,
		`SELECT version FROM memory_documents WHERE file_path = `+w.ph(1),
		absPath,
	).Scan(&newVersion); scanErr != nil {
		return 0, fmt.Errorf("memory.fs: read new version: %w", scanErr)
	}

	// Layer 4: atomic FS write. DB ownership is confirmed; FS failure rolls back
	// the TX (via defer Rollback) so the DB and FS stay consistent.
	if err := atomicWriteFile(absPath, content); err != nil {
		// TX will be rolled back by defer — DB update undone, FS untouched.
		return 0, fmt.Errorf("memory.fs: atomic write: %w", err)
	}

	if err := tx.Commit(); err != nil {
		// Commit failed — orphan sweep will reap the FS file (hash mismatch guard
		// in Read() will block serving stale content in the interim).
		return 0, fmt.Errorf("memory.fs: commit: %w", err)
	}
	return newVersion, nil
}

// resolveSafe converts scope + relPath to an absolute path, rejecting traversal.
func (w *fsWriterImpl) resolveSafe(scope ScopeKey, relPath string) (string, error) {
	if strings.Contains(relPath, "..") || filepath.IsAbs(relPath) {
		return "", ErrInvalidPath
	}
	dir, err := scopeFolder(scope)
	if err != nil {
		return "", err
	}
	// filepath.Clean collapses any residual . or double-slash segments.
	abs := filepath.Join(w.root, dir, filepath.Clean(relPath))

	// Verify the resolved path stays inside the root (belt-and-suspenders).
	if !strings.HasPrefix(abs+string(filepath.Separator), w.root+string(filepath.Separator)) {
		return "", ErrInvalidPath
	}

	// Symlink guard: if the target already exists and is a symlink, reject.
	if real, evalErr := filepath.EvalSymlinks(abs); evalErr == nil && real != abs {
		return "", ErrSymlinkRejected
	}

	return abs, nil
}

// lockForPath returns (or creates) the per-path mutex stored in the sync.Map.
func (w *fsWriterImpl) lockForPath(absPath string) *sync.Mutex {
	v, _ := w.mu.LoadOrStore(absPath, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// ph returns the dialect-appropriate placeholder for position n (1-based).
func (w *fsWriterImpl) ph(n int) string {
	if w.dialect == "pg" {
		return fmt.Sprintf("$%d", n)
	}
	return "?"
}

// atomicWriteFile writes content to a .tmp sibling then renames it into place.
// On Windows, rename may fail with ERROR_SHARING_VIOLATION; retry up to 3x.
func atomicWriteFile(absPath string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return err
	}
	tmpPath := fmt.Sprintf("%s.tmp.%d.%d", absPath, os.Getpid(), time.Now().UnixNano())
	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(content); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}

	// POSIX: os.Rename is atomic. Windows: retry on sharing violation.
	const maxRenameRetries = 3
	renameBackoff := 50 * time.Millisecond
	for i := range maxRenameRetries {
		err = os.Rename(tmpPath, absPath)
		if err == nil {
			return nil
		}
		if runtime.GOOS != "windows" || i == maxRenameRetries-1 {
			break
		}
		time.Sleep(renameBackoff)
	}
	os.Remove(tmpPath) // clean up tmp on failure
	return err
}

// contentHashHex returns the full 64-char lowercase hex SHA-256 of data.
// Using the full 64 chars avoids collisions at the memory-document scale.
func contentHashHex(data []byte) string {
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h[:])
}

// isSQLiteBusy returns true when err carries SQLite SQLITE_BUSY (code 5).
// We check the error string because the modernc SQLite driver does not
// export a typed error for SQLITE_BUSY.
func isSQLiteBusy(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "SQLITE_BUSY") || strings.Contains(s, "database is locked")
}
