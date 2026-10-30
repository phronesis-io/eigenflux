package legacy

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"eigenflux_server/pkg/recallsource"
	sortDal "eigenflux_server/rpc/sort/dal"
	"eigenflux_server/rpc/sort/discovery"
	"eigenflux_server/rpc/sort/ranker"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollapseRankedByGroup_KeepsBestPerGroup(t *testing.T) {
	ranked := []ranker.RankedItem{
		{ItemID: 11, Score: 0.91},
		{ItemID: 12, Score: 0.85},
		{ItemID: 21, Score: 0.80},
		{ItemID: 31, Score: 0.77},
	}
	itemMap := map[int64]sortDal.Item{
		11: {ID: 11, GroupID: 1001},
		12: {ID: 12, GroupID: 1001},
		21: {ID: 21, GroupID: 2002},
		31: {ID: 31, GroupID: 0},
	}

	collapsed, filtered := collapseRankedByGroup(ranked, itemMap)

	assert.Equal(t, 1, filtered)
	assert.Len(t, collapsed, 3)
	assert.Equal(t, int64(11), collapsed[0].ItemID)
	assert.Equal(t, int64(21), collapsed[1].ItemID)
	assert.Equal(t, int64(31), collapsed[2].ItemID)
}

func TestCollapseRankedByGroup_AllowsUngroupedItems(t *testing.T) {
	ranked := []ranker.RankedItem{
		{ItemID: 1, Score: 0.7},
		{ItemID: 2, Score: 0.6},
	}
	itemMap := map[int64]sortDal.Item{
		1: {ID: 1, GroupID: 0},
		2: {ID: 2, GroupID: 0},
	}

	collapsed, filtered := collapseRankedByGroup(ranked, itemMap)

	assert.Equal(t, 0, filtered)
	assert.Len(t, collapsed, 2)
	assert.Equal(t, int64(1), collapsed[0].ItemID)
	assert.Equal(t, int64(2), collapsed[1].ItemID)
}

func TestApplyItemRerankPolicies_FiltersStaleAlerts(t *testing.T) {
	s := &Service{}

	now := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	items := []sortDal.Item{
		{ID: 1, Type: "alert", UpdatedAt: now.Add(-5 * time.Hour)},
		{ID: 2, Type: "alert", UpdatedAt: now.Add(-7 * time.Hour)},
		{ID: 3, Type: "info", UpdatedAt: now.Add(-7 * time.Hour)},
	}
	sourceMap := map[int64]recallsource.Source{
		1: recallsource.Keyword,
		2: recallsource.Keyword,
		3: recallsource.Keyword,
	}
	path := filepath.Join(t.TempDir(), "rerank.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
policies:
  - name: freshness
    item_rules:
      - broadcast_type: alert
        max_age: 6h
        action: drop
`), 0o644))

	s.itemRerankPolicies = loadRerankPolicySet(context.Background(), path, func() time.Time { return now })

	filtered := s.applyItemRerankPolicies(context.Background(), items, sourceMap)

	assert.Len(t, filtered, 2)
	assert.Equal(t, int64(1), filtered[0].ID)
	assert.Equal(t, int64(3), filtered[1].ID)
	_, staleSourceKept := sourceMap[2]
	assert.False(t, staleSourceKept)
}

func TestApplyPostRankBoost_ItemWhitelistOverridesAttributeBoost(t *testing.T) {
	s := &Service{}

	path := filepath.Join(t.TempDir(), "rerank.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
policies:
  - name: boost
    item_boosts:
      - item_id: 2
        weight: 2
    boost_rules:
      - field: type
        values: [supply]
        weight: 3
`), 0o644))

	s.itemRerankPolicies = loadRerankPolicySet(context.Background(), path, time.Now)

	ranked := []ranker.RankedItem{
		{ItemID: 1, Score: 0.3},
		{ItemID: 2, Score: 0.6},
	}
	itemMap := map[int64]sortDal.Item{
		1: {ID: 1, Type: "supply"},
		2: {ID: 2, Type: "supply"},
	}

	out := s.applyPostRankBoost(context.Background(), ranked, itemMap, nil)

	require.Len(t, out, 2)
	assert.Equal(t, int64(2), out[0].ItemID)
	assert.InDelta(t, 1.2, out[0].Score, 1e-9)
	assert.Equal(t, int64(1), out[1].ItemID)
	assert.InDelta(t, 0.9, out[1].Score, 1e-9)
}

func TestLoadRerankPolicySet_BadConfigDisablesPolicies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rerank.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
policies:
  - name: unknown
`), 0o644))

	policySet := loadRerankPolicySet(context.Background(), path, time.Now)

	assert.Empty(t, policySet.PreRankPolicies())
	assert.Empty(t, policySet.PostRankPolicies())
}

func TestConfiguredSwingPageCeiling(t *testing.T) {
	s := &Service{itemRerankPolicies: loadRerankPolicySet(context.Background(), "../../../configs/sort/rerank.yaml", time.Now)}
	limits := s.DiscoverySourceLimits()
	require.Contains(t, limits, discovery.SourceLimit{Source: "swing_i2i", Numerator: 1, Denominator: 4})
	require.Equal(t, &discovery.NeedMix{Numerator: 3, Denominator: 4, MinNeedCosine: 0.4}, s.DiscoveryNeedMix())
	require.NotEmpty(t, s.itemRerankPolicies.PostRankPolicies(), "need_mix must not invalidate the other policies")
	require.NotEmpty(t, s.itemRerankPolicies.InjectRules())
	candidates := []discovery.Candidate{}
	for id := int64(1); id <= 30; id++ {
		c := discovery.Candidate{Document: discovery.Document{Ref: discovery.SourceRef{Type: discovery.Broadcast, ID: id}}}
		if id <= 13 {
			c.Document.Channels = []string{"swing_i2i"}
		}
		candidates = append(candidates, c)
	}
	page, _ := discovery.SelectRecommendationPage(candidates, 20, limits)
	require.Len(t, page, 20)
	swings := 0
	for _, c := range page {
		if c.Document.RecallSources().Has(recallsource.SwingI2I) {
			swings++
		}
	}
	require.Equal(t, 5, swings)
}
