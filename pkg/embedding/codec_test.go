package embedding

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	original := []float32{0.1, -0.5, 0.0, 1.0, math.MaxFloat32, math.SmallestNonzeroFloat32}
	encoded := Encode(original)
	assert.Equal(t, len(original)*4, len(encoded))
	decoded := Decode(encoded)
	require.Equal(t, len(original), len(decoded))
	for i := range original {
		assert.Equal(t, original[i], decoded[i], "mismatch at index %d", i)
	}
}

func TestEncodeEmpty(t *testing.T) {
	assert.Nil(t, Encode(nil))
	assert.Nil(t, Encode([]float32{}))
}

func TestDecodeEmpty(t *testing.T) {
	assert.Nil(t, Decode(nil))
	assert.Nil(t, Decode([]byte{}))
}

func TestDecodeInvalidLength(t *testing.T) {
	// Non-multiple-of-4 input returns nil to prevent silent truncation
	result := Decode([]byte{1, 2, 3, 4, 5})
	assert.Nil(t, result)
}

func TestCosineSimilarity(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{1, 0, 0}
	assert.InDelta(t, 1.0, CosineSimilarity(a, b), 0.001)

	c := []float32{0, 1, 0}
	assert.InDelta(t, 0.0, CosineSimilarity(a, c), 0.001)

	d := []float32{-1, 0, 0}
	assert.InDelta(t, -1.0, CosineSimilarity(a, d), 0.001)
}

func TestCosineSimilarityZeroVector(t *testing.T) {
	a := []float32{0, 0, 0}
	b := []float32{1, 0, 0}
	assert.InDelta(t, 0.0, CosineSimilarity(a, b), 0.001)
}

func TestCosineSimilarityLengthMismatch(t *testing.T) {
	a := []float32{1, 0}
	b := []float32{1, 0, 0}
	assert.InDelta(t, 0.0, CosineSimilarity(a, b), 0.001)
}
