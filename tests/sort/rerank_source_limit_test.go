package sort_test

import (
	"path/filepath"
	"slices"
	"testing"
	"time"

	"eigenflux_server/pkg/recallsource"
	"eigenflux_server/rpc/sort/rank"
	"eigenflux_server/rpc/sort/rerank"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sourceCandidate(id int64, score float64) rank.Candidate {
	return rank.NewCandidate(id, rank.CandidateItem, score, nil, nil)
}

func sourceMatch(sourceMap map[int64]recallsource.Source, name string) func(rank.Candidate) bool {
	return func(candidate rank.Candidate) bool {
		return slices.Contains(recallsource.Names(sourceMap[candidate.ID()]), name)
	}
}

func TestMatchLimitPolicyCapsFriendItemsAndBackfillsOthers(t *testing.T) {
	candidates := make([]rank.Candidate, 0, 30)
	sources := make(map[int64]recallsource.Source, 30)
	for i := 0; i < 20; i++ {
		id := int64(i + 1)
		candidates = append(candidates, sourceCandidate(id, float64(30-i)))
		sources[id] = recallsource.Friend
	}
	for i := 0; i < 10; i++ {
		id := int64(100 + i)
		candidates = append(candidates, sourceCandidate(id, float64(10-i)))
		sources[id] = recallsource.Keyword
	}

	final := rerank.New(&rerank.MatchLimitPolicy{
		Match:     sourceMatch(sources, "friend"),
		MaxCount:  10,
		ReasonTag: "source=friend",
	}).Rerank(candidates, 20)

	require.Len(t, final, 20)
	friendCount := 0
	for _, candidate := range final {
		if sources[candidate.ID()].Has(recallsource.Friend) {
			friendCount++
		}
	}
	assert.Equal(t, 10, friendCount)
	assert.Equal(t, int64(10), final[9].ID(), "highest-ranked friend items survive")
	assert.Equal(t, int64(100), final[10].ID(), "non-friend items backfill capped friend slots")
}

func TestMatchLimitPolicyCountsMixedSourceFriendItem(t *testing.T) {
	candidates := []rank.Candidate{
		sourceCandidate(1, 0.9),
		sourceCandidate(2, 0.8),
		sourceCandidate(3, 0.7),
	}
	sources := map[int64]recallsource.Source{
		1: recallsource.Friend,
		2: recallsource.Friend | recallsource.Keyword,
		3: recallsource.Keyword,
	}

	final := rerank.New(&rerank.MatchLimitPolicy{
		Match:     sourceMatch(sources, "friend"),
		MaxCount:  1,
		ReasonTag: "source=friend",
	}).Rerank(candidates, 3)

	require.Len(t, final, 2)
	assert.Equal(t, []int64{1, 3}, []int64{final[0].ID(), final[1].ID()})
	assert.Contains(t, candidates[1].(*rank.BasicCandidate).Reasons(), "match_limit:source=friend")
}

func TestSourceLimitConfigUsesExactFraction(t *testing.T) {
	cfg, err := rerank.LoadConfig(filepath.Join("..", "..", "configs", "sort", "rerank.yaml"))
	require.NoError(t, err)
	_, err = cfg.NewPolicies(time.Now)
	require.NoError(t, err)

	limits, err := cfg.SourceLimits()
	require.NoError(t, err)
	require.Len(t, limits, 1)
	assert.Equal(t, "friend", limits[0].Source)
	assert.Equal(t, 10, limits[0].MaxCount(20))
	assert.Equal(t, 1, limits[0].MaxCount(3))
	assert.Equal(t, 0, limits[0].MaxCount(1))
}

func TestSourceLimitConfigRejectsInvalidValues(t *testing.T) {
	cases := []rerank.PolicyConfig{
		{Name: "source_limit", Source: "", MaxFraction: "1/2"},
		{Name: "source_limit", Source: "friend", MaxFraction: ""},
		{Name: "source_limit", Source: "friend", MaxFraction: "0/2"},
		{Name: "source_limit", Source: "friend", MaxFraction: "3/2"},
		{Name: "source_limit", Source: "friend", MaxFraction: "1/0"},
		{Name: "source_limit", Source: "friend", MaxFraction: "0.5"},
	}
	for _, policy := range cases {
		cfg := &rerank.Config{Policies: []rerank.PolicyConfig{policy}}
		_, err := cfg.NewPolicies(time.Now)
		require.Error(t, err, policy)
	}
}
