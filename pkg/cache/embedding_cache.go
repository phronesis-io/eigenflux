package cache

import (
	"context"
	"eigenflux_server/pkg/cache/keys"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// EmbeddingCache stores profile embeddings as raw bytes in Redis.
// Separate from ProfileCache to keep the hot path lean.
type EmbeddingCache struct {
	client *redis.Client
	ttl    time.Duration
}

// NewEmbeddingCache creates a new embedding cache
func NewEmbeddingCache(client *redis.Client, ttl time.Duration) *EmbeddingCache {
	return &EmbeddingCache{
		client: client,
		ttl:    ttl,
	}
}

// BuildKey generates a cache key for an embedding
// Format: cache:profile:emb:{agent_id}
func (ec *EmbeddingCache) BuildKey(agentID int64) string {
	return fmt.Sprintf(keys.Embedding, agentID)
}

// Get retrieves a cached embedding as raw bytes
func (ec *EmbeddingCache) Get(ctx context.Context, agentID int64) ([]byte, error) {
	val, err := Redis(ec.client).Read(ctx, ec.BuildKey(agentID)).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, ErrCacheMiss
		}
		return nil, fmt.Errorf("redis get failed: %w", err)
	}
	return []byte(val), nil
}

// Set stores an embedding as raw bytes in cache
func (ec *EmbeddingCache) Set(ctx context.Context, agentID int64, raw []byte) error {
	if err := Redis(ec.client).Write(ctx, ec.BuildKey(agentID), raw, ec.ttl).Err(); err != nil {
		return fmt.Errorf("redis set failed: %w", err)
	}
	return nil
}

// Delete removes an embedding from cache
func (ec *EmbeddingCache) Delete(ctx context.Context, agentID int64) error {
	if err := Redis(ec.client).Remove(ctx, ec.BuildKey(agentID)).Err(); err != nil {
		return fmt.Errorf("redis delete failed: %w", err)
	}
	return nil
}
