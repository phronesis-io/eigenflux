package rerank

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"eigenflux_server/rpc/sort/rank"
)

func mkSvcWithIntents(id int64, score float64, intents []string) *rank.BasicCandidate {
	c := rank.NewCandidate(id, rank.CandidateService, score, nil, nil)
	c.SetMatchedIntents(intents)
	return c
}

func TestCoveragePolicy_EachIntentMeetsFloor(t *testing.T) {
	// Top-3 by raw score is three "translate" candidates. Coverage must pull
	// in one "ppt" and one "image" by tail-replacement.
	cands := []rank.Candidate{
		mkSvcWithIntents(1, 0.95, []string{"translate"}),
		mkSvcWithIntents(2, 0.90, []string{"translate"}),
		mkSvcWithIntents(3, 0.85, []string{"translate"}),
		mkSvcWithIntents(4, 0.70, []string{"ppt"}),
		mkSvcWithIntents(5, 0.65, []string{"ppt"}),
		mkSvcWithIntents(6, 0.60, []string{"image"}),
	}
	p := &CoveragePolicy{
		Limit:               3,
		FloorPerIntent:      1,
		ImportanceThreshold: 0.5,
		Importance: map[string]float64{
			"translate": 1.0,
			"ppt":       0.9,
			"image":     0.6,
		},
	}
	out := p.Apply(cands)
	assert.GreaterOrEqual(t, len(out), 3)

	seen := map[string]bool{}
	for _, c := range out[:3] {
		for _, i := range c.(*rank.BasicCandidate).MatchedIntents() {
			seen[i] = true
		}
	}
	for _, want := range []string{"translate", "ppt", "image"} {
		assert.True(t, seen[want], "intent %q missing from window", want)
	}
}

func TestCoveragePolicy_LowImportanceIntentNotProtected(t *testing.T) {
	cands := []rank.Candidate{
		mkSvcWithIntents(1, 0.95, []string{"translate"}),
		mkSvcWithIntents(2, 0.90, []string{"translate"}),
		mkSvcWithIntents(3, 0.85, []string{"translate"}),
		mkSvcWithIntents(4, 0.10, []string{"image"}),
	}
	p := &CoveragePolicy{
		Limit:               3,
		FloorPerIntent:      1,
		ImportanceThreshold: 0.5,
		Importance:          map[string]float64{"translate": 1.0, "image": 0.3},
	}
	out := p.Apply(cands)
	for _, c := range out[:3] {
		for _, i := range c.(*rank.BasicCandidate).MatchedIntents() {
			assert.NotEqual(t, "image", i, "low-importance intent must not be promoted")
		}
	}
}

func TestCoveragePolicy_NoCandidatesForIntent_NoOp(t *testing.T) {
	cands := []rank.Candidate{
		mkSvcWithIntents(1, 0.95, []string{"translate"}),
		mkSvcWithIntents(2, 0.90, []string{"translate"}),
	}
	p := &CoveragePolicy{
		Limit:               2,
		FloorPerIntent:      1,
		ImportanceThreshold: 0.5,
		Importance:          map[string]float64{"translate": 1.0, "ppt": 1.0},
	}
	out := p.Apply(cands)
	assert.Len(t, out, 2)
}

func TestCoveragePolicy_DoesNotAddReason(t *testing.T) {
	cands := []rank.Candidate{
		mkSvcWithIntents(1, 0.95, []string{"translate"}),
		mkSvcWithIntents(2, 0.10, []string{"ppt"}),
	}
	p := &CoveragePolicy{
		Limit:               1,
		FloorPerIntent:      1,
		ImportanceThreshold: 0.5,
		Importance:          map[string]float64{"translate": 1.0, "ppt": 1.0},
	}
	out := p.Apply(cands)
	for _, c := range out {
		bc := c.(*rank.BasicCandidate)
		for _, r := range bc.Reasons() {
			assert.NotContains(t, r, "coverage:", "CoveragePolicy must not add reason tags")
		}
	}
}

func TestCoveragePolicy_NilImportanceTreatsAllAsOne(t *testing.T) {
	cands := []rank.Candidate{
		mkSvcWithIntents(1, 0.95, []string{"a"}),
		mkSvcWithIntents(2, 0.50, []string{"b"}),
	}
	p := &CoveragePolicy{
		Limit:               1,
		FloorPerIntent:      1,
		ImportanceThreshold: 0.5,
		Importance:          nil,
	}
	out := p.Apply(cands)
	// With nil Importance, both "a" and "b" are treated as importance 1.0.
	// The window is size 1. Alphabetical order processes "a" first: it is
	// already satisfied (id=1 is "a") and that slot becomes locked. When "b"
	// is processed, the only window slot is locked, so no swap occurs and
	// id=1 keeps the position. This is the determinate behaviour — once a
	// protected intent's coverage is established, later intents cannot
	// displace it within the same pass.
	assert.Equal(t, int64(1), out[0].(*rank.BasicCandidate).ID(), "first alphabetical protected intent locks its slot")
}
