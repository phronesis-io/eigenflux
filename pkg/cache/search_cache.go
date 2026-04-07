package cache

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// SearchCache handles caching of search results with time-bucketed keys
type SearchCache struct {
	cache      Cache
	bucketSize time.Duration // Time bucket size (e.g., 2 seconds)
	ttl        time.Duration // Cache TTL
}

// NewSearchCache creates a new search cache
func NewSearchCache(client *redis.Client, bucketSize, ttl time.Duration) *SearchCache {
	return &SearchCache{
		cache:      NewRedisCache(client),
		bucketSize: bucketSize,
		ttl:        ttl,
	}
}

// CachedItem represents a cached search result item
type CachedItem struct {
	ItemID        string   `json:"item_id"`
	Content       string   `json:"content"`
	Summary       string   `json:"summary"`
	BroadcastType string   `json:"broadcast_type"`
	Domains       []string `json:"domains"`
	Keywords      []string `json:"keywords"`
	Geo           string   `json:"geo"`
	SourceType    string   `json:"source_type"`
	QualityScore  float64  `json:"quality_score"`
	GroupID       int64    `json:"group_id"`
	Lang          string   `json:"lang"`
	Timeliness    string   `json:"timeliness"`
	CreatedAtMs   int64    `json:"created_at_ms"`
	UpdatedAt     int64    `json:"updated_at"`
	UpdatedAtMs   int64    `json:"updated_at_ms"`
	Score         float64  `json:"score"`
}

// BuildCacheKey generates a cache key from search parameters
// Format: cache:search:{hash}:{time_bucket}
func (sc *SearchCache) BuildCacheKey(domains, keywords []string, geo string) string {
	// Normalize to lowercase for case-insensitive caching
	normalizedDomains := make([]string, len(domains))
	for i, d := range domains {
		normalizedDomains[i] = strings.ToLower(d)
	}

	normalizedKeywords := make([]string, len(keywords))
	for i, k := range keywords {
		normalizedKeywords[i] = strings.ToLower(k)
	}

	// Sort arrays for consistent hashing
	sort.Strings(normalizedDomains)
	sort.Strings(normalizedKeywords)

	// Build hash input
	hashInput := fmt.Sprintf("domains:%s|keywords:%s|geo:%s",
		strings.Join(normalizedDomains, ","),
		strings.Join(normalizedKeywords, ","),
		geo,
	)

	// Calculate MD5 hash
	hash := md5.Sum([]byte(hashInput))
	hashStr := hex.EncodeToString(hash[:])

	// Calculate time bucket
	now := time.Now()
	bucket := now.Unix() / int64(sc.bucketSize.Seconds())

	return fmt.Sprintf("cache:search:%s:%d", hashStr, bucket)
}

// Get retrieves cached search results
func (sc *SearchCache) Get(ctx context.Context, key string) ([]CachedItem, error) {
	var items []CachedItem
	if err := sc.cache.Get(ctx, key, &items); err != nil {
		return nil, err
	}
	return items, nil
}

// Set stores search results in cache
func (sc *SearchCache) Set(ctx context.Context, key string, items []CachedItem) error {
	return sc.cache.Set(ctx, key, items, sc.ttl)
}

// FilterByTimestamp filters cached items by timestamp
// Returns items with updated_at > lastFetchTime
func FilterByTimestamp(items []CachedItem, lastFetchTime int64) []CachedItem {
	if lastFetchTime == 0 {
		return items
	}

	filtered := make([]CachedItem, 0, len(items))
	for _, item := range items {
		if item.UpdatedAt > lastFetchTime {
			filtered = append(filtered, item)
		}
	}
	return filtered
}
