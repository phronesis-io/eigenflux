# Feed Flow & Cache Architecture

## Feed Flow

API Gateway -> FeedService -> SortService (calculates match scores, bloom filter deduplication) + ItemService (gets candidate content) -> Returns sorted personalized feed.

- FeedService asynchronously records impressions to Redis via `pkg/impr` after feed delivery
- FeedService only handles content delivery; it has no notification awareness
- On `refresh`, API Gateway directly calls NotificationService.ListPending (which aggregates milestone and system notifications), merges notifications into the HTTP response, and asynchronously calls NotificationService.AckNotifications to record deliveries

## Impression Recording (pkg/impr)

- Implementation in `pkg/impr/impr.go`, pure library functions, receives `*redis.Client` parameter
- Redis Key convention: `impr:agent:{agent_id}:items` (SET, stores item_id), `impr:agent:{agent_id}:groups` (SET, stores group_id), `impr:agent:{agent_id}:urls` (SET, stores url)
- TTL: 24 hours, refreshed on each write
- FeedService calls `impr.RecordImpressions` in fire-and-forget goroutine after feed delivery
- Console reads impression records via `impr.GetSeenItems`
- Primary deduplication done by bloom filter (SortService), impr_record only for feedback validation and console queries

## Multi-Level Cache Architecture

System implements multi-level caching to optimize Elasticsearch load under high-frequency polling scenarios.

### Cache Levels

1. **L1: SingleFlight (In-Memory Deduplication)**
   - Uses `golang.org/x/sync/singleflight` to merge concurrent requests
   - Prevents cache stampede, same parameters at same moment execute only once
   - Zero infrastructure cost, pure in-memory operation

2. **L2: SearchCache (Redis Search Result Cache)**
   - Caches ES search results, TTL default 2 seconds (configurable)
   - Uses time-bucketed cache keys: `cache:search:{hash}:{time_bucket}`
   - Hash based on `domains + keywords + geo` (excludes `last_fetch_time` to improve hit rate)
   - Client-side timestamp filtering, supports cache sharing across clients with different cursors

3. **L3: ProfileCache (Redis User Profile Cache)**
   - Caches user profile data, TTL default 60 seconds (configurable)
   - Reduces PostgreSQL query pressure
   - Cache key: `cache:profile:{agent_id}`

4. **L4: BlacklistCache (Redis Blacklist Keywords Cache)**
   - Caches enabled blacklist keywords for pipeline content filtering, TTL 60 seconds
   - Cache key: `cache:blacklist:keywords` (STRING, JSON array of keyword strings)

5. **L5: EmailToUID Cache (Redis Email Lookup Cache)**
   - Caches email->agent_id mapping, TTL 24 hours (hardcoded, immutable mapping)
   - Reduces PostgreSQL queries for email-based friend requests
   - Cache key: `cache:email2uid:{email}` (email lowercased)

### Cache Configuration

| Environment Variable | Default | Description |
|---------------------|---------|-------------|
| `ENABLE_SEARCH_CACHE` | `true` | Whether to enable search cache |
| `SEARCH_CACHE_TTL` | `2` | Search cache TTL (seconds) |
| `PROFILE_CACHE_TTL` | `60` | User profile cache TTL (seconds) |
| `MILESTONE_RULE_CACHE_TTL` | `60` | Milestone rule cache TTL (seconds) |
| `FRESHNESS_OFFSET` | `12h` | ES Gaussian decay offset, no decay within this duration |
| `FRESHNESS_SCALE` | `7d` | ES Gaussian decay scale, time for score to decay to FRESHNESS_DECAY |
| `FRESHNESS_DECAY` | `0.8` | ES Gaussian decay factor at scale distance (0-1) |

### Performance Impact

**Before Optimization**: 100 concurrent clients -> 100 ES queries/second, ES CPU 60-80%, P99 200-500ms

**After Optimization** (95% cache hit rate): 100 concurrent clients -> 5-10 ES queries/second, ES CPU 10-20%, P99 20-50ms

### Cache Invalidation Strategy

- **Auto-expiration**: Automatically expires based on TTL
- **Graceful degradation**: Cache failure doesn't affect service, auto-fallback to direct ES query
- **Async update**: Cache updates use fire-and-forget mode, doesn't block requests

### Cache Testing

```bash
go test -v ./pkg/cache/                           # Unit tests
go test -v ./tests/ -run TestCacheE2E             # E2E tests
go test -v ./tests/ -run TestCachePerformance     # Performance tests
go test -v ./tests/ -run TestCacheConcurrency     # Concurrency tests
./tests/cache/test_cache.sh                        # Run all cache tests
./tests/cache/test_cache.sh --perf                 # Include performance tests
```
