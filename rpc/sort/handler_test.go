package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"eigenflux_server/pkg/recallsource"
	sortDal "eigenflux_server/rpc/sort/dal"
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

	prev := itemRerankPolicies
	itemRerankPolicies = loadRerankPolicySet(context.Background(), path, func() time.Time { return now })
	t.Cleanup(func() { itemRerankPolicies = prev })

	filtered := applyItemRerankPolicies(context.Background(), items, sourceMap)

	assert.Len(t, filtered, 2)
	assert.Equal(t, int64(1), filtered[0].ID)
	assert.Equal(t, int64(3), filtered[1].ID)
	_, staleSourceKept := sourceMap[2]
	assert.False(t, staleSourceKept)
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
