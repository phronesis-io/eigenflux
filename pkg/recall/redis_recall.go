package recall

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisRecallReader reads recall indices written by the offline pipeline.
type RedisRecallReader struct {
	rdb       *redis.Client
	namespace string // key prefix, typically "rec"

	mu          sync.RWMutex
	cache       map[string]cacheEntry
	scoredCache map[string]scoredCacheEntry
}

type cacheEntry struct {
	data      []int64
	fetchedAt time.Time
}

type scoredCacheEntry struct {
	data      []ScoredCandidate
	fetchedAt time.Time
}

// ScoredCandidate is one item candidate with an optional precomputed score
// from an offline recall job.
type ScoredCandidate struct {
	ItemID int64
	Score  float64
}

const cacheTTL = 30 * time.Second

// NewRedisRecallReader creates a new reader with the given Redis client and namespace.
func NewRedisRecallReader(rdb *redis.Client, namespace string) *RedisRecallReader {
	return &RedisRecallReader{
		rdb:         rdb,
		namespace:   namespace,
		cache:       make(map[string]cacheEntry),
		scoredCache: make(map[string]scoredCacheEntry),
	}
}

// FetchItemIDIndex returns item IDs from an item_id_index recall (hot_recall, new_recall).
func (r *RedisRecallReader) FetchItemIDIndex(ctx context.Context, key string) ([]int64, error) {
	cacheKey := "idx:" + key
	if cached, ok := r.getCache(cacheKey); ok {
		return cached, nil
	}

	version, err := r.activeVersion(ctx, key)
	if err != nil {
		return nil, err
	}

	redisKey := fmt.Sprintf("%s:%s:%s:index", r.namespace, key, version)
	val, err := r.rdb.Get(ctx, redisKey).Result()
	if err != nil {
		if err == redis.Nil {
			r.setCache(cacheKey, []int64{})
			return nil, nil
		}
		return nil, fmt.Errorf("recall: get %s: %w", redisKey, err)
	}

	ids, err := parseIDList(val)
	if err != nil {
		return nil, fmt.Errorf("recall: parse %s: %w", redisKey, err)
	}
	r.setCache(cacheKey, ids)
	return ids, nil
}

// FetchUserScoredCandidates returns scored item candidates for one user from a
// user_scored_candidates recall output.
func (r *RedisRecallReader) FetchUserScoredCandidates(ctx context.Context, key, userID string) ([]ScoredCandidate, error) {
	cacheKey := fmt.Sprintf("scored:%s:%s", key, userID)
	if cached, ok := r.getScoredCache(cacheKey); ok {
		return cached, nil
	}

	version, err := r.activeVersion(ctx, key)
	if err != nil {
		return nil, err
	}

	redisKey := fmt.Sprintf("%s:%s:%s:user:%s:scored_candidates", r.namespace, key, version, userID)
	val, err := r.rdb.Get(ctx, redisKey).Result()
	if err != nil {
		if err == redis.Nil {
			r.setScoredCache(cacheKey, []ScoredCandidate{})
			return nil, nil
		}
		return nil, fmt.Errorf("recall: get %s: %w", redisKey, err)
	}

	candidates, err := parseScoredCandidateList(val)
	if err != nil {
		return nil, fmt.Errorf("recall: parse %s: %w", redisKey, err)
	}
	r.setScoredCache(cacheKey, candidates)
	return candidates, nil
}

func (r *RedisRecallReader) activeVersion(ctx context.Context, key string) (string, error) {
	versionKey := fmt.Sprintf("%s:%s:active_version", r.namespace, key)
	version, err := r.rdb.Get(ctx, versionKey).Result()
	if err != nil {
		if err == redis.Nil {
			return "", fmt.Errorf("recall: no active version for %s", key)
		}
		return "", fmt.Errorf("recall: get version %s: %w", versionKey, err)
	}
	return version, nil
}

func (r *RedisRecallReader) getCache(key string) ([]int64, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.cache[key]
	if !ok || time.Since(entry.fetchedAt) > cacheTTL {
		return nil, false
	}
	if len(entry.data) == 0 {
		return nil, true // cached empty result
	}
	return entry.data, true
}

func (r *RedisRecallReader) setCache(key string, data []int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cache[key] = cacheEntry{data: data, fetchedAt: time.Now()}
}

func (r *RedisRecallReader) getScoredCache(key string) ([]ScoredCandidate, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.scoredCache[key]
	if !ok || time.Since(entry.fetchedAt) > cacheTTL {
		return nil, false
	}
	if len(entry.data) == 0 {
		return nil, true // cached empty result
	}
	return entry.data, true
}

func (r *RedisRecallReader) setScoredCache(key string, data []ScoredCandidate) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	// Evict expired entries to prevent unbounded growth from per-user keys.
	for k, v := range r.scoredCache {
		if now.Sub(v.fetchedAt) > cacheTTL {
			delete(r.scoredCache, k)
		}
	}
	r.scoredCache[key] = scoredCacheEntry{data: data, fetchedAt: now}
}

// parseIDList parses a comma-separated list of int64 IDs.
func parseIDList(s string) ([]int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	ids := make([]int64, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		id, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid id %q: %w", p, err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// parseScoredCandidateList parses comma-separated "item_id:score" pairs.
func parseScoredCandidateList(s string) ([]ScoredCandidate, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	candidates := make([]ScoredCandidate, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		itemIDPart, scorePart, ok := strings.Cut(p, ":")
		if !ok {
			return nil, fmt.Errorf("invalid candidate %q", p)
		}
		itemID, err := strconv.ParseInt(strings.TrimSpace(itemIDPart), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid candidate item id %q: %w", itemIDPart, err)
		}
		score, err := strconv.ParseFloat(strings.TrimSpace(scorePart), 64)
		if err != nil {
			return nil, fmt.Errorf("invalid candidate score %q: %w", scorePart, err)
		}
		candidates = append(candidates, ScoredCandidate{ItemID: itemID, Score: score})
	}
	return candidates, nil
}
