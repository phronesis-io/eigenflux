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

const ContentHashTTL = 30 * 24 * time.Hour

// ComputeContentHash computes the MD5 hash of content
func ComputeContentHash(content string) string {
	hash := md5.Sum([]byte(content))
	return hex.EncodeToString(hash[:])
}

// HashExists reports whether the content hash gate exists without depending on
// the legacy group_id value stored in that key.
func HashExists(ctx context.Context, rdb *redis.Client, hash string) (bool, error) {
	count, err := rdb.Exists(ctx, hashKey(hash)).Result()
	return count > 0, err
}

// CheckHashExists checks if hash exists, returns (exists, group_id, error)
func CheckHashExists(ctx context.Context, rdb *redis.Client, hash string) (bool, int64, error) {
	val, err := rdb.Get(ctx, hashKey(hash)).Result()
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
	return rdb.Set(ctx, hashKey(hash), groupID, ContentHashTTL).Err()
}

func hashKey(hash string) string {
	return fmt.Sprintf("dedup:hash:%s", hash)
}
