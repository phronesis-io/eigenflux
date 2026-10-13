# Shared cache package

`pkg/cache` uses [jetcache-go v1.2.6](https://github.com/mgtv-tech/jetcache-go/tree/v1.2.6)
for local/remote composition and read-through singleflight. It owns Redis cache
adapters, process connection pools and the cache key registry. It does not
implement another cache engine or eviction algorithm.

## Configuration

```go
// One instance per service/cache policy; reuse it to share in-flight loads.
c, err := cache.New(cache.Config{
    Name: "public-summary",
    Redis: cache.SharedConnections.Client(addr, password),
    Local: cache.NewLRU(4096, time.Second),
    TTL: 30 * time.Second,
})
// Handle err, then close c when its owner stops.
```

- Omit `Local` for Redis-only storage; omit `Redis` for local-only storage.
- `NewLRU` adapts the existing HashiCorp LRU dependency to Jetcache's `local.Local`.
- `NewTinyLFU` uses Jetcache's built-in Ristretto adapter. Its capacity counts
  entries, and admission may reject new values. TTL jitter is disabled. The
  upstream adapter has no close method; construct it once for the process lifetime.
- Supply another `local.Local` implementation for other local policies.
- Jetcache composes one local tier and one remote tier. Existing auth,
  relationship, discovery and private-search caches remain Redis-only.
- Local expiry is a configured staleness bound. Reading a remote value into L1
  starts the local TTL; it does not inherit Redis's remaining TTL. External writes
  do not synchronously invalidate another process's local tier. Enable a local
  tier only when that bound is acceptable. Generation-based discovery invalidation
  and fresh private-search visibility checks remain domain responsibilities.

`Get`/`Set` use JSON, including JSON strings. `GetBytes`/`SetBytes` preserve raw
bytes, except Jetcache's reserved raw `*` placeholder, which is rejected explicitly.
The raw compatibility adapter retains unrestricted byte values. `Redis(client).Read/Write/Remove` is the compatibility adapter for existing
raw Redis formats and error policies. `RedisCache` retains the existing JSON
interface. TTL zero on explicit Redis writes means no expiry; negative TTLs are
rejected. A cache with a local tier uses its configured fixed TTL on writes.

`Load` delegates coalescing to Jetcache `Once`. It runs shared work with a bounded
independent context that retains caller metadata, lets canceled waiters leave,
and decodes an independent result for each waiter. Loader errors are not cached.
It preserves two contracts beyond upstream defaults:

1. A Redis read error does not trigger a loader. Jetcache normally falls through
   to its loader on remote errors; the adapter preserves the error for `Do`.
2. A write error reaches every waiter. Jetcache 1.2.6 can discard final remote
   write errors in `Once`; the wrapper writes inside `Do` and disables the
   duplicate remote write with `TTL(-1)`.

The Redis adapter also preserves subsecond TTLs instead of allowing Jetcache's
item-TTL defaulting to replace them. The cache package returns errors; callers
retain their explicit optional-cache or fail-closed policies.

## Connections and keys

`pkg/db.InitRedis` and `pkg/mq.Init` borrow the same process pool for the same
endpoint and credentials from `cache.SharedConnections`. Constructors accepting
clients borrow them and never close them. A pool owner calls `Connections.Close`
at shutdown; `Store.Close` closes only its Jetcache instance. Tests may inject an
isolated client without registering it globally.

All cache key templates live in `pkg/cache/keys/keys.go`, grouped by owning
subsystem. Readers, writers, invalidators and account cleanup reference the same
constants. Keep existing wire names during rolling upgrades. Private search keys
include an owner, named scope and hash of every query/filter/pagination input;
raw query text never appears in a Redis key.

## Migration inventory

| Existing cache | Shared implementation | Preserved policy |
| --- | --- | --- |
| Legacy search, profile, item stats, influence | Jetcache JSON adapter | Existing Redis keys and configured TTLs |
| Profile embedding | Raw adapter | Raw bytes, 24-hour TTL |
| Discovery context/compiled inputs | `cache.DiscoveryCache`, raw adapter | Generation fencing, absolute deadlines, optional Redis, 30/5-second TTLs |
| PM item owner/response/conversation/map and inbox | Raw adapter | Existing null markers, TTLs and write invalidation |
| PM friend/block sets | Redis set adapter | Redis-only membership, DB fallback, 24-hour TTL, atomic replacement |
| Feed queue | `cache.FeedCache`, `pkg/feedcache` API aliases | Atomic list pop, entry order, legacy payloads, 30-minute TTL |
| Auth verify result/session | Raw adapter | Redis-only, existing logout/reset invalidation and 2/10-minute TTLs |
| Email lookup, blacklist, beat signals | Raw adapter | Existing raw/JSON formats, 24-hour/60-second/5-minute TTLs |
| Console home discovery/activity/worth-watching | Raw adapter | Domain deadlines, corruption handling, existing TTLs and coalescing |
| Query embeddings and frozen delivery pages/responses/sessions | Raw adapter | Versioned keys, distributed leases and idempotency checks |
| Recall lists and Swing neighbors | Shared HashiCorp typed LRU adapter | 30-second TTL; bounded to 10,000 entries per cache; copied slices |
| Author content class | Shared typed LRU adapter | 100,000 entries, 12-hour TTL |
| Milestone rules | Shared typed LRU adapter | Configured TTL, clock injection, explicit invalidation; 256 entries |
| Embedding readiness | Shared typed LRU adapter | One entry, separate success/failure TTLs and clock injection |
| Private search | Jetcache `Load` through `searchguard` | Five-second pages, rate/concurrency limits, authoritative visibility |

Redis Streams, locks, counters, impression sets, feature materializations and
last-active write buffers are business state with atomic domain operations, not
interchangeable key/value caches. They keep their owners and named key contracts.
Feature hydration's bounded request memo and immutable compiled YAML plans are
execution/configuration state and remain scoped to their request/configuration.
The CLI's filesystem cache is durable Agent state and remains in its independent
module. The independent admin Console only invalidates the blacklist cache; its
named key constant retains the same wire contract (covered by a registry test).

Commission is a separate repository/module and cannot import the root module.
Its `pkg/cache` contains the same small Jetcache/Redis adapter and search key
contracts; only private service/order search is migrated there. General caches
and local backends in Commission are outside this change.

## Library choices and validation

[Jetcache](https://github.com/mgtv-tech/jetcache-go) supplies composition and
singleflight; its [TinyLFU adapter](https://github.com/mgtv-tech/jetcache-go/blob/v1.2.6/local/tinylfu.go)
uses Ristretto. [go-cache](https://github.com/patrickmn/go-cache) and
[BigCache](https://github.com/allegro/bigcache) were considered as local-store
references; no additional dependency on either is needed for this integration.

Tests cover wire format/TTL compatibility, tier promotion, failed-write local
cleanup, LRU eviction, TinyLFU access, independent decoded values, canceled
leaders, Redis failures, error propagation, connection ownership and legacy
business-cache tests. Private-search tests also cover owner/query isolation,
shared budgets, bounded concurrency and revocation after warming a cache.
