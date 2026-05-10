//go:build integration

package integration

import (
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"testing"
)

// TestHalfvec3072RoundTrip inserts a 3072-dim halfvec into memory_chunks.embedding
// and reads it back via SELECT, verifying the DB stores and returns the correct
// dimension without truncation or type errors.
func TestHalfvec3072RoundTrip(t *testing.T) {
	db := testDB(t)
	_, agentID := seedTenantAgent(t, db)

	// Generate a deterministic 3072-dim vector literal.
	vec := generate3072HalfvecLiteral(42)

	// Insert a memory_document to satisfy the FK.
	var docID string
	err := db.QueryRow(
		`INSERT INTO memory_documents (agent_id, path, file_path, content_hash, version)
		 VALUES ($1, 'test/roundtrip.md', 'test/roundtrip.md', 'aabbcc', 1)
		 RETURNING id`,
		agentID,
	).Scan(&docID)
	if err != nil {
		t.Fatalf("insert memory_document: %v", err)
	}
	t.Cleanup(func() {
		db.Exec("DELETE FROM memory_documents WHERE id = $1", docID)
	})

	// Insert a memory_chunk with the 3072-dim embedding.
	var chunkID string
	insertSQL := fmt.Sprintf(
		`INSERT INTO memory_chunks (document_id, agent_id, path, start_line, end_line, hash, text, embedding)
		 VALUES ($1, $2, 'test/roundtrip.md', 0, 1, 'aabbcc', 'hello world', '%s'::halfvec)
		 RETURNING id`,
		vec,
	)
	err = db.QueryRow(insertSQL, docID, agentID).Scan(&chunkID)
	if err != nil {
		t.Fatalf("insert memory_chunk with halfvec(3072): %v", err)
	}
	t.Cleanup(func() {
		db.Exec("DELETE FROM memory_chunks WHERE id = $1", chunkID)
	})

	// Read back the embedding and verify dimension.
	var embeddingStr string
	err = db.QueryRow(
		`SELECT embedding::text FROM memory_chunks WHERE id = $1`,
		chunkID,
	).Scan(&embeddingStr)
	if err != nil {
		t.Fatalf("select embedding: %v", err)
	}

	dims := countVectorDims(embeddingStr)
	if dims != 3072 {
		t.Errorf("round-trip dimension mismatch: got %d, want 3072", dims)
	}
	t.Logf("halfvec(3072) round-trip: chunk=%s dims=%d", chunkID, dims)
}

// TestHalfvec3072DimMismatchRejected verifies that inserting a 1536-dim vector
// into a halfvec(3072) column is rejected by PostgreSQL with a dimension error.
func TestHalfvec3072DimMismatchRejected(t *testing.T) {
	db := testDB(t)
	_, agentID := seedTenantAgent(t, db)

	var docID string
	err := db.QueryRow(
		`INSERT INTO memory_documents (agent_id, path, file_path, content_hash, version)
		 VALUES ($1, 'test/mismatch.md', 'test/mismatch.md', 'deadbeef', 1)
		 RETURNING id`,
		agentID,
	).Scan(&docID)
	if err != nil {
		t.Fatalf("insert memory_document: %v", err)
	}
	t.Cleanup(func() {
		db.Exec("DELETE FROM memory_documents WHERE id = $1", docID)
	})

	vec1536 := generate1536HalfvecLiteral(99)
	insertSQL := fmt.Sprintf(
		`INSERT INTO memory_chunks (document_id, agent_id, path, start_line, end_line, hash, text, embedding)
		 VALUES ($1, $2, 'test/mismatch.md', 0, 1, 'deadbeef', 'mismatch', '%s'::halfvec)`,
		vec1536,
	)
	_, err = db.Exec(insertSQL, docID, agentID)
	if err == nil {
		t.Error("expected dimension mismatch error inserting 1536-dim vector into halfvec(3072), got nil")
		db.Exec("DELETE FROM memory_chunks WHERE document_id = $1", docID)
		return
	}
	if !strings.Contains(err.Error(), "dimensions") {
		t.Errorf("expected dimension error, got: %v", err)
	}
	t.Logf("dimension mismatch correctly rejected: %v", err)
}

// generate3072HalfvecLiteral returns a pgvector-compatible "[f1,f2,...]" string
// with exactly 3072 float values generated deterministically from seed.
func generate3072HalfvecLiteral(seed int64) string {
	rng := rand.New(rand.NewSource(seed)) //nolint:gosec
	parts := make([]string, 3072)
	for i := range parts {
		parts[i] = strconv.FormatFloat(rng.NormFloat64(), 'f', 6, 32)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// generate1536HalfvecLiteral returns a 1536-dim literal for dim-mismatch tests.
func generate1536HalfvecLiteral(seed int64) string {
	rng := rand.New(rand.NewSource(seed)) //nolint:gosec
	parts := make([]string, 1536)
	for i := range parts {
		parts[i] = strconv.FormatFloat(rng.NormFloat64(), 'f', 6, 32)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// countVectorDims counts the number of comma-separated elements in a pgvector
// string like "[0.1,0.2,...]".
func countVectorDims(s string) int {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	if s == "" {
		return 0
	}
	return strings.Count(s, ",") + 1
}
