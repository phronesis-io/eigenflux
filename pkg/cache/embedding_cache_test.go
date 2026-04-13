package cache

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestEmbeddingCache_BuildKey(t *testing.T) {
	client, mr := setupTestRedis(t)
	defer mr.Close()

	ec := NewEmbeddingCache(client, 24*time.Hour)

	// Test key format
	agentID := int64(12345)
	key := ec.BuildKey(agentID)
	assert.Equal(t, "cache:profile:emb:12345", key)

	// Test different agent IDs produce different keys
	key2 := ec.BuildKey(67890)
	assert.Equal(t, "cache:profile:emb:67890", key2)
	assert.NotEqual(t, key, key2)
}

func TestEmbeddingCache_SetAndGet(t *testing.T) {
	client, mr := setupTestRedis(t)
	defer mr.Close()

	ec := NewEmbeddingCache(client, 24*time.Hour)
	ctx := context.Background()

	// Test data: raw embedding bytes (simulating float32 array)
	embeddingData := []byte{0x3f, 0x80, 0x00, 0x00, 0x3f, 0x00, 0x00, 0x00, 0xbe, 0x80, 0x00, 0x00}

	agentID := int64(12345)

	// Set
	err := ec.Set(ctx, agentID, embeddingData)
	assert.NoError(t, err)

	// Get
	result, err := ec.Get(ctx, agentID)
	assert.NoError(t, err)
	assert.Equal(t, embeddingData, result)
}

func TestEmbeddingCache_GetMiss(t *testing.T) {
	client, mr := setupTestRedis(t)
	defer mr.Close()

	ec := NewEmbeddingCache(client, 24*time.Hour)
	ctx := context.Background()

	// Get on missing key should return ErrCacheMiss
	_, err := ec.Get(ctx, 99999)
	assert.Equal(t, ErrCacheMiss, err)
}

func TestEmbeddingCache_Delete(t *testing.T) {
	client, mr := setupTestRedis(t)
	defer mr.Close()

	ec := NewEmbeddingCache(client, 24*time.Hour)
	ctx := context.Background()

	// Set
	embeddingData := []byte{0x3f, 0x80, 0x00, 0x00}
	agentID := int64(12345)
	err := ec.Set(ctx, agentID, embeddingData)
	assert.NoError(t, err)

	// Delete
	err = ec.Delete(ctx, agentID)
	assert.NoError(t, err)

	// Verify deleted
	_, err = ec.Get(ctx, agentID)
	assert.Equal(t, ErrCacheMiss, err)
}

func TestEmbeddingCache_LargeEmbedding(t *testing.T) {
	client, mr := setupTestRedis(t)
	defer mr.Close()

	ec := NewEmbeddingCache(client, 24*time.Hour)
	ctx := context.Background()

	// Test with large embedding (1536-dimensional float32 = 6144 bytes)
	largeEmbedding := make([]byte, 6144)
	for i := range largeEmbedding {
		largeEmbedding[i] = byte(i % 256)
	}

	agentID := int64(54321)

	// Set
	err := ec.Set(ctx, agentID, largeEmbedding)
	assert.NoError(t, err)

	// Get
	result, err := ec.Get(ctx, agentID)
	assert.NoError(t, err)
	assert.Equal(t, largeEmbedding, result)
	assert.Equal(t, 6144, len(result))
}

func TestEmbeddingCache_EmptyEmbedding(t *testing.T) {
	client, mr := setupTestRedis(t)
	defer mr.Close()

	ec := NewEmbeddingCache(client, 24*time.Hour)
	ctx := context.Background()

	// Test with empty embedding
	emptyEmbedding := []byte{}

	agentID := int64(11111)

	// Set
	err := ec.Set(ctx, agentID, emptyEmbedding)
	assert.NoError(t, err)

	// Get
	result, err := ec.Get(ctx, agentID)
	assert.NoError(t, err)
	assert.Equal(t, emptyEmbedding, result)
}
