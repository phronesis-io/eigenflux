package bloomfilter

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// BloomFilterKeyPrefix is the prefix for bloom filter keys
	BloomFilterKeyPrefix = "bf:global:"
	// BloomFilterTTL is the TTL for bloom filter keys (30 days)
	BloomFilterTTL = 30 * 24 * time.Hour
	// BloomFilterLookbackDays is the number of daily buckets CheckExists scans
	BloomFilterLookbackDays = 30
	// BloomFilterErrorRate is the target false positive rate
	BloomFilterErrorRate = 0.01
	// BloomFilterCapacity is the expected number of items
	BloomFilterCapacity = 1000000
)

// BloomFilter provides global rolling bloom filter for deduplication
type BloomFilter struct {
	rdb *redis.Client
}

// NewBloomFilter creates a new BloomFilter instance
func NewBloomFilter(rdb *redis.Client) *BloomFilter {
	return &BloomFilter{rdb: rdb}
}

// GetKeyForDate returns the bloom filter key for a specific date
func GetKeyForDate(date time.Time) string {
	return fmt.Sprintf("%s%s", BloomFilterKeyPrefix, date.Format("20060102"))
}

// Add adds items to today's bloom filter
func (bf *BloomFilter) Add(ctx context.Context, agentID int64, groupIDs []int64) error {
	if len(groupIDs) == 0 {
		return nil
	}

	key := GetKeyForDate(time.Now())
	values := make([]interface{}, len(groupIDs))
	for i, gid := range groupIDs {
		values[i] = fmt.Sprintf("%d:%d", agentID, gid)
	}

	// Use BF.MADD if RedisBloom is available, otherwise use SADD as fallback
	pipe := bf.rdb.Pipeline()
	pipe.SAdd(ctx, key, values...)
	pipe.Expire(ctx, key, BloomFilterTTL)
	_, err := pipe.Exec(ctx)
	return err
}

// CheckExists checks if items exist in any of the last BloomFilterLookbackDays days' bloom filters
func (bf *BloomFilter) CheckExists(ctx context.Context, agentID int64, groupIDs []int64) (map[int64]bool, error) {
	if len(groupIDs) == 0 {
		return make(map[int64]bool), nil
	}

	// Deduplicate groupIDs
	seen := make(map[int64]struct{}, len(groupIDs))
	uniqueGIDs := make([]int64, 0, len(groupIDs))
	for _, gid := range groupIDs {
		if _, ok := seen[gid]; !ok {
			seen[gid] = struct{}{}
			uniqueGIDs = append(uniqueGIDs, gid)
		}
	}

	// Build member values once
	values := make([]interface{}, len(uniqueGIDs))
	for i, gid := range uniqueGIDs {
		values[i] = fmt.Sprintf("%d:%d", agentID, gid)
	}

	// Pipeline: BloomFilterLookbackDays days × 1 SMIsMember each → 1 RTT
	now := time.Now()
	pipe := bf.rdb.Pipeline()
	cmds := make([]*redis.BoolSliceCmd, BloomFilterLookbackDays)
	for i := 0; i < BloomFilterLookbackDays; i++ {
		date := now.AddDate(0, 0, -i)
		key := GetKeyForDate(date)
		cmds[i] = pipe.SMIsMember(ctx, key, values...)
	}
	_, err := pipe.Exec(ctx)
	if err != nil && err != redis.Nil {
		return nil, fmt.Errorf("failed to check bloom filter: %w", err)
	}

	result := make(map[int64]bool)
	for _, cmd := range cmds {
		bools, err := cmd.Result()
		if err != nil {
			continue
		}
		for j, exists := range bools {
			if exists {
				result[uniqueGIDs[j]] = true
			}
		}
	}

	return result, nil
}

