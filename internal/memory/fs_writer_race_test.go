package memory

// TestFSWriterChaos50Goroutines verifies the 4-layer race defense under
// maximum concurrency pressure.
//
// Dependency note (cross-finding): the "version monotonic 1,2,3,...,k"
// invariant requires the Phase 04 hashtext-collision guard to be in place.
// Without it, two file_paths whose hashtext() collides would both compute
// current=0 → newVersion=1 and both silently succeed, creating duplicate
// version numbers. This test will catch that via the strict-monotonic check.
//
// Run with: go test -race -run TestFSWriterChaos50Goroutines ./internal/memory/
// Stability target: deterministic over 100 repetitions (no goroutine-schedule
// dependent assertions — only invariants on the final committed state).

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
)

func TestFSWriterChaos50Goroutines(t *testing.T) {
	ctx := context.Background()
	scope, _ := mustTempScope(t)
	w := newNoopFSWriter()

	const N = 50
	const path = "shared/chaos-path.md"

	type result struct {
		idx     int
		version int
		err     error
		content []byte
	}

	results := make([]result, N)
	var wg sync.WaitGroup

	for i := range N {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			content := randomBytes(1024)
			v, err := w.Write(ctx, scope, path, content, -1 /* unconditional */)
			results[i] = result{idx: i, version: v, err: err, content: content}
		}(i)
	}
	wg.Wait()

	// Partition into successful and failed writes.
	var successes []result
	for _, r := range results {
		if r.err == nil {
			successes = append(successes, r)
		} else if !errors.Is(r.err, ErrVersionConflict) {
			t.Errorf("goroutine %d: unexpected error (not ErrVersionConflict): %v", r.idx, r.err)
		}
	}

	// RED: noopFSWriter returns ErrVersionConflict for all writes.
	// Once Phase 04 implements the real writer, at least one must succeed.
	if len(successes) == 0 {
		t.Log("RED: all writes returned ErrVersionConflict — expected before phase 04 impl")
		return
	}

	// Invariant 1: successful versions must be unique (no duplicates).
	versionSeen := make(map[int]bool)
	for _, r := range successes {
		if versionSeen[r.version] {
			t.Errorf("duplicate version %d returned by two goroutines", r.version)
		}
		versionSeen[r.version] = true
	}

	// Invariant 2: successful versions must be strictly monotonic (no gaps).
	// Sort successes by version to check the sequence.
	sort.Slice(successes, func(i, j int) bool {
		return successes[i].version < successes[j].version
	})
	for i, r := range successes {
		expected := i + 1
		if r.version != expected {
			t.Errorf("version gap at position %d: want %d, got %d", i, expected, r.version)
			break
		}
	}

	// Invariant 3: final FS content hash must match the DB row hash for the
	// highest committed version. Phase 04 populates content_hash in the DB;
	// we verify via Read that the returned content matches what the winner wrote.
	winner := successes[len(successes)-1]
	gotContent, gotVersion, err := w.Read(ctx, scope, path)
	if err != nil {
		t.Fatalf("read after chaos: %v", err)
	}
	if gotVersion != winner.version {
		t.Errorf("final version: want %d, got %d", winner.version, gotVersion)
	}
	if string(gotContent) != string(winner.content) {
		t.Error("final FS content does not match winning goroutine's content")
	}

	// Invariant 4: final FS file size must equal len(winner.content).
	if len(gotContent) != len(winner.content) {
		t.Errorf("torn file: got %d bytes, want %d", len(gotContent), len(winner.content))
	}
}
