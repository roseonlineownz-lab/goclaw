package memory

import (
	"math/rand"
	"strconv"
	"strings"
)

// generate3072HalfvecLiteral returns a pgvector-compatible "[f1,f2,...]" string
// with exactly 3072 float values generated deterministically from seed.
// Values are normal-distributed and clamped to float32 range to match halfvec precision.
// Used by integration tests that INSERT directly via SQL cast ::halfvec.
func generate3072HalfvecLiteral(seed int64) string {
	rng := rand.New(rand.NewSource(seed)) //nolint:gosec // deterministic test fixture
	parts := make([]string, 3072)
	for i := range parts {
		parts[i] = strconv.FormatFloat(rng.NormFloat64(), 'f', 6, 32)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// generate3072FloatSlice returns a []float32 with 3072 deterministic values.
// Used when calling EmbeddingProvider.Embed() stubs or comparing round-trip results.
func generate3072FloatSlice(seed int64) []float32 {
	rng := rand.New(rand.NewSource(seed)) //nolint:gosec
	out := make([]float32, 3072)
	for i := range out {
		out[i] = float32(rng.NormFloat64())
	}
	return out
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
