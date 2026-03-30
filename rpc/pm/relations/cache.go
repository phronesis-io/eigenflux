package relations

import (
	"context"
	"fmt"
	"time"

	"eigenflux_server/rpc/pm/dal"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

const (
	RedisKeyFriendSet   = "friend:%d"       // SET of friend IDs
	RedisKeyBlockSet    = "block:%d"        // SET of blocked user IDs
	RedisKeyFriendCount = "friend_count:%d" // STRING friend count cache

	// Cache TTL for friend and block sets (24 hours)
	RelationCacheTTL = 24 * time.Hour
)

// LoadFriendSet loads friend IDs from DB to Redis SET
func LoadFriendSet(ctx context.Context, rdb *redis.Client, db *gorm.DB, uid int64) error {
	var relations []dal.UserRelation
	err := db.Where("from_uid = ? AND rel_type = ?", uid, dal.RelTypeFriend).Find(&relations).Error
	if err != nil {
		return err
	}

	key := fmt.Sprintf(RedisKeyFriendSet, uid)
	pipe := rdb.Pipeline()
	pipe.Del(ctx, key)

	if len(relations) > 0 {
		members := make([]interface{}, len(relations))
		for i, rel := range relations {
			members[i] = rel.ToUID
		}
		pipe.SAdd(ctx, key, members...)
		pipe.Expire(ctx, key, RelationCacheTTL)
	}

	_, err = pipe.Exec(ctx)
	return err
}

// LoadBlockSet loads blocked user IDs from DB to Redis SET
func LoadBlockSet(ctx context.Context, rdb *redis.Client, db *gorm.DB, uid int64) error {
	var relations []dal.UserRelation
	err := db.Where("from_uid = ? AND rel_type = ?", uid, dal.RelTypeBlock).Find(&relations).Error
	if err != nil {
		return err
	}

	key := fmt.Sprintf(RedisKeyBlockSet, uid)
	pipe := rdb.Pipeline()
	pipe.Del(ctx, key)

	if len(relations) > 0 {
		members := make([]interface{}, len(relations))
		for i, rel := range relations {
			members[i] = rel.ToUID
		}
		pipe.SAdd(ctx, key, members...)
		pipe.Expire(ctx, key, RelationCacheTTL)
	}

	_, err = pipe.Exec(ctx)
	return err
}

// IsFriendCached checks if two users are friends via Redis cache with DB fallback
func IsFriendCached(ctx context.Context, rdb *redis.Client, db *gorm.DB, uidA, uidB int64) (bool, error) {
	key := fmt.Sprintf(RedisKeyFriendSet, uidA)
	exists, err := rdb.Exists(ctx, key).Result()
	if err != nil {
		return dal.IsFriend(db, uidA, uidB)
	}

	if exists == 0 {
		if err := LoadFriendSet(ctx, rdb, db, uidA); err != nil {
			return dal.IsFriend(db, uidA, uidB)
		}
	}

	isMember, err := rdb.SIsMember(ctx, key, uidB).Result()
	if err != nil {
		return dal.IsFriend(db, uidA, uidB)
	}
	return isMember, nil
}

// IsBlockedCached checks if fromUID has blocked toUID via Redis cache with DB fallback
func IsBlockedCached(ctx context.Context, rdb *redis.Client, db *gorm.DB, fromUID, toUID int64) (bool, error) {
	key := fmt.Sprintf(RedisKeyBlockSet, fromUID)
	exists, err := rdb.Exists(ctx, key).Result()
	if err != nil {
		return dal.IsBlocked(db, fromUID, toUID)
	}

	if exists == 0 {
		if err := LoadBlockSet(ctx, rdb, db, fromUID); err != nil {
			return dal.IsBlocked(db, fromUID, toUID)
		}
	}

	isMember, err := rdb.SIsMember(ctx, key, toUID).Result()
	if err != nil {
		return dal.IsBlocked(db, fromUID, toUID)
	}
	return isMember, nil
}

// InvalidateFriendCache deletes friend cache keys
func InvalidateFriendCache(ctx context.Context, rdb *redis.Client, uid int64) error {
	keys := []string{
		fmt.Sprintf(RedisKeyFriendSet, uid),
		fmt.Sprintf(RedisKeyFriendCount, uid),
	}
	return rdb.Del(ctx, keys...).Err()
}

// InvalidateBlockCache deletes block cache key
func InvalidateBlockCache(ctx context.Context, rdb *redis.Client, uid int64) error {
	key := fmt.Sprintf(RedisKeyBlockSet, uid)
	return rdb.Del(ctx, key).Err()
}



