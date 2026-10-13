package cache

import (
	"context"
	"github.com/redis/go-redis/v9"
	"time"
)

// ReplaceSet is the Redis-only relation projection adapter. SET operations are
// kept atomic; they cannot be represented as independent local key/value reads.
func (r *RedisBackend) ReplaceSet(ctx context.Context, key string, members []any, ttl time.Duration) error {
	if nilClient(r.Client) {
		return ErrUnavailable
	}
	_, err := r.Client.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.Del(ctx, key)
		if len(members) > 0 {
			p.SAdd(ctx, key, members...)
			p.Expire(ctx, key, ttl)
		}
		return nil
	})
	return err
}
func (r *RedisBackend) Exists(ctx context.Context, key string) (bool, error) {
	if nilClient(r.Client) {
		return false, ErrUnavailable
	}
	n, err := r.Client.Exists(ctx, key).Result()
	return n > 0, err
}
func (r *RedisBackend) IsMember(ctx context.Context, key string, member any) (bool, error) {
	if nilClient(r.Client) {
		return false, ErrUnavailable
	}
	return r.Client.SIsMember(ctx, key, member).Result()
}
