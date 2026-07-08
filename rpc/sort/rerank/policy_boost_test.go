package rerank

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"eigenflux_server/rpc/sort/rank"
)

type testItemBoostSource struct {
	broadcastType string
	sourceType    string
	contentClass  string
}

func (s testItemBoostSource) ItemBoostFields() (string, string, string) {
	return s.broadcastType, s.sourceType, s.contentClass
}

func boostCand(id int64, score float64, broadcastType, contentClass string) *rank.BasicCandidate {
	return rank.NewCandidate(id, rank.CandidateItem, score, nil,
		testItemBoostSource{broadcastType: broadcastType, contentClass: contentClass})
}

func TestBoostPolicy_MultipliesAndResorts(t *testing.T) {
	// supply starts below info but ×1.3 overtakes it.
	info := boostCand(1, 1.0, "info", "pgc")
	supply := boostCand(2, 0.8, "supply", "pgc")

	policy := &BoostPolicy{Rules: []BoostRule{
		{Field: "type", Values: []string{"supply", "demand"}, Weight: 1.3},
	}}
	out := policy.Apply([]rank.Candidate{info, supply})

	require.Len(t, out, 2)
	assert.Equal(t, int64(2), out[0].ID())
	assert.InDelta(t, 1.04, out[0].Score(), 1e-9)
	assert.Equal(t, int64(1), out[1].ID())
	assert.InDelta(t, 1.0, out[1].Score(), 1e-9)
	assert.Contains(t, supply.Reasons(), "boost:type=supply")
}

func TestBoostPolicy_RulesCompound(t *testing.T) {
	// A UGC demand item matches both rules: ×1.3 ×1.2 = ×1.56.
	c := boostCand(1, 1.0, "demand", "ugc")

	policy := &BoostPolicy{Rules: []BoostRule{
		{Field: "type", Values: []string{"supply", "demand"}, Weight: 1.3},
		{Field: "content_class", Values: []string{"ugc"}, Weight: 1.2},
	}}
	policy.Apply([]rank.Candidate{c})

	assert.InDelta(t, 1.56, c.Score(), 1e-9)
	assert.Contains(t, c.Reasons(), "boost:type=demand")
	assert.Contains(t, c.Reasons(), "boost:content_class=ugc")
}

func TestBoostPolicy_NoMatchLeavesScore(t *testing.T) {
	c := boostCand(1, 0.5, "info", "pgc")

	policy := &BoostPolicy{Rules: []BoostRule{
		{Field: "type", Values: []string{"supply"}, Weight: 1.3},
	}}
	policy.Apply([]rank.Candidate{c})

	assert.InDelta(t, 0.5, c.Score(), 1e-9)
	assert.Empty(t, c.Reasons())
}

func TestBoostPolicy_IgnoresServicesAndUnknownSources(t *testing.T) {
	service := rank.NewCandidate(1, rank.CandidateService, 0.9, nil,
		testItemBoostSource{broadcastType: "supply"})
	unknown := rank.NewCandidate(2, rank.CandidateItem, 0.8, nil, nil)

	policy := &BoostPolicy{Rules: []BoostRule{
		{Field: "type", Values: []string{"supply"}, Weight: 2.0},
	}}
	out := policy.Apply([]rank.Candidate{service, unknown})

	require.Len(t, out, 2)
	assert.InDelta(t, 0.9, service.Score(), 1e-9)
	assert.InDelta(t, 0.8, unknown.Score(), 1e-9)
}

func TestBoostPolicy_EmptyRulesPassThrough(t *testing.T) {
	c := boostCand(1, 0.5, "supply", "ugc")
	policy := &BoostPolicy{}
	out := policy.Apply([]rank.Candidate{c})
	require.Len(t, out, 1)
	assert.InDelta(t, 0.5, c.Score(), 1e-9)
}
