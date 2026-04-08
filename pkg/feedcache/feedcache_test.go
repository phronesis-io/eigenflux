package feedcache

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestRedis(t *testing.T) (*redis.Client, func()) {
	mr, err := miniredis.Run()
	require.NoError(t, err)

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})

	cleanup := func() {
		client.Close()
		mr.Close()
	}

	return client, cleanup
}

func TestFeedCache_PushAndPop(t *testing.T) {
	rdb, cleanup := setupTestRedis(t)
	defer cleanup()

	fc := NewFeedCache(rdb)
	ctx := context.Background()

	agentID := int64(1001)
	entries := []Entry{
		{GroupID: 100001, ItemID: 200001, Position: 0, ImpressionID: "imp_1", Score: 1.1, AgentFeatures: `{"keywords":["ai"]}`, ItemFeatures: `{"group_id":100001}`},
		{GroupID: 100002, ItemID: 200002, Position: 1, ImpressionID: "imp_1", Score: 2.2, AgentFeatures: `{"keywords":["ai"]}`, ItemFeatures: `{"group_id":100002}`},
		{GroupID: 100003, ItemID: 200003, Position: 2, ImpressionID: "imp_1", Score: 3.3, AgentFeatures: `{"keywords":["ai"]}`, ItemFeatures: `{"group_id":100003}`},
		{GroupID: 100004, ItemID: 200004, Position: 3, ImpressionID: "imp_1", Score: 4.4, AgentFeatures: `{"keywords":["ai"]}`, ItemFeatures: `{"group_id":100004}`},
		{GroupID: 100005, ItemID: 200005, Position: 4, ImpressionID: "imp_1", Score: 5.5, AgentFeatures: `{"keywords":["ai"]}`, ItemFeatures: `{"group_id":100005}`},
	}

	// Push items
	err := fc.Push(ctx, agentID, entries)
	assert.NoError(t, err)

	// Pop 2 items
	popped, err := fc.Pop(ctx, agentID, 2)
	assert.NoError(t, err)
	require.Len(t, popped, 2)
	assert.Equal(t, entries[:2], popped)

	// Check remaining length
	length, err := fc.Len(ctx, agentID)
	assert.NoError(t, err)
	assert.Equal(t, int64(3), length)

	// Pop remaining items
	popped, err = fc.Pop(ctx, agentID, 10)
	assert.NoError(t, err)
	assert.Equal(t, entries[2:], popped)

	// Cache should be empty now
	length, err = fc.Len(ctx, agentID)
	assert.NoError(t, err)
	assert.Equal(t, int64(0), length)
}

func TestFeedCache_Clear(t *testing.T) {
	rdb, cleanup := setupTestRedis(t)
	defer cleanup()

	fc := NewFeedCache(rdb)
	ctx := context.Background()

	agentID := int64(1001)
	entries := []Entry{
		{GroupID: 100001, ItemID: 200001, Position: 0, ImpressionID: "imp_1"},
		{GroupID: 100002, ItemID: 200002, Position: 1, ImpressionID: "imp_1"},
		{GroupID: 100003, ItemID: 200003, Position: 2, ImpressionID: "imp_1"},
	}

	// Push items
	err := fc.Push(ctx, agentID, entries)
	require.NoError(t, err)

	// Verify items exist
	length, err := fc.Len(ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, int64(3), length)

	// Clear cache
	err = fc.Clear(ctx, agentID)
	assert.NoError(t, err)

	// Verify cache is empty
	length, err = fc.Len(ctx, agentID)
	assert.NoError(t, err)
	assert.Equal(t, int64(0), length)
}

func TestFeedCache_MultipleAgents(t *testing.T) {
	rdb, cleanup := setupTestRedis(t)
	defer cleanup()

	fc := NewFeedCache(rdb)
	ctx := context.Background()

	// Agent 1 cache
	err := fc.Push(ctx, 1001, []Entry{{GroupID: 100001, ItemID: 200001, Position: 0, ImpressionID: "imp_1"}, {GroupID: 100002, ItemID: 200002, Position: 1, ImpressionID: "imp_1"}})
	require.NoError(t, err)

	// Agent 2 cache
	err = fc.Push(ctx, 1002, []Entry{{GroupID: 100003, ItemID: 200003, Position: 0, ImpressionID: "imp_2"}, {GroupID: 100004, ItemID: 200004, Position: 1, ImpressionID: "imp_2"}})
	require.NoError(t, err)

	// Pop from agent 1
	popped1, err := fc.Pop(ctx, 1001, 1)
	assert.NoError(t, err)
	assert.Equal(t, []Entry{{GroupID: 100001, ItemID: 200001, Position: 0, ImpressionID: "imp_1"}}, popped1)

	// Pop from agent 2
	popped2, err := fc.Pop(ctx, 1002, 1)
	assert.NoError(t, err)
	assert.Equal(t, []Entry{{GroupID: 100003, ItemID: 200003, Position: 0, ImpressionID: "imp_2"}}, popped2)

	// Verify remaining lengths
	len1, err := fc.Len(ctx, 1001)
	assert.NoError(t, err)
	assert.Equal(t, int64(1), len1)

	len2, err := fc.Len(ctx, 1002)
	assert.NoError(t, err)
	assert.Equal(t, int64(1), len2)
}

func TestFeedCache_EmptyPop(t *testing.T) {
	rdb, cleanup := setupTestRedis(t)
	defer cleanup()

	fc := NewFeedCache(rdb)
	ctx := context.Background()

	agentID := int64(1001)

	// Pop from empty cache
	popped, err := fc.Pop(ctx, agentID, 5)
	assert.NoError(t, err)
	assert.Empty(t, popped)
}

func TestFeedCache_EmptyPush(t *testing.T) {
	rdb, cleanup := setupTestRedis(t)
	defer cleanup()

	fc := NewFeedCache(rdb)
	ctx := context.Background()

	agentID := int64(1001)

	// Push empty list
	err := fc.Push(ctx, agentID, []Entry{})
	assert.NoError(t, err)

	// Verify cache is still empty
	length, err := fc.Len(ctx, agentID)
	assert.NoError(t, err)
	assert.Equal(t, int64(0), length)
}

func TestFeedCache_KeyFormat(t *testing.T) {
	key := GetKey(1001)
	assert.Equal(t, "feed:cache:1001", key)
}

func TestFeedCache_PopLegacyGroupIDs(t *testing.T) {
	rdb, cleanup := setupTestRedis(t)
	defer cleanup()

	ctx := context.Background()
	key := GetKey(1001)
	require.NoError(t, rdb.RPush(ctx, key, "100001", "100002").Err())

	fc := NewFeedCache(rdb)
	popped, err := fc.Pop(ctx, 1001, 10)
	require.NoError(t, err)
	assert.Equal(t, []Entry{{GroupID: 100001}, {GroupID: 100002}}, popped)
}

func TestFeedCache_PopLegacyJSONEntriesWithoutItemID(t *testing.T) {
	rdb, cleanup := setupTestRedis(t)
	defer cleanup()

	ctx := context.Background()
	key := GetKey(1001)
	require.NoError(t, rdb.RPush(ctx, key,
		`{"group_id":100001,"score":1.1,"agent_features":"{}","item_features":"{}"}`,
		`{"group_id":100002,"score":2.2,"agent_features":"{}","item_features":"{}"}`,
	).Err())

	fc := NewFeedCache(rdb)
	popped, err := fc.Pop(ctx, 1001, 10)
	require.NoError(t, err)
	assert.Equal(t, []Entry{
		{GroupID: 100001, Score: 1.1, AgentFeatures: "{}", ItemFeatures: "{}"},
		{GroupID: 100002, Score: 2.2, AgentFeatures: "{}", ItemFeatures: "{}"},
	}, popped)
}
