package cache

import (
	"context"
	"errors"
	"github.com/mgtv-tech/jetcache-go/remote"
	"reflect"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Connections owns process-wide Redis pools. Call Close once at process shutdown.
// Borrowed clients passed to RedisBackend are never closed by a cache.
type Connections struct {
	mu      sync.Mutex
	clients map[string]*redis.Client
}

func (p *Connections) Client(addr, password string) *redis.Client {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.clients == nil {
		p.clients = make(map[string]*redis.Client)
	}
	key := addr + "\x00" + password
	if c := p.clients[key]; c != nil {
		return c
	}
	c := redis.NewClient(&redis.Options{Addr: addr, Password: password, ContextTimeoutEnabled: true})
	p.clients[key] = c
	return c
}
func (p *Connections) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	var result error
	for _, c := range p.clients {
		result = errors.Join(result, c.Close())
	}
	p.clients = nil
	return result
}

var SharedConnections Connections

// RedisBackend centralizes raw cache access and borrows its client's lifecycle.
type RedisBackend struct{ Client redis.UniversalClient }

func Redis(client redis.UniversalClient) *RedisBackend { return &RedisBackend{Client: client} }
func (r *RedisBackend) Read(ctx context.Context, key string) *redis.StringCmd {
	if nilClient(r.Client) {
		return redis.NewStringResult("", ErrUnavailable)
	}
	return r.Client.Get(ctx, key)
}
func (r *RedisBackend) Write(ctx context.Context, key string, value any, ttl time.Duration) *redis.StatusCmd {
	if nilClient(r.Client) {
		return redis.NewStatusResult("", ErrUnavailable)
	}
	return r.Client.Set(ctx, key, value, ttl)
}
func (r *RedisBackend) Remove(ctx context.Context, keys ...string) *redis.IntCmd {
	if nilClient(r.Client) {
		return redis.NewIntResult(0, ErrUnavailable)
	}
	return r.Client.Del(ctx, keys...)
}

func nilClient(c redis.UniversalClient) bool {
	if c == nil {
		return true
	}
	v := reflect.ValueOf(c)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

type writeTTLKey struct{}
type loadStateKey struct{}
type loadState struct{ readErr error }
type jetRemote struct {
	remote.Remote
	client   redis.UniversalClient
	maxBytes int
}

func newRemote(c redis.UniversalClient, maxBytes int) *jetRemote {
	return &jetRemote{remote.NewGoRedisV9Adapter(c), c, maxBytes}
}
func (r *jetRemote) Get(ctx context.Context, key string) (string, error) {
	read, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	value, err := Redis(r.client).Read(read, key).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		err = errors.Join(ErrUnavailable, err)
		if state, _ := ctx.Value(loadStateKey{}).(*loadState); state != nil {
			state.readErr = err
		}
	}
	return value, err
}
func (r *jetRemote) SetEX(ctx context.Context, key string, value any, ttl time.Duration) error {
	if b, ok := value.([]byte); ok && r.maxBytes > 0 && len(b) > r.maxBytes {
		return nil
	}
	if exact, ok := ctx.Value(writeTTLKey{}).(time.Duration); ok {
		ttl = exact
	}
	write, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	if err := Redis(r.client).Write(write, key, value, ttl).Err(); err != nil {
		return errors.Join(ErrUnavailable, err)
	}
	return nil
}
func (r *jetRemote) Del(ctx context.Context, key string) (int64, error) {
	return Redis(r.client).Remove(ctx, key).Result()
}
