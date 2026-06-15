package rerank

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"eigenflux_server/rpc/sort/rank"
)

func TestSlotPolicy_PromotesTopServiceToTargetPosition(t *testing.T) {
	cands := []rank.Candidate{
		mkItem(1, 0.9), mkItem(2, 0.85), mkItem(3, 0.8), mkItem(4, 0.75),
		mkSvc(10, 0.5), mkSvc(20, 0.9),
	}
	policy := &SlotPolicy{Slots: []SlotRule{{Position: 2, Type: rank.CandidateService}}}
	out := policy.Apply(cands)
	require.Len(t, out, 6)

	assert.Equal(t, rank.CandidateService, out[2].Type(), "position 2 must be a service after slot")
	assert.Equal(t, int64(20), out[2].ID(), "highest-scoring service is promoted")
}

func TestSlotPolicy_AlreadyCorrectTypeIsNoop(t *testing.T) {
	cands := []rank.Candidate{
		mkItem(1, 0.9), mkSvc(10, 0.8), mkItem(2, 0.7),
	}
	policy := &SlotPolicy{Slots: []SlotRule{{Position: 1, Type: rank.CandidateService}}}
	out := policy.Apply(cands)

	assert.Equal(t, int64(1), out[0].ID())
	assert.Equal(t, int64(10), out[1].ID())
	assert.Equal(t, int64(2), out[2].ID())
}

func TestSlotPolicy_MissingTypeLeavesSlotUntouched(t *testing.T) {
	cands := []rank.Candidate{
		mkItem(1, 0.9), mkItem(2, 0.8), mkItem(3, 0.7),
	}
	policy := &SlotPolicy{Slots: []SlotRule{{Position: 1, Type: rank.CandidateService}}}
	out := policy.Apply(cands)
	require.Len(t, out, 3)
	assert.Equal(t, rank.CandidateItem, out[1].Type(), "no service available, slot keeps its item")
}

func TestSlotPolicy_OutOfRangePositionIgnored(t *testing.T) {
	cands := []rank.Candidate{mkItem(1, 0.5)}
	policy := &SlotPolicy{Slots: []SlotRule{
		{Position: -1, Type: rank.CandidateService},
		{Position: 999, Type: rank.CandidateService},
	}}
	out := policy.Apply(cands)
	assert.Len(t, out, 1)
}

func TestSlotPolicy_MultipleSlotsRespectEarlierPlacements(t *testing.T) {
	cands := []rank.Candidate{
		mkItem(1, 0.9), mkItem(2, 0.85), mkItem(3, 0.8), mkItem(4, 0.75),
		mkSvc(10, 0.6), mkSvc(20, 0.5),
	}
	policy := &SlotPolicy{Slots: []SlotRule{
		{Position: 1, Type: rank.CandidateService},
		{Position: 3, Type: rank.CandidateService},
	}}
	out := policy.Apply(cands)
	require.Len(t, out, 6)
	assert.Equal(t, rank.CandidateService, out[1].Type())
	assert.Equal(t, rank.CandidateService, out[3].Type())
	assert.Equal(t, int64(10), out[1].ID(), "top service goes to first slot")
	assert.Equal(t, int64(20), out[3].ID(), "second service goes to second slot")
}

func TestSlotPolicy_TagsCandidate(t *testing.T) {
	promoted := rank.NewCandidate(20, rank.CandidateService, 0.9, nil, nil)
	cands := []rank.Candidate{
		mkItem(1, 0.95), mkItem(2, 0.9), promoted,
	}
	policy := &SlotPolicy{Slots: []SlotRule{{Position: 1, Type: rank.CandidateService}}}
	policy.Apply(cands)
	assert.Contains(t, promoted.Reasons(), "slot:1")
}
