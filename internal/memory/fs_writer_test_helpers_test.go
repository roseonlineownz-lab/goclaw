package memory

import (
	"context"
	"crypto/rand"
	"math/big"
	"testing"
)

// randomBytes returns n cryptographically random bytes.
// Used by chaos tests to generate distinct content per goroutine.
func randomBytes(n int) []byte {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		// rand.Read should never fail; if it does, fill with counter bytes
		for i := range buf {
			buf[i] = byte(i)
		}
	}
	return buf
}

// randomInt returns a random int in [0, max).
func randomInt(max int) int {
	if max <= 0 {
		return 0
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(max)))
	if err != nil {
		return 0
	}
	return int(n.Int64())
}

// noopFSWriter is a nil-impl that returns ErrVersionConflict on every call.
// Used by tests that need a writer instance before Phase 04 ships the real impl.
type noopFSWriter struct{}

func newNoopFSWriter() FSWriter { return &noopFSWriter{} }

func (*noopFSWriter) Write(_ context.Context, _ ScopeKey, _ string, _ []byte, _ int) (int, error) {
	return 0, ErrVersionConflict
}

func (*noopFSWriter) Read(_ context.Context, _ ScopeKey, _ string) ([]byte, int, error) {
	return nil, 0, ErrDriftDetected
}

// mustTempScope returns a ScopeKey rooted at t.TempDir() for FS isolation.
// Each call to t.TempDir() produces a unique temp directory that is removed
// when the test (and all its sub-tests) complete.
func mustTempScope(t *testing.T) (ScopeKey, string) {
	t.Helper()
	dir := t.TempDir()
	return ScopeKey{AgentID: "test-agent"}, dir
}
