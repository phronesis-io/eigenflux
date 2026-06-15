package rerank

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"eigenflux_server/rpc/sort/rank"
)

func TestRatioPolicy_HitsCycleRatio(t *testing.T) {
	// Build 5 items + 5 services, all scored high enough to be present.
	cands := []rank.Candidate{
		mkItem(1, 0.9), mkItem(2, 0.85), mkItem(3, 0.8), mkItem(4, 0.75), mkItem(5, 0.7),
		mkSvc(10, 0.95), mkSvc(20, 0.9), mkSvc(30, 0.85), mkSvc(40, 0.8), mkSvc(50, 0.75),
	}
	policy := &RatioPolicy{
		CycleSize: 3,
		TypeCounts: map[rank.CandidateType]int{
			rank.CandidateItem:    2,
			rank.CandidateService: 1,
		},
	}
	out := policy.Apply(cands)
	require.Len(t, out, 10)

	// Expected pattern across 9 positions (3 full cycles): I I S | I I S | I I S
	// Position 9: only 0 services left + 0 items left? items=5 used 6 → ran out at position 7
	// Let's just assert the cycle property over the first 6 positions where both queues are healthy.
	types := make([]rank.CandidateType, 6)
	for i := 0; i < 6; i++ {
		types[i] = out[i].Type()
	}
	assert.Equal(t, []rank.CandidateType{
		rank.CandidateItem, rank.CandidateItem, rank.CandidateService,
		rank.CandidateItem, rank.CandidateItem, rank.CandidateService,
	}, types)
}

func TestRatioPolicy_HighestScoreFirstWithinType(t *testing.T) {
	cands := []rank.Candidate{
		mkItem(1, 0.5), mkItem(2, 0.9), mkItem(3, 0.7),
	}
	policy := &RatioPolicy{
		CycleSize:  1,
		TypeCounts: map[rank.CandidateType]int{rank.CandidateItem: 1},
	}
	out := policy.Apply(cands)
	require.Len(t, out, 3)
	// Highest score first within the type queue
	assert.Equal(t, int64(2), out[0].ID())
	assert.Equal(t, int64(3), out[1].ID())
	assert.Equal(t, int64(1), out[2].ID())
}

func TestRatioPolicy_UnderflowFallsThrough(t *testing.T) {
	// 4 items, 1 service. Ratio 1:1 cycle=2 should normally emit I,S,I,S,I,S,... but
	// only 1 service exists, so positions 3 and 5 should fall through to items.
	cands := []rank.Candidate{
		mkItem(1, 0.9), mkItem(2, 0.8), mkItem(3, 0.7), mkItem(4, 0.6),
		mkSvc(10, 1.0),
	}
	policy := &RatioPolicy{
		CycleSize:  2,
		TypeCounts: map[rank.CandidateType]int{rank.CandidateItem: 1, rank.CandidateService: 1},
	}
	out := policy.Apply(cands)
	require.Len(t, out, 5)

	// First two: item, service. After that all items.
	assert.Equal(t, rank.CandidateItem, out[0].Type())
	assert.Equal(t, rank.CandidateService, out[1].Type())
	for i := 2; i < 5; i++ {
		assert.Equal(t, rank.CandidateItem, out[i].Type(), "position %d falls back to item", i)
	}
}

func TestRatioPolicy_UnknownTypeAppendedAtTail(t *testing.T) {
	other := rank.NewCandidate(99, rank.CandidateType("other"), 0.99, nil, nil)
	cands := []rank.Candidate{
		mkItem(1, 0.5), mkSvc(10, 0.6), other,
	}
	policy := &RatioPolicy{
		CycleSize:  2,
		TypeCounts: map[rank.CandidateType]int{rank.CandidateItem: 1, rank.CandidateService: 1},
	}
	out := policy.Apply(cands)
	require.Len(t, out, 3)
	assert.Equal(t, rank.CandidateType("other"), out[2].Type(), "unknown type goes to the tail")
}

func TestRatioPolicy_NoCountsOrEmptyInputNoop(t *testing.T) {
	cands := []rank.Candidate{mkItem(1, 0.5)}
	policy := &RatioPolicy{CycleSize: 3, TypeCounts: nil}
	assert.Equal(t, cands, policy.Apply(cands))

	policy2 := &RatioPolicy{CycleSize: 3, TypeCounts: map[rank.CandidateType]int{rank.CandidateItem: 1}}
	assert.Empty(t, policy2.Apply(nil))
}
