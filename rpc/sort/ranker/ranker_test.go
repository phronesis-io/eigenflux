package ranker

import (
	"testing"
	"time"

	sortDal "eigenflux_server/rpc/sort/dal"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func defaultTestConfig() *RankerConfig {
	return &RankerConfig{
		Alpha: 0.4, Beta: 0.2, Gamma: 0.3, Delta: 0.1,
		UrgencyBoost: 0.5, UrgencyWindow: 24 * time.Hour,
		MMRLambda: 0.7, ExplorationSlots: 0, DraftDampening: 0.8,
		Freshness: map[string]FreshnessParams{
			"alert":  {Offset: 2 * time.Hour, Scale: 12 * time.Hour, Decay: 0.5},
			"demand": {Offset: 12 * time.Hour, Scale: 7 * 24 * time.Hour, Decay: 0.8},
			"info":   {Offset: 12 * time.Hour, Scale: 7 * 24 * time.Hour, Decay: 0.8},
			"supply": {Offset: 48 * time.Hour, Scale: 30 * 24 * time.Hour, Decay: 0.9},
		},
	}
}

func TestRanker_BasicRanking(t *testing.T) {
	r := New(defaultTestConfig())
	now := time.Now()

	candidates := []sortDal.Item{
		{ID: 1, Type: "info", Keywords: []string{"AI"}, Domains: []string{"tech"},
			Embedding: []float32{0.9, 0.1, 0}, UpdatedAt: now, CreatedAt: now},
		{ID: 2, Type: "info", Keywords: []string{"cooking"}, Domains: []string{"food"},
			Embedding: []float32{0, 1, 0}, UpdatedAt: now, CreatedAt: now},
	}

	profile := &UserProfile{
		Keywords: []string{"AI"}, Domains: []string{"tech"},
		Embedding: []float32{1, 0, 0},
	}

	ranked := r.Rank(candidates, profile, 2)
	require.Len(t, ranked, 2)
	assert.Equal(t, int64(1), ranked[0].ItemID)
	assert.Greater(t, ranked[0].Score, ranked[1].Score)
}

func TestRanker_DraftDampening(t *testing.T) {
	r := New(defaultTestConfig())
	now := time.Now()

	candidates := []sortDal.Item{
		{ID: 1, Type: "info", Keywords: []string{"AI"}, Domains: []string{"tech"},
			Embedding: []float32{1, 0, 0}, UpdatedAt: now, CreatedAt: now},
		{ID: 2, Embedding: []float32{1, 0, 0}, UpdatedAt: now, CreatedAt: now},
	}

	profile := &UserProfile{Keywords: []string{"AI"}, Domains: []string{"tech"}, Embedding: []float32{1, 0, 0}}
	ranked := r.Rank(candidates, profile, 2)
	require.Len(t, ranked, 2)
	assert.Equal(t, int64(1), ranked[0].ItemID)
	assert.Greater(t, ranked[0].Score, ranked[1].Score)
}

func TestRanker_MMRDiversity(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.MMRLambda = 0.5 // equal weight to relevance and diversity
	r := New(cfg)
	now := time.Now()

	candidates := []sortDal.Item{
		{ID: 1, Type: "info", Keywords: []string{"AI"}, Domains: []string{"tech"},
			Embedding: []float32{1, 0, 0}, UpdatedAt: now, CreatedAt: now},
		{ID: 2, Type: "info", Keywords: []string{"AI"}, Domains: []string{"tech"},
			Embedding: []float32{0.99, 0.01, 0}, UpdatedAt: now, CreatedAt: now},
		{ID: 3, Type: "info", Keywords: []string{"cooking"}, Domains: []string{"food"},
			Embedding: []float32{0, 1, 0}, UpdatedAt: now, CreatedAt: now},
	}

	profile := &UserProfile{
		Keywords: []string{"AI"}, Domains: []string{"tech"},
		Embedding: []float32{1, 0, 0},
	}

	// rankMMR selects diverse items; Rank() uses score-based sorting (MMR disabled).
	// With multiplicative freshness and weight normalization, item 3 (no keyword
	// match) has relevance=0 and total=0, so MMR picks item 2 before item 3.
	// The diversity signal only reorders among items with nonzero relevance.
	ranked := r.rankMMR(candidates, profile, 3)
	require.Len(t, ranked, 3)
	assert.Equal(t, int64(1), ranked[0].ItemID)
	assert.Equal(t, int64(2), ranked[1].ItemID, "item 2 has nonzero relevance, selected before zero-relevance item 3")
	assert.Equal(t, int64(3), ranked[2].ItemID)
}

func TestRanker_ScoreBreakdown(t *testing.T) {
	r := New(defaultTestConfig())
	now := time.Now()

	candidates := []sortDal.Item{
		{ID: 1, Type: "info", Keywords: []string{"AI"}, Domains: []string{"tech"},
			Embedding: []float32{1, 0, 0}, UpdatedAt: now, CreatedAt: now},
	}

	profile := &UserProfile{Keywords: []string{"AI"}, Domains: []string{"tech"}, Embedding: []float32{1, 0, 0}}
	ranked := r.Rank(candidates, profile, 1)
	require.Len(t, ranked, 1)

	bd := ranked[0].Scores
	assert.Greater(t, bd.Semantic, 0.0, "semantic should be positive for matching embedding")
	assert.Greater(t, bd.Keyword, 0.0, "keyword should be positive for matching keywords")
	assert.Greater(t, bd.Freshness, 0.0, "freshness should be positive for recent item")
	assert.InDelta(t, ranked[0].Score, bd.Total, 1e-9, "Score should equal breakdown total")
	assert.False(t, bd.IsDraft, "item with keywords and type should not be draft")
}

func TestRanker_EmptyProfile(t *testing.T) {
	r := New(defaultTestConfig())
	now := time.Now()

	candidates := []sortDal.Item{
		{ID: 1, Type: "info", Keywords: []string{"AI"},
			Embedding: []float32{1, 0, 0}, UpdatedAt: now, CreatedAt: now},
	}

	// Empty profile: no embedding, no keywords → keyword overlap = 0, semantic = 0
	// With multiplicative freshness, relevance=0 → total=0
	profile := &UserProfile{}
	ranked := r.Rank(candidates, profile, 1)
	require.Len(t, ranked, 1)
	assert.Equal(t, 0.0, ranked[0].Score, "empty profile should produce zero score")
}

func TestRanker_EmptyCandidates(t *testing.T) {
	r := New(defaultTestConfig())
	ranked := r.Rank(nil, &UserProfile{}, 5)
	assert.Empty(t, ranked)
}
