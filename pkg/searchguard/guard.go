// Package searchguard bounds private-search work before it reaches storage.
package searchguard

import (
	"context"
	"crypto/sha256"
	"eigenflux_server/pkg/cache/keys"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"eigenflux_server/pkg/cache"
	"github.com/redis/go-redis/v9"
)

const (
	CacheTTL      = 5 * time.Second
	RateWindow    = 10 * time.Second
	OwnerLimit    = 30
	MaxConcurrent = 16
	maxCacheBytes = 256 << 10
)

// Scope names are part of shared cache/rate keys and must stay stable.
const (
	Dashboard   = "dashboard"
	Messages    = "pm-message"
	Friends     = "pm-friend"
	Broadcasts  = "item-broadcast"
	AgentNames  = "profile-name"
	Commissions = "commission-commission"
	Orders      = "commission-order"
)

var (
	ErrLimited     = errors.New("search rate or concurrency limit exceeded")
	ErrUnavailable = errors.New("search protection unavailable")
)

// Allow uses a shared Redis counter so replicas share the same owner budget.
// A Redis failure fails closed instead of shifting unbounded traffic to SQL.
func Allow(ctx context.Context, r *redis.Client, scope string, owner int64, limit int, window time.Duration) error {
	if r == nil {
		return ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	key := fmt.Sprintf(keys.SearchRate, scope, owner)
	n, err := increment.Run(ctx, r, []string{key}, window.Milliseconds()).Int64()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if n > int64(limit) {
		return ErrLimited
	}
	return nil
}

var increment = redis.NewScript(`
local n=redis.call('INCR',KEYS[1])
if n==1 then redis.call('PEXPIRE',KEYS[1],ARGV[1]) end
return n
`)

// Guard is owned by one service instance. Cache and rate budgets are shared
// through Redis; singleflight and the concurrency ceiling are process-local.
type Guard struct {
	cacheMu sync.Mutex
	caches  map[*redis.Client]*cache.Store
	once    sync.Once
	slots   chan struct{}
}

func (g *Guard) acquire() bool {
	g.once.Do(func() { g.slots = make(chan struct{}, MaxConcurrent) })
	select {
	case g.slots <- struct{}{}:
		return true
	default:
		return false
	}
}

// Load returns a separate decoded value to every caller, including waiters.
// Identity must include every query/filter/cursor/limit input. Private callers
// authenticate before Load and revalidate current visibility in validate. That
// callback runs for every caller, including cache hits and flight waiters.
func (g *Guard) Load(ctx context.Context, r *redis.Client, scope string, owner int64, identity any, dest any, load func(context.Context) (any, error), validate func(context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := Allow(ctx, r, scope, owner, OwnerLimit, RateWindow); err != nil {
		return err
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(encoded)
	key := fmt.Sprintf(keys.SearchResult, scope, owner, hex.EncodeToString(sum[:]))
	g.cacheMu.Lock()
	if g.caches == nil {
		g.caches = make(map[*redis.Client]*cache.Store)
	}
	c := g.caches[r]
	if c == nil {
		c, _ = cache.New(cache.Config{Name: "private-search", Redis: r, TTL: CacheTTL, MaxBytes: maxCacheBytes})
		g.caches[r] = c
	}
	g.cacheMu.Unlock()
	err = c.Load(ctx, key, dest, 3*time.Second, func(work context.Context) (any, error) {
		if !g.acquire() {
			return nil, ErrLimited
		}
		defer func() { <-g.slots }()
		if err := Allow(work, r, scope+"-fills", 0, 100, time.Second); err != nil {
			return nil, err
		}
		return load(work)
	})
	if errors.Is(err, cache.ErrUnavailable) {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if err != nil {
		return err
	}
	if validate != nil {
		if !g.acquire() {
			return ErrLimited
		}
		defer func() { <-g.slots }()
		check, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		return validate(check)
	}
	return nil
}
