//go:build sqlite || sqliteonly

package sqlitestore

import (
	"container/heap"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
)

// ErrZeroNorm is returned when the query vector has zero norm (all-zero vector).
var ErrZeroNorm = errors.New("query vector has zero norm")

// Hit represents a single nearest-neighbor result from CosineTopK.
type Hit struct {
	ID       string
	Distance float32 // cosine distance = 1 - cosine_similarity; lower = more similar
}

// ScopeFilter holds a pre-built WHERE clause fragment + args for scoping
// cosine scans to a particular agent/user/team/contact/project combination.
// The clause must not include the leading "AND" — it is the complete WHERE body.
type ScopeFilter struct {
	Clause string // e.g. "agent_id = ? AND user_id IS NULL"
	Args   []any
}

// CosineTopK performs a linear cosine-similarity scan over a table's BLOB
// embedding column, returning the top-k nearest neighbors to query.
//
// The scan reads all rows matching scopeFilter that have a non-NULL embedding
// BLOB and precomputed norm, computes cosine distance in-memory, and returns
// the k smallest distances (most similar) in ascending distance order.
//
// table must be a trusted constant (no user input). scopeFilter.Args are
// passed as parameterized query arguments.
func CosineTopK(
	ctx context.Context,
	db *sql.DB,
	table string,
	scopeFilter ScopeFilter,
	query []float32,
	k int,
) ([]Hit, error) {
	qNorm := L2Norm(query)
	if qNorm == 0 {
		return nil, ErrZeroNorm
	}

	where := scopeFilter.Clause
	if where == "" {
		where = "1=1"
	}
	q := fmt.Sprintf(
		`SELECT id, embedding, embedding_norm FROM %s WHERE (%s) AND embedding IS NOT NULL AND embedding_norm IS NOT NULL AND embedding_norm > 0`,
		table, where,
	)
	rows, err := db.QueryContext(ctx, q, scopeFilter.Args...)
	if err != nil {
		return nil, fmt.Errorf("cosine scan %s: %w", table, err)
	}
	defer rows.Close()

	h := &hitMinHeap{}
	for rows.Next() {
		var id string
		var blob []byte
		var norm float64
		if err := rows.Scan(&id, &blob, &norm); err != nil {
			continue // skip corrupt rows
		}
		v, err := DecodeHalfvec3072(blob)
		if err != nil {
			continue // skip malformed blobs
		}
		cos := CosineSimilarity(query, qNorm, v, norm)
		dist := float32(1 - cos)
		if h.Len() < k {
			heap.Push(h, Hit{ID: id, Distance: dist})
		} else if h.Len() > 0 && dist < (*h)[0].Distance {
			heap.Pop(h)
			heap.Push(h, Hit{ID: id, Distance: dist})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cosine scan rows %s: %w", table, err)
	}

	// Extract hits and sort by distance ascending, tie-breaking on ID for
	// deterministic ordering across runs.
	results := make([]Hit, h.Len())
	for i := range results {
		results[i] = heap.Pop(h).(Hit)
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Distance != results[j].Distance {
			return results[i].Distance < results[j].Distance
		}
		return results[i].ID < results[j].ID
	})
	return results, nil
}

// hitMinHeap is a max-heap on Distance so we can efficiently maintain the
// top-k smallest distances: when at capacity, pop the largest (worst) and
// push the new candidate if it's better.
type hitMinHeap []Hit

func (h hitMinHeap) Len() int            { return len(h) }
func (h hitMinHeap) Less(i, j int) bool  { return h[i].Distance > h[j].Distance } // max-heap
func (h hitMinHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *hitMinHeap) Push(x any)         { *h = append(*h, x.(Hit)) }
func (h *hitMinHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}
