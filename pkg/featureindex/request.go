package featureindex

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/redis/go-redis/v9"
)

type requestKey struct {
	client               *redis.Client
	namespace, component string
	id                   int64
	plans                *sync.Map
}
type requestCache struct {
	mu     sync.Mutex
	values map[requestKey]json.RawMessage
	bytes  int
}
type requestCacheKey struct{}

// WithRequestCache shares raw feature snapshots across Need contexts only for
// this execution. Generation and immutable YAML snapshot identity isolate reads.
// It does not cache source errors, query scores, filters or relationship checks.
func WithRequestCache(ctx context.Context) context.Context {
	return context.WithValue(ctx, requestCacheKey{}, &requestCache{values: map[requestKey]json.RawMessage{}})
}
func requestCacheFrom(ctx context.Context) *requestCache {
	c, _ := ctx.Value(requestCacheKey{}).(*requestCache)
	return c
}
func (c *requestCache) get(key requestKey) (json.RawMessage, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.values[key]
	return v, ok
}
func (c *requestCache) put(key requestKey, value json.RawMessage) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	old, exists := c.values[key]
	// Bound both metadata cardinality and payload bytes for large executions.
	if !exists && len(c.values) >= 4096 || c.bytes-len(old)+len(value) > 4<<20 {
		return
	}
	c.values[key] = value
	c.bytes += len(value) - len(old)
}

func (c *requestCache) remove(key requestKey) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bytes -= len(c.values[key])
	delete(c.values, key)
}

// EnsureRequestCache preserves an execution cache or creates one for standalone hydration.
func EnsureRequestCache(ctx context.Context) context.Context {
	if requestCacheFrom(ctx) != nil {
		return ctx
	}
	return WithRequestCache(ctx)
}
