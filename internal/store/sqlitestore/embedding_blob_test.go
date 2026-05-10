//go:build sqlite || sqliteonly

package sqlitestore

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"math/rand"
	"testing"

	_ "modernc.org/sqlite"
)

// TestEncodeDecodeHalfvec3072RoundTrip verifies that encoding a 3072-dim
// float32 slice to BLOB and decoding back produces values within float16
// precision (relative error ~0.1% for normal values).
func TestEncodeDecodeHalfvec3072RoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(42)) //nolint:gosec
	v := make([]float32, 3072)
	for i := range v {
		v[i] = float32(rng.NormFloat64())
	}

	blob, err := EncodeHalfvec3072(v)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if len(blob) != Halfvec3072Bytes {
		t.Fatalf("blob length: got %d, want %d", len(blob), Halfvec3072Bytes)
	}

	got, err := DecodeHalfvec3072(blob)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 3072 {
		t.Fatalf("decoded dims: got %d, want 3072", len(got))
	}

	// Verify float16 precision: relative error < 0.2% for non-zero values.
	var maxRelErr float64
	for i, orig := range v {
		if orig == 0 {
			continue
		}
		rel := math.Abs(float64(got[i]-orig)) / math.Abs(float64(orig))
		if rel > maxRelErr {
			maxRelErr = rel
		}
	}
	if maxRelErr > 0.01 { // 1% — generous bound; float16 is ~0.1% typical
		t.Errorf("max relative error %.4f exceeds 1%% threshold", maxRelErr)
	}
	t.Logf("halfvec round-trip: dims=3072, max_rel_error=%.6f", maxRelErr)
}

// TestEncodeHalfvec3072WrongDim verifies that encoding a vector with wrong
// dimension returns an error rather than silently producing a malformed BLOB.
func TestEncodeHalfvec3072WrongDim(t *testing.T) {
	_, err := EncodeHalfvec3072(make([]float32, 1536))
	if err == nil {
		t.Error("expected error for 1536-dim input, got nil")
	}
}

// TestDecodeHalfvec3072WrongSize verifies that decoding a BLOB with wrong
// byte count returns an error.
func TestDecodeHalfvec3072WrongSize(t *testing.T) {
	_, err := DecodeHalfvec3072(make([]byte, 1024))
	if err == nil {
		t.Error("expected error for wrong blob size, got nil")
	}
}

// TestL2NormKnownValues verifies L2Norm against analytically known results.
func TestL2NormKnownValues(t *testing.T) {
	cases := []struct {
		v    []float32
		want float64
	}{
		{[]float32{1, 0, 0}, 1.0},
		{[]float32{0, 1, 0}, 1.0},
		{[]float32{3, 4}, 5.0},
		{[]float32{1, 1, 1, 1}, 2.0},
	}
	for _, tc := range cases {
		got := L2Norm(tc.v)
		if math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("L2Norm(%v) = %.9f, want %.9f", tc.v, got, tc.want)
		}
	}
}

// TestCosineSimilarityOrthogonal verifies that orthogonal vectors have
// cosine similarity 0 and parallel vectors have similarity 1.
func TestCosineSimilarityOrthogonal(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{0, 1, 0}
	normA := L2Norm(a)
	normB := L2Norm(b)

	sim := CosineSimilarity(a, normA, b, normB)
	if math.Abs(sim) > 1e-9 {
		t.Errorf("orthogonal vectors: similarity = %.9f, want 0", sim)
	}

	c := []float32{2, 0, 0}
	normC := L2Norm(c)
	sim2 := CosineSimilarity(a, normA, c, normC)
	if math.Abs(sim2-1.0) > 1e-9 {
		t.Errorf("parallel vectors: similarity = %.9f, want 1.0", sim2)
	}
}

// TestCosineTopKBasic inserts 5 vectors into a temp SQLite table and verifies
// that CosineTopK returns the correct nearest neighbor.
func TestCosineTopKBasic(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	_, err = db.Exec(`CREATE TABLE test_chunks (
		id TEXT PRIMARY KEY,
		agent_id TEXT,
		embedding BLOB,
		embedding_norm REAL
	)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}

	// Insert 5 vectors: first 4 are nearly orthogonal one-hot bases,
	// 5th is close to vector 0 (dominates dim 0).
	insert := func(id string, v []float32) {
		t.Helper()
		blob, _ := EncodeHalfvec3072(v)
		norm := L2Norm(v)
		_, err := db.Exec(`INSERT INTO test_chunks (id, agent_id, embedding, embedding_norm) VALUES (?, 'a1', ?, ?)`,
			id, blob, norm)
		if err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}

	makeUnit := func(hotDim int) []float32 {
		v := make([]float32, 3072)
		v[hotDim] = 1.0
		return v
	}

	insert("v0", makeUnit(0))
	insert("v1", makeUnit(1))
	insert("v2", makeUnit(2))
	insert("v3", makeUnit(3))

	// Insert a vector that is clearly orthogonal to dim 0 (activates dim 1 only).
	insert("dim1only", makeUnit(1))

	// Query with dim-0 basis vector. The top-2 most similar are v0 (exact match,
	// distance ~0) and any other dim-0-aligned vector. The least similar is v1/v2/v3
	// (pure dim-1/2/3, cosine=0 with dim-0 query, distance=1).
	query := makeUnit(0)
	hits, err := CosineTopK(context.Background(), db, "test_chunks",
		ScopeFilter{Clause: "agent_id = ?", Args: []any{"a1"}},
		query, 1)
	if err != nil {
		t.Fatalf("CosineTopK: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(hits))
	}
	// v0 is the only exact dim-0 unit vector — must be Top-1.
	if hits[0].ID != "v0" {
		t.Errorf("Top-1 ID: got %q, want %q", hits[0].ID, "v0")
	}
	// Exact match: distance must be very close to 0 (within float16 precision).
	if hits[0].Distance > 0.005 {
		t.Errorf("Top-1 distance: got %.4f, want ~0 (exact match)", hits[0].Distance)
	}
	t.Logf("Top-1: %v", hits)
}

// TestCosineTopKRestartSafety verifies that embeddings survive DB close/reopen.
func TestCosineTopKRestartSafety(t *testing.T) {
	dir := t.TempDir()
	dbPath := dir + "/test.db"

	write := func() {
		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer db.Close()
		db.Exec(`CREATE TABLE IF NOT EXISTS chunks (id TEXT PRIMARY KEY, embedding BLOB, embedding_norm REAL, agent_id TEXT)`) //nolint:errcheck

		rng := rand.New(rand.NewSource(7)) //nolint:gosec
		for i := 0; i < 10; i++ {
			v := make([]float32, 3072)
			for j := range v {
				v[j] = float32(rng.NormFloat64())
			}
			blob, _ := EncodeHalfvec3072(v)
			norm := L2Norm(v)
			db.Exec(`INSERT OR IGNORE INTO chunks VALUES (?, ?, ?, 'ag1')`, //nolint:errcheck
				fmt.Sprintf("row%d", i), blob, norm)
		}
	}

	read := func() int {
		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer db.Close()
		var n int
		db.QueryRow(`SELECT count(*) FROM chunks WHERE embedding IS NOT NULL`).Scan(&n) //nolint:errcheck
		return n
	}

	write()
	n := read()
	if n != 10 {
		t.Errorf("after restart: got %d rows with embeddings, want 10", n)
	}
	t.Logf("restart-safety: %d rows persisted", n)
}
