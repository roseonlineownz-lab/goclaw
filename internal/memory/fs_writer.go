package memory

import (
	"context"
	"errors"
)

// ScopeKey identifies the 5D scope bucket for a file path.
// All five dimensions map to the DB row that owns the content.
// Fields that are not relevant for a given scope are left empty.
type ScopeKey struct {
	AgentID   string // required — always set
	TeamID    string // set for team-scoped writes
	UserID    string // set for user-scoped writes
	ContactID string // set for contact-scoped writes
	ProjectID string // set for project-scoped writes
}

// FSWriter provides 4-layer race-safe content persistence for memory documents.
//
// Layer 1: in-process sync.RWMutex keyed by file_path (same-process concurrency)
// Layer 2: pg_advisory_xact_lock(hashtext(path)) (cross-process / cross-replica)
// Layer 3: optimistic version check (WHERE version = $oldVersion)
// Layer 4: os.Rename(tmp→target) atomic FS write (prevents torn-file reads)
//
// SQLite substitute for Layer 2: BEGIN IMMEDIATE + retry on SQLITE_BUSY.
//
// Write with oldVersion=-1 is an unconditional overwrite (bypasses Layer 3).
// This is used by the chaos test and internal merge logic only; callers that
// perform a read-modify-write cycle MUST pass the version returned by Read.
//
// Phase 03 ships only this interface + sentinel errors; Phase 04 adds the impl.
type FSWriter interface {
	// Write persists content to the FS-backed path within the given scope.
	// Returns the new monotonically-increasing version, or ErrVersionConflict
	// if oldVersion doesn't match the current DB row (another writer won the race).
	// Pass oldVersion=-1 to bypass optimistic check (unconditional overwrite).
	Write(ctx context.Context, scope ScopeKey, relPath string, content []byte, oldVersion int) (newVersion int, err error)

	// Read retrieves the content and version for the given scope + path.
	// Returns ErrDriftDetected when the DB row exists but the FS file is missing
	// or its SHA-256 hash doesn't match content_hash stored in the DB.
	// Returns os.ErrNotExist when neither DB row nor FS file exists.
	Read(ctx context.Context, scope ScopeKey, relPath string) (content []byte, version int, err error)
}

// ErrVersionConflict is returned by FSWriter.Write when the optimistic version
// check fails — the DB row's current version differs from oldVersion, meaning
// another writer committed between this writer's Read and Write calls.
var ErrVersionConflict = errors.New("memory.fs: version conflict")

// ErrDriftDetected is returned by FSWriter.Read when the DB row exists but
// the FS file is absent or its SHA-256 hash does not match content_hash in
// the DB — indicating out-of-band FS mutation or a failed atomic rename.
var ErrDriftDetected = errors.New("memory.fs: db-fs hash mismatch")

// ErrInvalidPath is returned when relPath contains ".." segments or is absolute,
// which would allow escaping the workspace root (directory traversal).
var ErrInvalidPath = errors.New("memory.fs: invalid or unsafe path")

// ErrSymlinkRejected is returned when the resolved absolute path is a symlink.
// Following symlinks is rejected to prevent TOCTOU attacks where an attacker
// replaces the target between the resolution check and the actual file write.
var ErrSymlinkRejected = errors.New("memory.fs: symlink target rejected")
