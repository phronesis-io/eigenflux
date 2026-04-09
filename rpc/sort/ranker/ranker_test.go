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

	ranked := r.Rank(candidates, profile, 3)
	require.Len(t, ranked, 3)
	assert.Equal(t, int64(1), ranked[0].ItemID)
	// Item 3 (diverse) should be selected before item 2 (similar to item 1)
	assert.Equal(t, int64(3), ranked[1].ItemID, "diverse item should be preferred via MMR")
}

func TestRanker_EmptyProfile(t *testing.T) {
	r := New(defaultTestConfig())
	now := time.Now()

	candidates := []sortDal.Item{
		{ID: 1, Type: "info", Keywords: []string{"AI"},
			Embedding: []float32{1, 0, 0}, UpdatedAt: now, CreatedAt: now},
	}

	profile := &UserProfile{}
	ranked := r.Rank(candidates, profile, 1)
	require.Len(t, ranked, 1)
	assert.Greater(t, ranked[0].Score, 0.0)
}

func TestRanker_EmptyCandidates(t *testing.T) {
	r := New(defaultTestConfig())
	ranked := r.Rank(nil, &UserProfile{}, 5)
	assert.Empty(t, ranked)
}
