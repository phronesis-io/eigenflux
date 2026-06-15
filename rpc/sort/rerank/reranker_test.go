package rerank

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"eigenflux_server/rpc/sort/rank"
)

// recorderPolicy is a Policy that records the order in which it was applied
// and what fingerprints it saw. Used to assert chaining behaviour.
type recorderPolicy struct {
	tag       string
	calls     *[]string
	seenCount *int
}

func (r *recorderPolicy) Name() string { return r.tag }
func (r *recorderPolicy) Apply(cands []rank.Candidate) []rank.Candidate {
	*r.calls = append(*r.calls, r.tag)
	*r.seenCount = len(cands)
	return cands
}

func mkItem(id int64, score float64) rank.Candidate {
	return rank.NewCandidate(id, rank.CandidateItem, score, nil, nil)
}

func mkSvc(id int64, score float64) rank.Candidate {
	return rank.NewCandidate(id, rank.CandidateService, score, nil, nil)
}

func TestReranker_EmptyInput(t *testing.T) {
	r := New(&DedupPolicy{})
	assert.Empty(t, r.Rerank(nil, 10))
	assert.Empty(t, r.Rerank([]rank.Candidate{}, 10))
}

func TestReranker_PolicyOrderPreserved(t *testing.T) {
	var calls []string
	seenA, seenB, seenC := 0, 0, 0
	a := &recorderPolicy{tag: "a", calls: &calls, seenCount: &seenA}
	b := &recorderPolicy{tag: "b", calls: &calls, seenCount: &seenB}
	c := &recorderPolicy{tag: "c", calls: &calls, seenCount: &seenC}

	r := New(a, b, c)
	r.Rerank([]rank.Candidate{mkItem(1, 0.5)}, 10)
	assert.Equal(t, []string{"a", "b", "c"}, calls)
}

func TestReranker_LimitTruncates(t *testing.T) {
	r := New()
	cands := []rank.Candidate{
		mkItem(1, 0.9), mkItem(2, 0.8), mkItem(3, 0.7), mkItem(4, 0.6),
	}
	out := r.Rerank(cands, 2)
	require.Len(t, out, 2)
	assert.Equal(t, int64(1), out[0].ID())
	assert.Equal(t, int64(2), out[1].ID())
}

func TestReranker_NonPositiveLimitDoesNotTruncate(t *testing.T) {
	r := New()
	cands := []rank.Candidate{mkItem(1, 0.9), mkItem(2, 0.8)}
	assert.Len(t, r.Rerank(cands, 0), 2)
	assert.Len(t, r.Rerank(cands, -1), 2)
}

func TestReranker_PoliciesExposedForLogging(t *testing.T) {
	d := &DedupPolicy{}
	n := &NormalizePolicy{Method: MinMax}
	r := New(d, n)
	require.Len(t, r.Policies(), 2)
	assert.Equal(t, "dedup", r.Policies()[0].Name())
	assert.Equal(t, "normalize:minmax", r.Policies()[1].Name())
}
