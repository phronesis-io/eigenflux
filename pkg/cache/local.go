package cache

import (
	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/mgtv-tech/jetcache-go/local"
	"time"
)

// Local is a typed adapter around HashiCorp LRU for immutable domain values.
// Mutable slices/maps must be cloned by their owner. Expiry uses caller clocks.
type Local[K comparable, V any] struct{ entries *lru.Cache[K, localValue[V]] }
type localValue[V any] struct {
	value V
	until time.Time
}

func NewLocal[K comparable, V any](capacity int) *Local[K, V] {
	c, err := lru.New[K, localValue[V]](capacity)
	if err != nil {
		panic(err)
	}
	return &Local[K, V]{c}
}
func (l *Local[K, V]) GetAt(key K, now time.Time) (V, bool) {
	e, ok := l.entries.Get(key)
	if ok && (e.until.IsZero() || now.Before(e.until)) {
		return e.value, true
	}
	var zero V
	return zero, false
}
func (l *Local[K, V]) Get(key K) (V, bool) { return l.GetAt(key, time.Now()) }
func (l *Local[K, V]) PutUntil(key K, v V, until time.Time) {
	l.entries.Add(key, localValue[V]{v, until})
}
func (l *Local[K, V]) Delete(key K) { l.entries.Remove(key) }
func (l *Local[K, V]) Clear()       { l.entries.Purge() }

type localLRU struct {
	entries *Local[string, []byte]
	ttl     time.Duration
}

// NewLRU provides Jetcache's Local extension using the existing LRU dependency.
func NewLRU(capacity int, ttl time.Duration) local.Local {
	if ttl <= 0 {
		panic("LRU TTL must be positive")
	}
	return &localLRU{NewLocal[string, []byte](capacity), ttl}
}
func (l *localLRU) Get(key string) ([]byte, bool) {
	b, ok := l.entries.Get(key)
	return append([]byte(nil), b...), ok
}
func (l *localLRU) Set(key string, b []byte) {
	l.entries.PutUntil(key, append([]byte(nil), b...), time.Now().Add(l.ttl))
}
func (l *localLRU) Del(key string) { l.entries.Delete(key) }

// NewTinyLFU uses Jetcache's built-in Ristretto adapter. Capacity counts entries;
// admission can reject new values. Disable TTL jitter for explicit expiry bounds.
func NewTinyLFU(capacity int, ttl time.Duration) local.Local {
	if capacity <= 0 || ttl <= 0 {
		panic("TinyLFU capacity and TTL must be positive")
	}
	c := local.NewTinyLFU(capacity, ttl)
	c.UseRandomizedTTL(0)
	return c
}
