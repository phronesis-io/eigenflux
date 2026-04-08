package main

import (
	"testing"

	"eigenflux_server/pkg/feedcache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCacheEntryHelpersPreserveRankedItemIdentity(t *testing.T) {
	entries := []feedcache.Entry{
		{GroupID: 10, ItemID: 101, Position: 0, ImpressionID: "imp_1", Score: 1.1, AgentFeatures: `{"keywords":["ai"]}`, ItemFeatures: `{"item_id":101}`},
		{GroupID: 20, ItemID: 202, Position: 1, ImpressionID: "imp_1", Score: 2.2, AgentFeatures: `{"keywords":["ai"]}`, ItemFeatures: `{"item_id":202}`},
	}

	assert.Equal(t, []int64{10, 20}, groupIDsFromCacheEntries(entries))
	assert.Equal(t, []int64{101, 202}, itemIDsFromCacheEntries(entries))
	assert.Equal(t, "imp_1", impressionIDFromCacheEntries(entries))

	svc := &FeedServiceImpl{}
	lookup := svc.buildReplayLookupFromCacheEntries(entries)

	if assert.Contains(t, lookup, int64(10)) {
		assert.Equal(t, 0, lookup[10].position)
		assert.Equal(t, 1.1, lookup[10].score)
		assert.Equal(t, "imp_1", lookup[10].impressionID)
		assert.Equal(t, `{"item_id":101}`, lookup[10].itemFeatures)
	}
	if assert.Contains(t, lookup, int64(20)) {
		assert.Equal(t, 1, lookup[20].position)
		assert.Equal(t, 2.2, lookup[20].score)
		assert.Equal(t, "imp_1", lookup[20].impressionID)
		assert.Equal(t, `{"item_id":202}`, lookup[20].itemFeatures)
	}
}

func TestItemIDsFromCacheEntriesSkipsLegacyEntriesWithoutItemID(t *testing.T) {
	entries := []feedcache.Entry{
		{GroupID: 10, ItemID: 101},
		{GroupID: 20},
		{GroupID: 30, ItemID: 303},
	}

	assert.Equal(t, []int64{101, 303}, itemIDsFromCacheEntries(entries))
	assert.Equal(t, []int64{10, 20, 30}, groupIDsFromCacheEntries(entries))
	assert.Empty(t, impressionIDFromCacheEntries(entries))
}

type stubIDGen struct {
	ids []int64
}

func (g *stubIDGen) NextID() (int64, error) {
	id := g.ids[0]
	g.ids = g.ids[1:]
	return id, nil
}

func TestNewImpressionIDPrefixesSnowflakeID(t *testing.T) {
	svc := &FeedServiceImpl{
		impressionIDGen: &stubIDGen{ids: []int64{12345}},
	}

	impressionID, err := svc.newImpressionID()
	require.NoError(t, err)
	assert.Equal(t, "imp_12345", impressionID)
}
