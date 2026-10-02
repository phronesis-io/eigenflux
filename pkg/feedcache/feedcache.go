package feedcache

import (
	"eigenflux_server/pkg/cache"
	"github.com/redis/go-redis/v9"
)

type Entry = cache.FeedEntry
type FeedCache = cache.FeedCache

const FeedCacheTTL = cache.FeedCacheTTL
const FeedCacheKeyPrefix = cache.FeedCacheKeyPrefix

func GetKey(agentID int64) string               { return cache.FeedKey(agentID) }
func NewFeedCache(rdb *redis.Client) *FeedCache { return cache.NewFeedCache(rdb) }
