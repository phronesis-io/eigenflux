package main

import (
	"testing"

	sortDal "eigenflux_server/rpc/sort/dal"
	"eigenflux_server/rpc/sort/ranker"

	"github.com/stretchr/testify/assert"
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
