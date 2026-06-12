package rerank

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"eigenflux_server/rpc/sort/rank"
)

func TestNormalizePolicy_MinMax_PutsBothTypesOn01(t *testing.T) {
	cands := []rank.Candidate{
		mkItem(1, 0.1), mkItem(2, 0.2), mkItem(3, 0.3),
		mkSvc(10, 10.0), mkSvc(20, 20.0), mkSvc(30, 30.0),
	}
	(&NormalizePolicy{Method: MinMax}).Apply(cands)

	// Items: 0.1→0, 0.2→0.5, 0.3→1.0
	assert.InDelta(t, 0.0, cands[0].Score(), 1e-9)
	assert.InDelta(t, 0.5, cands[1].Score(), 1e-9)
	assert.InDelta(t, 1.0, cands[2].Score(), 1e-9)

	// Services: 10→0, 20→0.5, 30→1.0
	assert.InDelta(t, 0.0, cands[3].Score(), 1e-9)
	assert.InDelta(t, 0.5, cands[4].Score(), 1e-9)
	assert.InDelta(t, 1.0, cands[5].Score(), 1e-9)
}

func TestNormalizePolicy_MinMax_AllEqualGoesToZero(t *testing.T) {
	cands := []rank.Candidate{mkItem(1, 0.7), mkItem(2, 0.7)}
	(&NormalizePolicy{Method: MinMax}).Apply(cands)
	assert.InDelta(t, 0.0, cands[0].Score(), 1e-9)
	assert.InDelta(t, 0.0, cands[1].Score(), 1e-9)
}

func TestNormalizePolicy_ZScore(t *testing.T) {
	cands := []rank.Candidate{
		mkItem(1, 1.0), mkItem(2, 2.0), mkItem(3, 3.0),
	}
	(&NormalizePolicy{Method: ZScore}).Apply(cands)

	// Mean=2, stddev=sqrt(2/3). Standardised: -sqrt(1.5), 0, +sqrt(1.5).
	expected := math.Sqrt(1.5)
	assert.InDelta(t, -expected, cands[0].Score(), 1e-9)
	assert.InDelta(t, 0.0, cands[1].Score(), 1e-9)
	assert.InDelta(t, +expected, cands[2].Score(), 1e-9)
}

func TestNormalizePolicy_TagsCandidate(t *testing.T) {
	c := rank.NewCandidate(1, rank.CandidateItem, 0.5, nil, nil)
	(&NormalizePolicy{Method: MinMax}).Apply([]rank.Candidate{c, mkItem(2, 1.0)})
	require.NotEmpty(t, c.Reasons())
	assert.Contains(t, c.Reasons(), "normalize:minmax")
}

func TestNormalizePolicy_Name(t *testing.T) {
	assert.Equal(t, "normalize:minmax", (&NormalizePolicy{Method: MinMax}).Name())
	assert.Equal(t, "normalize:zscore", (&NormalizePolicy{Method: ZScore}).Name())
}
