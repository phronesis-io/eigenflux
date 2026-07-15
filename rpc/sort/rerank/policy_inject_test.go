package rerank

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"eigenflux_server/rpc/sort/rank"
)

// matchIDs returns a predicate matching any candidate whose ID is in the set.
func matchIDs(ids ...int64) func(rank.Candidate) bool {
	set := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return func(c rank.Candidate) bool {
		_, ok := set[c.ID()]
		return ok
	}
}

func ids(cands []rank.Candidate) []int64 {
	out := make([]int64, len(cands))
	for i, c := range cands {
		out[i] = c.ID()
	}
	return out
}

func TestInjectPolicy_PromotesLowScoreMatchToTargetPosition(t *testing.T) {
	// Item 99 is un-exposed UGC with a low score; without injection it sits last
	// and would be truncated away. Injection must move it to position 2.
	cands := []rank.Candidate{
		mkItem(1, 0.9), mkItem(2, 0.85), mkItem(3, 0.8), mkItem(4, 0.75), mkItem(99, 0.01),
	}
	out := (&InjectPolicy{Match: matchIDs(99), Count: 1, Positions: []int{2}}).Apply(cands)

	require.Len(t, out, 5)
	assert.Equal(t, int64(99), out[2].ID(), "matched candidate lands at reserved position 2")
	assert.ElementsMatch(t, []int64{1, 2, 3, 4, 99}, ids(out), "no items lost or duplicated")
}

func TestInjectPolicy_RelevanceFirstPicksHighestScoringMatch(t *testing.T) {
	// Two matches; only one slot. The higher-scoring match wins (input is
	// score-ordered), giving relevance-first behaviour for free.
	cands := []rank.Candidate{
		mkItem(1, 0.9), mkItem(50, 0.6), mkItem(2, 0.55), mkItem(60, 0.2),
	}
	out := (&InjectPolicy{Match: matchIDs(50, 60), Count: 1, Positions: []int{0}}).Apply(cands)
	assert.Equal(t, int64(50), out[0].ID(), "higher-scoring match is injected")
}

func TestInjectPolicy_CoverageFallbackInjectsWhenOnlyLowScoreMatches(t *testing.T) {
	// Even with no relevance, the un-exposed item still gets its slot.
	cands := []rank.Candidate{mkItem(1, 0.9), mkItem(2, 0.8), mkItem(77, 0.0)}
	out := (&InjectPolicy{Match: matchIDs(77), Count: 1, Positions: []int{1}}).Apply(cands)
	assert.Equal(t, int64(77), out[1].ID())
}

func TestInjectPolicy_MultipleSlotsAscending(t *testing.T) {
	cands := []rank.Candidate{
		mkItem(1, 0.9), mkItem(2, 0.85), mkItem(3, 0.8),
		mkItem(50, 0.3), mkItem(60, 0.2),
	}
	out := (&InjectPolicy{Match: matchIDs(50, 60), Count: 2, Positions: []int{1, 3}}).Apply(cands)
	require.Len(t, out, 5)
	assert.Equal(t, int64(50), out[1].ID())
	assert.Equal(t, int64(60), out[3].ID())
	assert.ElementsMatch(t, []int64{1, 2, 3, 50, 60}, ids(out))
}

func TestInjectPolicy_EmptyPositionsFillsFront(t *testing.T) {
	cands := []rank.Candidate{mkItem(1, 0.9), mkItem(2, 0.8), mkItem(50, 0.1)}
	out := (&InjectPolicy{Match: matchIDs(50), Count: 1}).Apply(cands)
	assert.Equal(t, int64(50), out[0].ID(), "no positions → front")
}

func TestInjectPolicy_CountCapsInjections(t *testing.T) {
	cands := []rank.Candidate{mkItem(1, 0.9), mkItem(50, 0.3), mkItem(60, 0.2), mkItem(70, 0.1)}
	out := (&InjectPolicy{Match: matchIDs(50, 60, 70), Count: 1, Positions: []int{0}}).Apply(cands)
	assert.Equal(t, int64(50), out[0].ID())
	// Only one injected; the rest keep their relative order after the injected one.
	assert.ElementsMatch(t, []int64{1, 50, 60, 70}, ids(out))
}

func TestInjectPolicy_NoMatchLeavesOrderUnchanged(t *testing.T) {
	cands := []rank.Candidate{mkItem(1, 0.9), mkItem(2, 0.8)}
	out := (&InjectPolicy{Match: matchIDs(999), Count: 1, Positions: []int{0}}).Apply(cands)
	assert.Equal(t, []int64{1, 2}, ids(out))
}

func TestInjectPolicy_ZeroCountIsNoop(t *testing.T) {
	cands := []rank.Candidate{mkItem(1, 0.9), mkItem(50, 0.1)}
	out := (&InjectPolicy{Match: matchIDs(50), Count: 0, Positions: []int{0}}).Apply(cands)
	assert.Equal(t, []int64{1, 50}, ids(out))
}

func TestInjectPolicy_NilMatchIsNoop(t *testing.T) {
	cands := []rank.Candidate{mkItem(1, 0.9), mkItem(2, 0.1)}
	out := (&InjectPolicy{Count: 1, Positions: []int{0}}).Apply(cands)
	assert.Equal(t, []int64{1, 2}, ids(out))
}

func TestInjectPolicy_TagsInjectedCandidate(t *testing.T) {
	injected := rank.NewCandidate(50, rank.CandidateItem, 0.1, nil, nil)
	cands := []rank.Candidate{mkItem(1, 0.9), mkItem(2, 0.8), injected}
	(&InjectPolicy{Match: matchIDs(50), Count: 1, Positions: []int{1}}).Apply(cands)
	assert.Contains(t, injected.Reasons(), "inject:1")
}

func TestInjectPolicy_OutOfRangePositionClamped(t *testing.T) {
	cands := []rank.Candidate{mkItem(1, 0.9), mkItem(50, 0.1)}
	out := (&InjectPolicy{Match: matchIDs(50), Count: 1, Positions: []int{999}}).Apply(cands)
	require.Len(t, out, 2)
	assert.Equal(t, int64(50), out[len(out)-1].ID(), "clamped to tail")
}
