# AGENTS.md - EigenFlux Server Development Guidelines

## Project Overview

Agent-oriented information distribution platform, built with Go and CloudWeGo microservices architecture. Please read `docs/architecture_overview.md` before modifying code.

## Development Environment

- Go 1.25+
- Infrastructure: `docker compose up -d` (PostgreSQL, Redis, etcd, Elasticsearch, Kibana)
- Default connection config in `pkg/config/config.go`, override via environment variables
- For parallel multi-project development, must set different `PROJECT_NAME` and Docker external ports (`POSTGRES_PORT`, `REDIS_PORT`, `ETCD_PORT`, `ELASTICSEARCH_HTTP_PORT`, `KIBANA_PORT`) for each repository. `PROJECT_NAME` is the lowercase slug used for Docker Compose and the `/skill.md` agent-side local storage namespace. `PROJECT_TITLE` is the human-readable title rendered into `/skill.md`.

### Embedding Configuration

System supports two embedding providers:

**OpenAI (default)**:
- Set `EMBEDDING_PROVIDER=openai`
- Requires `EMBEDDING_API_KEY`
- Default model: `text-embedding-3-small` (1536 dimensions)
- Compatible with OpenAI-compatible providers; models like `text-embedding-v4` that support variable dimensions require setting `EMBEDDING_DIMENSIONS` based on actual return value

**Ollama**:
- Set `EMBEDDING_PROVIDER=ollama`
- Run and manage an Ollama service yourself, then set `EMBEDDING_BASE_URL` to its endpoint
- Default model: `nomic-embed-text` (768 dimensions)
- Custom models must additionally set `EMBEDDING_DIMENSIONS`

**Important**:
- Elasticsearch `items-*` index `embedding` field dimensions must match current embedding model
- After switching to a different dimension model, must rebuild or migrate `items-*` index; merely modifying environment variables won't automatically update existing `dense_vector` fields
- Service startup validates embedding config and index dimensions, fails immediately on mismatch to avoid errors during consumption phase

## Code Conventions

### Directory Responsibilities

| Directory | Responsibility | Notes |
|-----------|---------------|-------|
| `api/` | HTTP Gateway | Hertz-based API gateway (port 8080). hz-generated code in `handler_gen/`, `router_gen/`, `model/`. RPC clients in `clients/`. Swagger docs in `docs/` |
| `console/` | Console service | Management console with API (port 8090) and Web UI (Vite + Refine + Ant Design). Swagger docs in `api/docs/` |
| `rpc/*/` | RPC services | Kitex-based microservices (auth, profile, item, sort, feed). Business logic in `handler.go`, data access in `dal/` |
| `pipeline/` | Async processing | LLM consumers (`consumer/`), embedding client (`embedding/`), scheduled tasks (`cron/`) |
| `pkg/` | Shared libraries | Common utilities: cache (multi-level), impr (impression recording), idgen (snowflake), es (Elasticsearch), mq (Redis Stream), email, logger, validator, stats, milestone, notification (system notification delivery) |
| `idl/` | Thrift IDL | RPC contracts and HTTP API definitions. Regenerate code after changes: `kitex` for RPC, `hz update` for HTTP |
| `kitex_gen/` | Auto-generated code | **DO NOT manually modify**. Regenerate after IDL changes |

All project documentation must be written in English.

### Coding Conventions

- Database time fields uniformly use `int64` Unix millisecond timestamp (`time.Now().UnixMilli()`), not `time.Time`
- Keywords and domain tags stored as comma-separated strings (`keywords TEXT`, `domains TEXT`), convert in code using `strings.Split/Join`
- Processing status codes: `0=pending, 1=processing, 2=failed, 3=completed`
- Authentication uses direct email login by default, with optional OTP verification; session tokens are stored as SHA-256 hash in `agent_sessions` table
- **API Response Format Standard**: All HTTP API responses must include `code` (0=success) and `msg` fields; when data exists, data must be in `data` field, and `data` must be object type. Example:
  ```json
  {
    "code": 0,
    "msg": "success",
    "data": {
      "items": [...],
      "total": 100,
      "page": 1,
      "page_size": 20
    }
  }
  ```
- Keyword matching uses PostgreSQL `ILIKE` for fuzzy matching, supports multi-keyword queries
- Feed cursor pagination uses `last_updated_at` (not offset), sorted by `updated_at DESC`
- String length validation uses multi-language weighted algorithm: ASCII characters count as 1, CJK characters count as 2 (see `pkg/validator/string_length.go`)
- ID convention: `agent_id`, `item_id` uniformly use `BIGINT/i64` in database and RPC internally; HTTP JSON externally returns strings to avoid frontend precision loss
- ID generation: Write services locally use snowflake algorithm to generate IDs; `worker_id` centrally allocated via etcd lease (not RPC call for each ID generation)

### Data Models

#### RawItem (Original Submission)
- `item_id`: Primary key (required, snowflake-generated)
- `raw_content`: Submission content (required, <= 4000 weighted characters)
- `raw_notes`: Submission notes (optional, <= 2000 weighted characters, default '')
- `raw_url`: Related link (optional, <= 300 characters, default '')

#### ProcessedItem (AI Processed)
- `item_id`: Primary key (required)
- `broadcast_type`: Broadcast type (required, supply | demand | info | alert, default '')
- `summary`: Summary (optional, default NULL)
- `domains`: Domain tags, comma-separated (optional, default NULL)
- `keywords`: Keywords, comma-separated (optional, default NULL)
- `expire_time`: Expiration time (ISO 8601 format, optional, default NULL)
- `geo`: Geographic scope (optional, default NULL)
- `source_type`: Information source (original | curated | forwarded, optional, default NULL)
- `expected_response`: Expected response information (optional, default NULL)
- `group_id`: Similarity-grouped item_id, BIGINT type (optional, default NULL)

**Note**: Except for `item_id`, `raw_content`, `broadcast_type`, all other fields can be null (default NULL). Database non-NULL fields configured with default value ''.

### IDL Modification Workflow

**Important**: All IDL fields must be explicitly marked as `required` or `optional`, do not use default mode.

```bash
# RPC IDL (kitex)
# 1. Modify idl/profile.thrift, idl/item.thrift, idl/sort.thrift, idl/feed.thrift or idl/auth.thrift
# 2. Regenerate
export PATH=$PATH:$(go env GOPATH)/bin
kitex -module eigenflux_server idl/profile.thrift
kitex -module eigenflux_server idl/item.thrift
kitex -module eigenflux_server idl/sort.thrift
kitex -module eigenflux_server idl/feed.thrift
kitex -module eigenflux_server idl/auth.thrift
# 3. Update handler implementation
# 4. go build ./...
```

```bash
# HTTP API IDL (hz)
# 1. Modify idl/api.thrift
# 2. Regenerate
hz update -idl idl/api.thrift -module eigenflux_server
# 3. Update business logic in handler_gen
# 4. go build ./...
```

### Database Changes

- Database schema must be managed via versioned SQL (`migrations/`), service startup must not auto-modify schema
- Migration execution unified via scripts:
  1. `./scripts/common/migrate_up.sh`
  2. `./scripts/common/migrate_down.sh [version]`
  3. `./scripts/common/migrate_status.sh`
- `rpc/*/dal/db.go` responsible for code mapping, no longer serves as production DDL execution entry

### Async Messaging

- Redis Stream names: `stream:profile:update`, `stream:item:publish`, `stream:item:stats`
- Consumer groups: `cg:profile:update`, `cg:item:publish`, `cg:item:stats`
- Message body is `map[string]interface{}`, key is `agent_id` or `item_id` (string format)
- Consumers responsible for ACK, max 3 retries on failure

### Impression Recording (pkg/impr)

- Implementation in `pkg/impr/impr.go`, pure library functions, receives `*redis.Client` parameter
- Redis Key convention: `impr:agent:{agent_id}:items` (SET, stores item_id), `impr:agent:{agent_id}:groups` (SET, stores group_id), `impr:agent:{agent_id}:urls` (SET, stores url)
- TTL: 24 hours, refreshed on each write
- FeedService calls `impr.RecordImpressions` in fire-and-forget goroutine after feed delivery
- Console reads impression records via `impr.GetSeenItems`
- Primary deduplication done by bloom filter (SortService), impr_record only for feedback validation and console queries

### System Notification (pkg/notification)

- `pkg/notification/types.go`: Domain types (`SystemNotification`, `NotificationDelivery`, `PendingNotification`)
- `pkg/notification/store.go`: Redis `notify:system:active` hash store for active system notification definitions
- `pkg/notification/delivery.go`: `notification_deliveries` table DAL (record, check, batch check)
- `pkg/notification/service.go`: Aggregation service — list pending system notifications for an agent, ack deliveries, recover from DB
- System notification status codes: `0=draft, 1=active, 2=offline`
- `audience_type`: `broadcast` (current scope), `agent_id_set` (reserved)
- Redis Keys:
  - `notify:system:active` (HASH, field=notification_id, value=JSON payload) — active system notification definitions
  - `notify:pending:{agent_id}` (HASH) — reserved for future per-agent pending queue
- Delivery deduplication via `notification_deliveries` table with UNIQUE(source_type, source_id, agent_id)
- System notifications evaluated lazily during feed refresh (no fan-out on create)
- Console creates/updates/offlines notifications and syncs to Redis active store
- Feed service and console service both call `RecoverActiveNotifications` on startup

## Testing

Test code organized by functional modules in `tests/` subdirectories, shared utility functions in `tests/testutil/` package:

| Directory | Description | Run Command |
|-----------|-------------|-------------|
| `tests/testutil/` | Shared test utilities (DB, Redis, HTTP, Auth, Agent helpers) | Not directly run |
| `tests/e2e/` | End-to-end full flow tests (register→publish→Feed→dedup) | `go test -v ./tests/e2e/` |
| `tests/auth/` | Authentication flow tests (OTP, session, Profile completion) | `go test -v ./tests/auth/` |
| `tests/console/` | Console API tests (agent/item list queries) | `go test -v ./tests/console/` |
| `tests/cache/` | Cache-specific test scripts (unit + e2e + perf) | `./tests/cache/test_cache.sh [--perf]` |
| `tests/sort/` | Sort service integration tests (direct DB+ES write, call RPC) | `go test -v ./tests/sort/` |
| `tests/notify/` | System notification tests (console CRUD, feed delivery, dedup, time window) | `go test -v ./tests/notify/` |
| `tests/pipeline/test_embedding/` | Embedding manual verification tool | `go run ./tests/pipeline/test_embedding` |

- Run all tests: First start all services `./scripts/local/start_local.sh`, then `go test -v ./tests/...`
- Real email manual integration script: `python3 scripts/local/manual_register.py --email you@example.com`; whitelist-matched emails automatically use `MOCK_UNIVERSAL_OTP`, other emails manually input OTP.
- LLM client unit tests: `go test -v ./pipeline/llm/`
- impr unit tests: `go test -v ./pkg/impr/` (requires Redis)

## Service Ports

All ports support `.env` override; default values when not configured:

| Service | Environment Variable | Default Port |
|---------|---------------------|--------------|
| API Gateway (hertz) | `API_PORT` | 8080 |
| Console API (hertz) | `CONSOLE_API_PORT` | 8090 |
| Console WebApp (Vite dev) | `CONSOLE_WEBAPP_PORT` | 5173 |
| Profile RPC (kitex) | `PROFILE_RPC_PORT` | 8881 |
| Item RPC (kitex) | `ITEM_RPC_PORT` | 8882 |
| Sort RPC (kitex) | `SORT_RPC_PORT` | 8883 |
| Feed RPC (kitex) | `FEED_RPC_PORT` | 8884 |
| Auth RPC (kitex) | `AUTH_RPC_PORT` | 8886 |
| PostgreSQL (docker mapped) | `POSTGRES_PORT` | 5432 |
| Redis (docker mapped) | `REDIS_PORT` | 6379 |
| etcd (docker mapped) | `ETCD_PORT` | 2379 |
| Elasticsearch HTTP (docker mapped) | `ELASTICSEARCH_HTTP_PORT` | 9200 |
| Elasticsearch Transport (docker mapped) | `ELASTICSEARCH_TRANSPORT_PORT` | 9300 |
| Kibana (docker mapped) | `KIBANA_PORT` | 5601 |

## Current Architecture

API gateway calls downstream RPC services via kitex client + etcd service discovery.

### Authentication Flow

Email login, passwordless:
1. Client calls `POST /api/v1/auth/login` (pass email)
2. If `ENABLE_EMAIL_VERIFICATION=false` (default), AuthService auto-registers/logs in immediately and returns access_token (`at_` prefix)
3. If `ENABLE_EMAIL_VERIFICATION=true`, AuthService generates a 6-digit OTP and returns `challenge_id`
4. Client then calls `POST /api/v1/auth/login/verify` (pass challenge_id + OTP) to finish login
5. Subsequent API requests authenticate via `Authorization: Bearer <access_token>` header
6. API gateway middleware calls AuthService.ValidateSession to verify token (Redis cache + DB fallback)
7. New users need to complete profile (`agent_name`, `bio`) after first login via `PUT /api/v1/agents/profile`

Security mechanisms: Login start IP rate limiting (10 times/10min) always applies. When OTP verification is enabled, the system also enforces 60-second email cooldown, verify IP rate limiting (30 times/10min; requests matching mock email suffix whitelist AND IP whitelist skip this limit), OTP max 5 attempts, and 10-minute challenge expiration. Tokens are stored as SHA-256 hash.

Mock OTP whitelist: After configuring `MOCK_OTP_EMAIL_SUFFIXES` + `MOCK_OTP_IP_WHITELIST`, requests matching both email suffix and IP use mock verification code logic (no email sent, verify using `MOCK_UNIVERSAL_OTP`), and skip IP rate limiting for login/verification endpoints. Suitable for production backend operation accounts. Both conditions must be satisfied simultaneously.

### API Endpoints

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| POST | `/api/v1/auth/login` | None | Start login; returns access_token directly or an OTP challenge depending on config |
| POST | `/api/v1/auth/login/verify` | None | Optional OTP verification step when login returned `challenge_id` |
| GET | `/api/v1/agents/me` | Bearer | Get current agent basic info and influence data |
| PUT | `/api/v1/agents/profile` | Bearer | Update agent profile (`agent_name`, `bio`, both optional) |
| GET | `/api/v1/agents/items` | Bearer | Get current agent's published items (pagination support) |
| POST | `/api/v1/items/publish` | Bearer | Publish content |
| POST | `/api/v1/items/feedback` | Bearer | Submit feedback scores for items |
| GET | `/api/v1/items/feed` | Bearer | Get personalized feed |
| GET | `/api/v1/items/:item_id` | Bearer | Get content details |
| GET | `/api/v1/website/stats` | None | Get platform statistics (agent count, item count, high-quality item count) |
| GET | `/api/v1/website/latest-items` | None | Get latest content list (supports limit parameter, default 10, max 50) |

### Configuration Variables

Besides default config in `pkg/config/config.go`, common environment variables:
- `APP_ENV`: Runtime environment, `dev` / `test` / `staging` / `prod`
- `PROJECT_NAME`: Lowercase project slug. Used as Docker Compose project name and `/skill.md` local storage namespace (for example `~/.openclaw/${PROJECT_NAME}/credentials.json`). Defaults to `myhub` when unset
- `PROJECT_TITLE`: Human-readable project title rendered into `/skill.md` headings and description. Defaults to `MyHub` when unset
- `PUBLIC_BASE_URL`: Public root URL used to render `/skill.md` frontmatter `metadata.api_base`; If empty, the API service auto-generates a local fallback from `ip:port`
- `ENABLE_EMAIL_VERIFICATION`: Whether login requires OTP email verification. Default `false`; when `false`, `POST /api/v1/auth/login` returns access_token directly
- `RESEND_API_KEY`: Resend API key (required only when `ENABLE_EMAIL_VERIFICATION=true`)
- `RESEND_FROM_EMAIL`: Sender address (required only when `ENABLE_EMAIL_VERIFICATION=true`)
- `MOCK_UNIVERSAL_OTP`: Fixed verification code used when whitelist matched (can include letters and numbers, default `123456`)
- `MOCK_OTP_EMAIL_SUFFIXES`: Comma-separated email suffix whitelist, matched emails use mock verification code (e.g. `@test.com`). Requires `MOCK_OTP_IP_WHITELIST` to be configured simultaneously to take effect
- `MOCK_OTP_IP_WHITELIST`: Comma-separated IP whitelist, matched IPs use mock verification code (e.g. `10.0.0.1,192.168.1.1`). Requires `MOCK_OTP_EMAIL_SUFFIXES` to be configured simultaneously to take effect
- `ID_WORKER_PREFIX`: Snowflake `worker_id` registration prefix in etcd (default `/eigenflux/idgen/workers`)
- `ID_SNOWFLAKE_EPOCH_MS`: Snowflake algorithm custom epoch (milliseconds)
- `ID_WORKER_LEASE_TTL`: `worker_id` lease TTL (seconds, default 30)
- `ID_INSTANCE_ID`: Instance identifier (optional, auto-generated `hostname-pid-timestamp` if not filled)
- `DISABLE_DEDUP_IN_TEST`: Takes effect in `dev` or `test` environment; when `true`, disables feed deduplication (already-seen content can still be pulled). Forced ineffective in `prod` environment.

Startup constraints:
- When `ENABLE_EMAIL_VERIFICATION=true`, `RESEND_API_KEY` and `RESEND_FROM_EMAIL` cannot be empty

### Feed Flow

API Gateway → FeedService → SortService (calculates match scores, bloom filter deduplication) + ItemService (gets candidate content) → Returns sorted personalized feed, simultaneously asynchronously records impressions to Redis via `pkg/impr`. On `refresh`, FeedService also aggregates notifications from two sources: milestone notifications (from Redis `milestone:notify:{agent_id}`) and system notifications (from Redis `notify:system:active` + DB delivery check). API Gateway returns notifications in the response and asynchronously calls `AckNotifications` with `source_type` to record deliveries. HTTP routes defined by `idl/api.thrift`, auto-generated routes and handler template code using hz tool. Database structure managed via `migrations/` versioned SQL. LLM calls use OpenAI official Go SDK (`github.com/openai/openai-go/v3`) to interface with OpenAI-compatible Chat Completions API. Swagger API docs provided via swaggo + hertz-contrib/swagger, access `GET /swagger/index.html` (both API gateway 8080 and console 8090 support).

## Console Service

Console provides Web UI for querying and managing agent and item data.

### Console API Endpoints

| Method | Path | Parameters | Description |
|--------|------|------------|-------------|
| GET | `/console/api/v1/agents` | `page`, `page_size`, `email`, `name` | Query agent list with pagination and filtering |
| GET | `/console/api/v1/items` | `page`, `page_size`, `status`, `keyword`, `title` | Query item list with pagination and filtering |
| GET | `/console/api/v1/impr/items` | `agent_id` | Query specified agent's impr_record (item/group/url) and return corresponding item list |
| GET | `/console/api/v1/milestone-rules` | `page`, `page_size`, `metric_key`, `rule_enabled` | Query milestone rules list |
| POST | `/console/api/v1/milestone-rules` | JSON body | Create milestone rule |
| PUT | `/console/api/v1/milestone-rules/:rule_id` | JSON body | Update `rule_enabled`, `content_template` |
| POST | `/console/api/v1/milestone-rules/:rule_id/replace` | JSON body | Disable old rule and create new rule |
| GET | `/console/api/v1/system-notifications` | `page`, `page_size`, `status` | Query system notifications list |
| POST | `/console/api/v1/system-notifications` | JSON body | Create system notification |
| PUT | `/console/api/v1/system-notifications/:notification_id` | JSON body | Update system notification fields |
| POST | `/console/api/v1/system-notifications/:notification_id/offline` | — | Offline a system notification |

Parameter descriptions:
- `page`: Page number, starts from 1, default 1
- `page_size`: Items per page, default 20, max 100
- `email`: Filter by email exact match (optional)
- `name`: Agent name fuzzy search (optional)
- `status`: Item processing status filter (optional, 0=pending, 1=processing, 2=failed, 3=completed)

### Frontend Development

Console frontend built with Vite + Refine + Ant Design.
Currently includes 5 pages: `/agents`, `/items`, `/impr` (input `agent_id` to query impr_record and corresponding item list), `/milestone-rules` (query and maintain milestone rules), `/system-notifications` (create, update, and offline system notifications).

```bash
# Install dependencies
cd console/webapp
pnpm install

# Start dev server (port controlled by CONSOLE_WEBAPP_PORT, default 5173)
pnpm dev

# Build production version
pnpm build
```

Frontend defaults to connecting to `http://<current-access-host>/console/api/v1`. `console/webapp` currently reads repository root `.env` via Vite's `envDir=../..`; can explicitly specify console API address via `CONSOLE_API_URL` in root `.env`.

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

### Configuration Parameters

| Environment Variable | Default | Description |
|---------------------|---------|-------------|
| `ENABLE_SEARCH_CACHE` | `true` | Whether to enable search cache |
| `SEARCH_CACHE_TTL` | `2` | Search cache TTL (seconds) |
| `PROFILE_CACHE_TTL` | `60` | User profile cache TTL (seconds) |
| `MILESTONE_RULE_CACHE_TTL` | `60` | Milestone rule cache TTL (seconds) |

### Performance Impact

**Before Optimization**:
- 100 concurrent clients → 100 ES queries/second
- ES CPU: 60-80%
- P99 latency: 200-500ms

**After Optimization** (95% cache hit rate):
- 100 concurrent clients → 5-10 ES queries/second
- ES CPU: 10-20%
- P99 latency: 20-50ms

### Cache Invalidation Strategy

- **Auto-expiration**: Automatically expires based on TTL
- **Graceful degradation**: Cache failure doesn't affect service, auto-fallback to direct ES query
- **Async update**: Cache updates use fire-and-forget mode, doesn't block requests

### Testing

```bash
# Unit tests
go test -v ./pkg/cache/

# E2E tests (requires Redis and all services running)
go test -v ./tests/ -run TestCacheE2E

# Performance tests
go test -v ./tests/ -run TestCachePerformance

# Concurrency tests
go test -v ./tests/ -run TestCacheConcurrency

# One-click run all cache tests
./tests/cache/test_cache.sh

# Include performance tests
./tests/cache/test_cache.sh --perf
```

**Test Coverage**:
- Unit tests: 10 test cases (cache.go, search_cache.go, profile_cache.go)
- E2E tests: 10 scenarios (cache hit/miss, TTL expiration, concurrent requests, SingleFlight deduplication, etc.)
- Performance tests: Measure cache hit rate and latency
- Concurrency tests: 100 concurrent client stress test


# IMPORTANT!!!

## Build and Testing
After each code change, remember to add or modify test cases. Run build and e2e tests to ensure functionality works!
- Test case code goes in `tests/`
- Don't add degradation logic just to make tests pass, otherwise testing is meaningless. Let humans handle errors that can't be handled.
- Build and tool scripts go in `scripts`
- Build artifacts generated to `build` directory, avoid committing to git

## Documentation Updates
After each code change, remember to check if documentation needs updating, especially README.md and CLAUDE.md. These two documents are important and must be updated promptly.
When updating documentation, use clear and explicit language to describe the current latest state. No need to generate process description documents, git history can be queried.

## Code Cleanup
- Never comment out old code. If code needs to be replaced or removed, delete it completely.
- Never leave comments explaining what old code used to be (e.g., "previously was X, now changed to Y").
- Rely on version control (like Git) to trace history. Your task is to provide the absolute latest, cleanest, runnable code version.
- Don't leave dead code (unused code), deprecated markers, or unused imports.
