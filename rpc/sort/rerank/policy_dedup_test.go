package rerank

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"eigenflux_server/rpc/sort/rank"
)

func TestDedupPolicy_RemovesSameTypeAndID(t *testing.T) {
	cands := []rank.Candidate{
		mkItem(1, 0.9),
		mkItem(2, 0.8),
		mkItem(1, 0.5), // duplicate of cands[0]
		mkItem(3, 0.4),
	}
	out := (&DedupPolicy{}).Apply(cands)
	require.Len(t, out, 3)
	assert.Equal(t, int64(1), out[0].ID())
	assert.InDelta(t, 0.9, out[0].Score(), 1e-9, "first occurrence wins")
	assert.Equal(t, int64(2), out[1].ID())
	assert.Equal(t, int64(3), out[2].ID())
}

func TestDedupPolicy_SameIDDifferentTypesAreDistinct(t *testing.T) {
	cands := []rank.Candidate{
		mkItem(7, 0.9),
		mkSvc(7, 0.8),
	}
	out := (&DedupPolicy{}).Apply(cands)
	assert.Len(t, out, 2, "item:7 and service:7 have different fingerprints")
}

func TestDedupPolicy_EmptyAndSingleton(t *testing.T) {
	assert.Empty(t, (&DedupPolicy{}).Apply(nil))
	one := []rank.Candidate{mkItem(1, 0.5)}
	assert.Len(t, (&DedupPolicy{}).Apply(one), 1)
}

func TestDedupPolicy_AllDuplicates(t *testing.T) {
	cands := []rank.Candidate{
		mkItem(1, 0.9), mkItem(1, 0.8), mkItem(1, 0.7),
	}
	out := (&DedupPolicy{}).Apply(cands)
	require.Len(t, out, 1)
	assert.InDelta(t, 0.9, out[0].Score(), 1e-9)
}
