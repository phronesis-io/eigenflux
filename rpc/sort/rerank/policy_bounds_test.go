package rerank

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"eigenflux_server/rpc/sort/rank"
)

func TestBoundsPolicy_CeilingTrimsLowestScored(t *testing.T) {
	cands := []rank.Candidate{
		mkItem(1, 0.9), mkItem(2, 0.5), mkItem(3, 0.8), mkItem(4, 0.2),
		mkSvc(10, 0.7), mkSvc(20, 0.6),
	}
	policy := &BoundsPolicy{
		Bounds: map[rank.CandidateType]Bound{
			rank.CandidateItem: {Ceiling: 2},
		},
	}
	out := policy.Apply(cands)

	// Items: top 2 by score (0.9, 0.8). Services unbounded so both stay.
	var itemIDs []int64
	var svcIDs []int64
	for _, c := range out {
		switch c.Type() {
		case rank.CandidateItem:
			itemIDs = append(itemIDs, c.ID())
		case rank.CandidateService:
			svcIDs = append(svcIDs, c.ID())
		}
	}
	assert.ElementsMatch(t, []int64{1, 3}, itemIDs)
	assert.ElementsMatch(t, []int64{10, 20}, svcIDs)
}

func TestBoundsPolicy_TypeWithoutBoundPassesThrough(t *testing.T) {
	cands := []rank.Candidate{mkItem(1, 0.5), mkSvc(10, 0.6)}
	policy := &BoundsPolicy{Bounds: map[rank.CandidateType]Bound{}}
	out := policy.Apply(cands)
	assert.Len(t, out, 2)
}

func TestBoundsPolicy_CeilingZeroMeansUnbounded(t *testing.T) {
	cands := []rank.Candidate{mkItem(1, 0.5), mkItem(2, 0.4), mkItem(3, 0.3)}
	policy := &BoundsPolicy{Bounds: map[rank.CandidateType]Bound{rank.CandidateItem: {Ceiling: 0}}}
	out := policy.Apply(cands)
	assert.Len(t, out, 3)
}

func TestBoundsPolicy_TrimmedCandidatesTaggedForDebug(t *testing.T) {
	keep := rank.NewCandidate(1, rank.CandidateItem, 0.9, nil, nil)
	drop := rank.NewCandidate(2, rank.CandidateItem, 0.1, nil, nil)
	policy := &BoundsPolicy{Bounds: map[rank.CandidateType]Bound{rank.CandidateItem: {Ceiling: 1}}}
	out := policy.Apply([]rank.Candidate{keep, drop})
	require.Len(t, out, 1)
	assert.Equal(t, int64(1), out[0].ID())
	assert.Contains(t, drop.Reasons(), "bounds:ceiling")
}

func TestBoundsPolicy_EmptyBoundsOrEmptyInputNoop(t *testing.T) {
	assert.Empty(t, (&BoundsPolicy{Bounds: map[rank.CandidateType]Bound{rank.CandidateItem: {Ceiling: 5}}}).Apply(nil))
	cands := []rank.Candidate{mkItem(1, 0.5)}
	assert.Len(t, (&BoundsPolicy{}).Apply(cands), 1)
}

// Items dominate the top-Limit window. Floor=1 on service forces the
// lowest-scoring tail item to be displaced by the highest-scoring service
// from beyond the window.
func TestBoundsPolicy_FloorTailReplacesIntoWindow(t *testing.T) {
	cands := []rank.Candidate{
		mkItem(1, 0.95), mkItem(2, 0.90), mkItem(3, 0.85), mkItem(4, 0.80),
		mkSvc(10, 0.50), mkSvc(20, 0.30),
	}
	policy := &BoundsPolicy{
		Limit: 3,
		Bounds: map[rank.CandidateType]Bound{
			rank.CandidateService: {Floor: 1},
		},
	}
	out := policy.Apply(cands)

	// Top 3 must contain at least one service. Highest-scoring service is svc 10.
	top3 := out[:3]
	var svcInTop int
	var svcIDInTop int64
	for _, c := range top3 {
		if c.Type() == rank.CandidateService {
			svcInTop++
			svcIDInTop = c.ID()
		}
	}
	assert.GreaterOrEqual(t, svcInTop, 1, "floor should put at least one service in top-Limit")
	assert.Equal(t, int64(10), svcIDInTop, "highest-scoring service wins")
}

// When the natural score order already satisfies Floor, no replacement runs.
func TestBoundsPolicy_FloorSatisfiedByNaturalOrder(t *testing.T) {
	cands := []rank.Candidate{
		mkItem(1, 0.95), mkSvc(10, 0.90), mkItem(2, 0.85),
	}
	policy := &BoundsPolicy{
		Limit:  3,
		Bounds: map[rank.CandidateType]Bound{rank.CandidateService: {Floor: 1}},
	}
	out := policy.Apply(cands)

	require.Len(t, out, 3)
	assert.Equal(t, int64(1), out[0].ID())
	assert.Equal(t, int64(10), out[1].ID())
	assert.Equal(t, int64(2), out[2].ID())

	svc := out[1].(*rank.BasicCandidate)
	assert.NotContains(t, svc.Reasons(), "bounds:floor:service", "no floor swap needed when natural order satisfies it")
}

// When no candidate of the floored type exists at all, no panic, no fill —
// Floor degrades gracefully.
func TestBoundsPolicy_FloorGracefulWhenTypeAbsent(t *testing.T) {
	cands := []rank.Candidate{mkItem(1, 0.9), mkItem(2, 0.8), mkItem(3, 0.7)}
	policy := &BoundsPolicy{
		Limit:  3,
		Bounds: map[rank.CandidateType]Bound{rank.CandidateService: {Floor: 1}},
	}
	out := policy.Apply(cands)
	require.Len(t, out, 3)
	for _, c := range out {
		assert.Equal(t, rank.CandidateItem, c.Type())
	}
}

// Floor enforcement is disabled when Limit is unset (zero value).
func TestBoundsPolicy_FloorIgnoredWhenLimitUnset(t *testing.T) {
	cands := []rank.Candidate{
		mkItem(1, 0.95), mkItem(2, 0.90), mkSvc(10, 0.10),
	}
	policy := &BoundsPolicy{
		Bounds: map[rank.CandidateType]Bound{rank.CandidateService: {Floor: 1}},
	}
	out := policy.Apply(cands)
	// Without Limit, no floor swap — order is just score-desc.
	require.Len(t, out, 3)
	assert.Equal(t, int64(1), out[0].ID())
	assert.Equal(t, int64(2), out[1].ID())
	assert.Equal(t, int64(10), out[2].ID())
}

// Displaced candidate gets a debug reason tag.
func TestBoundsPolicy_FloorTagsBothSides(t *testing.T) {
	displacedItem := rank.NewCandidate(2, rank.CandidateItem, 0.80, nil, nil)
	promotedSvc := rank.NewCandidate(10, rank.CandidateService, 0.40, nil, nil)
	cands := []rank.Candidate{
		rank.NewCandidate(1, rank.CandidateItem, 0.95, nil, nil),
		displacedItem,
		promotedSvc,
	}
	policy := &BoundsPolicy{
		Limit:  2,
		Bounds: map[rank.CandidateType]Bound{rank.CandidateService: {Floor: 1}},
	}
	_ = policy.Apply(cands)
	assert.Contains(t, promotedSvc.Reasons(), "bounds:floor:service")
	assert.Contains(t, displacedItem.Reasons(), "bounds:displaced")
}
