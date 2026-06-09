package dedup

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// ComputeContentHash computes the MD5 hash of content
func ComputeContentHash(content string) string {
	hash := md5.Sum([]byte(content))
	return hex.EncodeToString(hash[:])
}

// CheckHashExists checks if hash exists, returns (exists, group_id, error)
func CheckHashExists(ctx context.Context, rdb *redis.Client, hash string) (bool, int64, error) {
	key := fmt.Sprintf("dedup:hash:%s", hash)
	val, err := rdb.Get(ctx, key).Result()
	if err == redis.Nil {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, err
	}
	groupID, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		return false, 0, fmt.Errorf("invalid group_id in dedup hash: %s", val)
	}
	return true, groupID, nil
}

// SaveHash saves hash and corresponding group_id, TTL 30 days
func SaveHash(ctx context.Context, rdb *redis.Client, hash string, groupID int64) error {
	key := fmt.Sprintf("dedup:hash:%s", hash)
	return rdb.Set(ctx, key, groupID, 30*24*time.Hour).Err()
}
