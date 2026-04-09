package cache

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// ProfileCache handles caching of user profiles
type ProfileCache struct {
	cache Cache
	ttl   time.Duration
}

// NewProfileCache creates a new profile cache
func NewProfileCache(client *redis.Client, ttl time.Duration) *ProfileCache {
	return &ProfileCache{
		cache: NewRedisCache(client),
		ttl:   ttl,
	}
}

// CachedProfile represents a cached user profile
type CachedProfile struct {
	AgentID    int64    `json:"agent_id"`
	Keywords   []string `json:"keywords"`
	Domains    []string `json:"domains"`
	Geo        string   `json:"geo"`
	GeoCountry string   `json:"geo_country"`
}

// BuildProfileKey generates a cache key for a profile
// Format: cache:profile:{agent_id}
func (pc *ProfileCache) BuildProfileKey(agentID int64) string {
	return fmt.Sprintf("cache:profile:%d", agentID)
}

// Get retrieves a cached profile
func (pc *ProfileCache) Get(ctx context.Context, agentID int64) (*CachedProfile, error) {
	key := pc.BuildProfileKey(agentID)
	var profile CachedProfile
	if err := pc.cache.Get(ctx, key, &profile); err != nil {
		return nil, err
	}
	return &profile, nil
}

// Set stores a profile in cache
func (pc *ProfileCache) Set(ctx context.Context, profile *CachedProfile) error {
	key := pc.BuildProfileKey(profile.AgentID)
	return pc.cache.Set(ctx, key, profile, pc.ttl)
}

// Delete removes a profile from cache
func (pc *ProfileCache) Delete(ctx context.Context, agentID int64) error {
	key := pc.BuildProfileKey(agentID)
	return pc.cache.Delete(ctx, key)
}
