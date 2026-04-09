package cache

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestRedis(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	mr, err := miniredis.Run()
	require.NoError(t, err)

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})

	return client, mr
}

func TestRedisCache_SetAndGet(t *testing.T) {
	client, mr := setupTestRedis(t)
	defer mr.Close()

	cache := NewRedisCache(client)
	ctx := context.Background()

	// Test data
	type TestData struct {
		Name  string `json:"name"`
		Value int    `json:"value"`
	}
	testData := TestData{Name: "test", Value: 42}

	// Set
	err := cache.Set(ctx, "test:key", testData, 10*time.Second)
	assert.NoError(t, err)

	// Get
	var result TestData
	err = cache.Get(ctx, "test:key", &result)
	assert.NoError(t, err)
	assert.Equal(t, testData, result)
}

func TestRedisCache_GetMiss(t *testing.T) {
	client, mr := setupTestRedis(t)
	defer mr.Close()

	cache := NewRedisCache(client)
	ctx := context.Background()

	var result string
	err := cache.Get(ctx, "nonexistent", &result)
	assert.Equal(t, ErrCacheMiss, err)
}

func TestRedisCache_Delete(t *testing.T) {
	client, mr := setupTestRedis(t)
	defer mr.Close()

	cache := NewRedisCache(client)
	ctx := context.Background()

	// Set
	err := cache.Set(ctx, "test:key", "value", 10*time.Second)
	assert.NoError(t, err)

	// Delete
	err = cache.Delete(ctx, "test:key")
	assert.NoError(t, err)

	// Verify deleted
	var result string
	err = cache.Get(ctx, "test:key", &result)
	assert.Equal(t, ErrCacheMiss, err)
}

func TestRedisCache_Exists(t *testing.T) {
	client, mr := setupTestRedis(t)
	defer mr.Close()

	cache := NewRedisCache(client)
	ctx := context.Background()

	// Non-existent key
	exists, err := cache.Exists(ctx, "test:key")
	assert.NoError(t, err)
	assert.False(t, exists)

	// Set key
	err = cache.Set(ctx, "test:key", "value", 10*time.Second)
	assert.NoError(t, err)

	// Check exists
	exists, err = cache.Exists(ctx, "test:key")
	assert.NoError(t, err)
	assert.True(t, exists)
}

func TestSearchCache_BuildCacheKey(t *testing.T) {
	client, mr := setupTestRedis(t)
	defer mr.Close()

	sc := NewSearchCache(client, 2*time.Second, 2*time.Second)

	// Test consistent hashing
	key1 := sc.BuildCacheKey([]string{"tech", "ai"}, []string{"ml", "nlp"}, "US")
	key2 := sc.BuildCacheKey([]string{"ai", "tech"}, []string{"nlp", "ml"}, "US")
	assert.Equal(t, key1, key2, "Keys should be identical regardless of order")

	// Test different parameters produce different keys
	key3 := sc.BuildCacheKey([]string{"tech"}, []string{"ml"}, "US")
	assert.NotEqual(t, key1, key3, "Different parameters should produce different keys")

	// Test case-insensitive caching
	keyUpperCase := sc.BuildCacheKey([]string{"AI", "Tech"}, []string{"ML", "NLP"}, "US")
	keyLowerCase := sc.BuildCacheKey([]string{"ai", "tech"}, []string{"ml", "nlp"}, "US")
	keyMixedCase := sc.BuildCacheKey([]string{"Ai", "TECH"}, []string{"Ml", "nlp"}, "US")
	assert.Equal(t, keyLowerCase, keyUpperCase, "Keys should be identical regardless of case")
	assert.Equal(t, keyLowerCase, keyMixedCase, "Keys should be identical regardless of case")
}

func TestSearchCache_SetAndGet(t *testing.T) {
	client, mr := setupTestRedis(t)
	defer mr.Close()

	sc := NewSearchCache(client, 2*time.Second, 2*time.Second)
	ctx := context.Background()

	// Test data
	items := []CachedItem{
		{
			ItemID:        "1",
			Content:       "Test content 1",
			Summary:       "Summary 1",
			BroadcastType: "info",
			Domains:       []string{"tech"},
			Keywords:      []string{"ai"},
			UpdatedAt:     time.Now().Unix(),
			UpdatedAtMs:   time.Now().UnixMilli(),
			Score:         0.9,
		},
		{
			ItemID:        "2",
			Content:       "Test content 2",
			Summary:       "Summary 2",
			BroadcastType: "supply",
			Domains:       []string{"tech"},
			Keywords:      []string{"ml"},
			UpdatedAt:     time.Now().Unix(),
			UpdatedAtMs:   time.Now().UnixMilli(),
			Score:         0.8,
		},
	}

	key := sc.BuildCacheKey([]string{"tech"}, []string{"ai", "ml"}, "US")

	// Set
	err := sc.Set(ctx, key, items)
	assert.NoError(t, err)

	// Get
	result, err := sc.Get(ctx, key)
	assert.NoError(t, err)
	assert.Equal(t, len(items), len(result))
	assert.Equal(t, items[0].ItemID, result[0].ItemID)
	assert.Equal(t, items[1].ItemID, result[1].ItemID)
}

func TestFilterByTimestamp(t *testing.T) {
	now := time.Now().Unix()

	items := []CachedItem{
		{ItemID: "1", UpdatedAt: now - 100},
		{ItemID: "2", UpdatedAt: now - 50},
		{ItemID: "3", UpdatedAt: now - 10},
		{ItemID: "4", UpdatedAt: now + 10},
	}

	// Filter items newer than (now - 60)
	// Should return items with UpdatedAt > (now - 60)
	// Items 2, 3, 4 have UpdatedAt > (now - 60)
	filtered := FilterByTimestamp(items, now-60)
	assert.Equal(t, 3, len(filtered))
	assert.Equal(t, "2", filtered[0].ItemID)
	assert.Equal(t, "3", filtered[1].ItemID)
	assert.Equal(t, "4", filtered[2].ItemID)

	// No filter (lastFetchTime = 0)
	filtered = FilterByTimestamp(items, 0)
	assert.Equal(t, len(items), len(filtered))
}

func TestProfileCache_SetAndGet(t *testing.T) {
	client, mr := setupTestRedis(t)
	defer mr.Close()

	pc := NewProfileCache(client, 60*time.Second)
	ctx := context.Background()

	// Test data
	profile := &CachedProfile{
		AgentID:  12345,
		Keywords: []string{"ai", "ml", "nlp"},
		Domains:  []string{"tech", "science"},
		Geo:      "US",
	}

	// Set
	err := pc.Set(ctx, profile)
	assert.NoError(t, err)

	// Get
	result, err := pc.Get(ctx, 12345)
	assert.NoError(t, err)
	assert.Equal(t, profile.AgentID, result.AgentID)
	assert.Equal(t, profile.Keywords, result.Keywords)
	assert.Equal(t, profile.Domains, result.Domains)
	assert.Equal(t, profile.Geo, result.Geo)
}

func TestProfileCache_GetMiss(t *testing.T) {
	client, mr := setupTestRedis(t)
	defer mr.Close()

	pc := NewProfileCache(client, 60*time.Second)
	ctx := context.Background()

	_, err := pc.Get(ctx, 99999)
	assert.Equal(t, ErrCacheMiss, err)
}

func TestProfileCache_Delete(t *testing.T) {
	client, mr := setupTestRedis(t)
	defer mr.Close()

	pc := NewProfileCache(client, 60*time.Second)
	ctx := context.Background()

	// Set
	profile := &CachedProfile{
		AgentID:  12345,
		Keywords: []string{"ai"},
		Domains:  []string{"tech"},
		Geo:      "US",
	}
	err := pc.Set(ctx, profile)
	assert.NoError(t, err)

	// Delete
	err = pc.Delete(ctx, 12345)
	assert.NoError(t, err)

	// Verify deleted
	_, err = pc.Get(ctx, 12345)
	assert.Equal(t, ErrCacheMiss, err)
}
