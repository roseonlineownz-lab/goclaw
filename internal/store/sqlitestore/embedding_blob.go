//go:build sqlite || sqliteonly

package sqlitestore

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/x448/float16"
)

// Halfvec3072Bytes is the byte length of a 3072-dim halfvec BLOB.
// Each dimension is encoded as IEEE 754 binary16 (2 bytes, little-endian).
const Halfvec3072Bytes = 3072 * 2 // 6144

// EncodeHalfvec3072 converts a 3072-element float32 slice to 6144 bytes
// of little-endian IEEE 754 binary16 values. This matches pgvector's halfvec
// wire format for cross-dialect consistency.
func EncodeHalfvec3072(v []float32) ([]byte, error) {
	if len(v) != 3072 {
		return nil, fmt.Errorf("halfvec encode: expected 3072 dims, got %d", len(v))
	}
	out := make([]byte, Halfvec3072Bytes)
	for i, f := range v {
		h := float16.Fromfloat32(f)
		binary.LittleEndian.PutUint16(out[i*2:], uint16(h))
	}
	return out, nil
}

// DecodeHalfvec3072 converts 6144 bytes of little-endian binary16 back to
// a 3072-element float32 slice.
func DecodeHalfvec3072(b []byte) ([]float32, error) {
	if len(b) != Halfvec3072Bytes {
		return nil, fmt.Errorf("halfvec decode: blob size %d, expected %d", len(b), Halfvec3072Bytes)
	}
	out := make([]float32, 3072)
	for i := range out {
		u := binary.LittleEndian.Uint16(b[i*2:])
		out[i] = float16.Frombits(u).Float32()
	}
	return out, nil
}

// L2Norm computes the L2 (Euclidean) norm of a float32 vector.
// Result is stored alongside the BLOB so cosine similarity can be computed
// without re-reading the full vector during scans that only need the norm.
func L2Norm(v []float32) float64 {
	var s float64
	for _, f := range v {
		s += float64(f) * float64(f)
	}
	return math.Sqrt(s)
}

// CosineSimilarity computes the cosine similarity between two float32 vectors.
// queryNorm must be pre-computed to avoid repeated sqrt calls during batch scans.
// Returns 0 if either norm is zero.
func CosineSimilarity(query []float32, queryNorm float64, candidate []float32, candidateNorm float64) float64 {
	if queryNorm == 0 || candidateNorm == 0 {
		return 0
	}
	var dot float64
	for i, qv := range query {
		dot += float64(qv) * float64(candidate[i])
	}
	return dot / (queryNorm * candidateNorm)
}
