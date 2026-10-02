// Package cache configures Jetcache for EigenFlux. Cache policy and key ownership
// belong to callers; tiering, admission and miss coalescing belong to Jetcache.
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	jet "github.com/mgtv-tech/jetcache-go"
	"github.com/mgtv-tech/jetcache-go/local"
	"github.com/redis/go-redis/v9"
)

var ErrCacheMiss = jet.ErrCacheMiss
var ErrUnavailable = errors.New("cache backend unavailable")
var ErrReservedValue = errors.New("Jetcache reserves the raw value *")

type Cache interface {
	Get(context.Context, string, any) error
	Set(context.Context, string, any, time.Duration) error
	Delete(context.Context, string) error
	Exists(context.Context, string) (bool, error)
}

type Config struct {
	Name  string
	Redis redis.UniversalClient
	// Local is a Jetcache extension. Nil selects Redis-only caching. Local TTL
	// is a bounded-staleness policy, including after a remote entry expires.
	Local local.Local
	TTL   time.Duration
	// MaxBytes skips remote storage for oversized successful loader results.
	MaxBytes int
}

type Store struct {
	engine jet.Cache
	config Config
	close  sync.Once
}

func New(cfg Config) (*Store, error) {
	if cfg.Name == "" || cfg.TTL <= 0 || (cfg.Redis == nil && cfg.Local == nil) {
		return nil, fmt.Errorf("cache requires name, positive TTL and a backend")
	}
	opts := []jet.Option{jet.WithName(cfg.Name), jet.WithCodec("json"), jet.WithRemoteExpiry(cfg.TTL), jet.WithStatsDisabled(true), jet.WithErrNotFound(ErrReservedValue)}
	if cfg.Redis != nil {
		opts = append(opts, jet.WithRemote(newRemote(cfg.Redis, cfg.MaxBytes)))
	}
	if cfg.Local != nil {
		opts = append(opts, jet.WithLocal(cfg.Local))
	}
	return &Store{engine: jet.New(opts...), config: cfg}, nil
}
func (s *Store) Close() { s.close.Do(s.engine.Close) }
func (s *Store) GetBytes(ctx context.Context, key string) ([]byte, error) {
	var raw []byte
	err := s.engine.Get(ctx, key, &raw)
	return raw, err
}
func (s *Store) Get(ctx context.Context, key string, dest any) error {
	raw, err := s.GetBytes(ctx, key)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dest)
}
func (s *Store) SetBytes(ctx context.Context, key string, raw []byte, ttl time.Duration) error {
	if string(raw) == "*" {
		return ErrReservedValue
	}
	raw = append([]byte(nil), raw...)
	if ttl < 0 {
		return fmt.Errorf("negative cache TTL")
	}
	if s.config.Local != nil && ttl != s.config.TTL {
		return fmt.Errorf("local cache uses its configured fixed TTL")
	}
	// Jetcache rounds subsecond item TTLs. Carry the exact TTL to the adapter;
	// existing Redis wire values and zero (no expiry) semantics stay unchanged.
	ctx = context.WithValue(ctx, writeTTLKey{}, ttl)
	err := s.engine.Set(ctx, key, jet.Value(raw))
	if err != nil {
		s.engine.DeleteFromLocalCache(key)
	}
	return err
}
func (s *Store) Set(ctx context.Context, key string, value any, ttl time.Duration) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.SetBytes(ctx, key, raw, ttl)
}
func (s *Store) Delete(ctx context.Context, key string) error { return s.engine.Delete(ctx, key) }
func (s *Store) Exists(ctx context.Context, key string) (bool, error) {
	_, err := s.GetBytes(ctx, key)
	if errors.Is(err, ErrCacheMiss) {
		return false, nil
	}
	return err == nil, err
}

// Load uses Jetcache.Once for singleflight. The remote adapter records read
// failures so Jetcache's default fallback cannot bypass DB protection. Waiters
// may leave independently; shared work keeps authentication metadata and has a
// fixed timeout. Every caller decodes a private copy after the work completes.
func (s *Store) Load(ctx context.Context, key string, dest any, timeout time.Duration, loader func(context.Context) (any, error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if timeout <= 0 {
		return fmt.Errorf("cache load requires a timeout")
	}
	type result struct {
		raw []byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		work, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
		defer cancel()
		state := &loadState{}
		work = context.WithValue(work, loadStateKey{}, state)
		work = context.WithValue(work, writeTTLKey{}, s.config.TTL)
		var raw []byte
		err := s.engine.Once(work, key, jet.Value(&raw), jet.TTL(-1), jet.Do(func(c context.Context) (any, error) {
			if state.readErr != nil {
				return nil, state.readErr
			}
			value, err := loader(c)
			if err != nil {
				return nil, err
			}
			encoded, err := json.Marshal(value)
			if err != nil {
				return nil, err
			}
			// Once in Jetcache 1.2.6 drops errors from its final remote write.
			// Store in Do so the shared result carries write failures to every
			// waiter; TTL(-1) skips the otherwise duplicate remote write.
			if err := s.SetBytes(c, key, encoded, s.config.TTL); err != nil {
				return nil, err
			}
			return encoded, nil
		}))
		if err != nil {
			s.engine.DeleteFromLocalCache(key)
		}
		done <- result{raw, err}
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case r := <-done:
		if r.err != nil {
			return r.err
		}
		return json.Unmarshal(r.raw, dest)
	}
}

// RedisCache preserves the established JSON API. Service-owned read-through
// caches should construct and reuse Store so Once shares its flight group.
type RedisCache struct{ *Store }

func NewRedisCache(client *redis.Client) *RedisCache {
	// A typed nil is retained to report unavailable rather than panic on reads.
	s, _ := New(Config{Name: "legacy", Redis: client, TTL: time.Hour})
	return &RedisCache{s}
}
