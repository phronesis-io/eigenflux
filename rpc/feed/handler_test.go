package main

import (
	"testing"

	"eigenflux_server/pkg/feedcache"

	"github.com/stretchr/testify/assert"
)

func TestCacheEntryHelpersPreserveRankedItemIdentity(t *testing.T) {
	entries := []feedcache.Entry{
		{GroupID: 10, ItemID: 101, Score: 1.1, AgentFeatures: `{"keywords":["ai"]}`, ItemFeatures: `{"item_id":101}`},
		{GroupID: 20, ItemID: 202, Score: 2.2, AgentFeatures: `{"keywords":["ai"]}`, ItemFeatures: `{"item_id":202}`},
	}

	assert.Equal(t, []int64{10, 20}, groupIDsFromCacheEntries(entries))
	assert.Equal(t, []int64{101, 202}, itemIDsFromCacheEntries(entries))

	svc := &FeedServiceImpl{}
	lookup := svc.buildReplayLookupFromCacheEntries(entries)

	if assert.Contains(t, lookup, int64(10)) {
		assert.Equal(t, 1.1, lookup[10].score)
		assert.Equal(t, `{"item_id":101}`, lookup[10].itemFeatures)
	}
	if assert.Contains(t, lookup, int64(20)) {
		assert.Equal(t, 2.2, lookup[20].score)
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
}
