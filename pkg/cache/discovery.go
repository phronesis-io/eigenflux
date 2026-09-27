package cache

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/metrics"
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
)

const DiscoveryInputTTL = 30 * time.Second
const DiscoveryEmptyTTL = 5 * time.Second

// DiscoveryCache shares immutable input/compiled values across Sort instances.
// Every caller decodes its own copy; the flight group never shares mutable DTOs.
type DiscoveryCache struct {
	Redis   *redis.Client
	flights singleflight.Group
}

type discoveryValue struct {
	Until int64           `json:"until"`
	Data  json.RawMessage `json:"data"`
}

func discoveryGenerationKey(owner int64) string {
	return fmt.Sprintf("cache:discovery:v1:{%d}:generation", owner)
}
func discoveryToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// InvalidateDiscovery runs after source commits. Replacing the generation rather
// than deleting values prevents an older in-flight reader from refilling the
// current namespace. TTL bounds staleness if invalidation cannot reach Redis.
// Cache failure must not turn an already committed capture into a failed write.
func InvalidateDiscovery(ctx context.Context, r *redis.Client, owner int64) {
	if r == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 500*time.Millisecond)
	defer cancel()
	token, err := discoveryToken()
	if err == nil {
		err = r.Set(ctx, discoveryGenerationKey(owner), token, 24*time.Hour).Err()
	}
	if err != nil {
		metrics.DiscoveryContextCache.WithLabelValues("invalidate", "error").Inc()
		logger.Ctx(ctx).Warn("discovery cache invalidation failed", "err", err)
	}
}

func (c *DiscoveryCache) generation(ctx context.Context, owner int64) (string, error) {
	key := discoveryGenerationKey(owner)
	v, err := c.Redis.Get(ctx, key).Result()
	if err != redis.Nil {
		return v, err
	}
	token, err := discoveryToken()
	if err != nil {
		return "", err
	}
	won, err := c.Redis.SetNX(ctx, key, token, 24*time.Hour).Result()
	if err != nil {
		return "", err
	}
	if won {
		return token, nil
	}
	return c.Redis.Get(ctx, key).Result()
}

// Load caches a value until its TTL or its earliest absolute deadline. scope is
// a bounded label (owner, needs, compiled); identity is an opaque content hash.
// Authoritative errors are propagated and never cached. Redis failures bypass
// this optional cache; they do not reinterpret a failed DB read as empty input.
func (c *DiscoveryCache) Load(ctx context.Context, owner int64, scope, identity string, now int64, ttl time.Duration, dest any, load func(context.Context) (any, int64, error)) error {
	build := func(ctx context.Context) ([]byte, error) {
		value, deadline, err := load(ctx)
		if err != nil {
			return nil, err
		}
		until := now + ttl.Milliseconds()
		if deadline > 0 && deadline < until {
			until = deadline
		}
		data, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		return json.Marshal(discoveryValue{Until: until, Data: data})
	}
	decode := func(raw []byte) error {
		var v discoveryValue
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		return json.Unmarshal(v.Data, dest)
	}
	if c == nil || c.Redis == nil {
		raw, err := build(ctx)
		if err != nil {
			return err
		}
		return decode(raw)
	}
	epoch, err := c.generation(ctx, owner)
	if err != nil {
		metrics.DiscoveryContextCache.WithLabelValues(scope, "error").Inc()
		raw, err := build(ctx)
		if err != nil {
			return err
		}
		return decode(raw)
	}
	key := fmt.Sprintf("cache:discovery:v1:{%d}:%s:%s:%s", owner, epoch, scope, identity)
	read := func(ctx context.Context) ([]byte, bool) {
		raw, err := c.Redis.Get(ctx, key).Bytes()
		if err == redis.Nil {
			return nil, false
		}
		var value discoveryValue
		if err != nil || json.Unmarshal(raw, &value) != nil || value.Until <= now || len(value.Data) == 0 {
			return nil, false
		}
		return raw, true
	}
	if raw, ok := read(ctx); ok {
		metrics.DiscoveryContextCache.WithLabelValues(scope, "hit").Inc()
		return decode(raw)
	}
	metrics.DiscoveryContextCache.WithLabelValues(scope, "miss").Inc()
	result := c.flights.DoChan(key, func() (any, error) {
		// A disconnected leader cannot cancel work needed by other waiting requests.
		shared, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		if raw, ok := read(shared); ok {
			return raw, nil
		}
		raw, err := build(shared)
		if err != nil {
			return nil, err
		}
		if err := c.Redis.Set(shared, key, raw, ttl).Err(); err != nil {
			metrics.DiscoveryContextCache.WithLabelValues(scope, "error").Inc()
		}
		return raw, nil
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case r := <-result:
		if r.Err != nil {
			return r.Err
		}
		raw := r.Val.([]byte)
		var value discoveryValue
		if err := json.Unmarshal(raw, &value); err != nil {
			return err
		}
		if value.Until <= now {
			// A waiter may have a later request time than the leader (e.g. a Need
			// crossed its deadline). Re-select instead of reusing that old result.
			raw, err = build(ctx)
			if err != nil {
				return err
			}
		}
		return decode(raw)
	}
}
