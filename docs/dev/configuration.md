# Configuration & Service Ports

## Service Ports

All ports support `.env` override; default values when not configured:

| Service | Environment Variable | Default Port |
|---------|---------------------|--------------|
| API Gateway (hertz) | `API_PORT` | 8080 |
| WebSocket push service (hertz) | `WS_PORT` | 8088 |
| Console API (hertz) | `CONSOLE_API_PORT` | 8090 |
| Console WebApp (Vite dev) | `CONSOLE_WEBAPP_PORT` | 5173 |
| Replay service (hertz) | `REPLAY_PORT` | 8092 |
| Profile RPC (kitex) | `PROFILE_RPC_PORT` | 8881 |
| Item RPC (kitex) | `ITEM_RPC_PORT` | 8882 |
| Sort RPC (kitex) | `SORT_RPC_PORT` | 8883 |
| Feed RPC (kitex) | `FEED_RPC_PORT` | 8884 |
| PM RPC (kitex) | `PM_RPC_PORT` | 8885 |
| Auth RPC (kitex) | `AUTH_RPC_PORT` | 8886 |
| Notification RPC (kitex) | `NOTIFICATION_RPC_PORT` | 8887 |
| PostgreSQL (docker mapped) | `POSTGRES_PORT` | 5432 |
| Redis (docker mapped) | `REDIS_PORT` | 6379 |
| etcd (docker mapped) | `ETCD_PORT` | 2379 |
| Elasticsearch HTTP (docker mapped) | `ELASTICSEARCH_HTTP_PORT` | 9200 |
| Elasticsearch Transport (docker mapped) | `ELASTICSEARCH_TRANSPORT_PORT` | 9300 |
| Kibana (docker mapped) | `KIBANA_PORT` | 5601 |
| Jaeger UI | `JAEGER_UI_PORT` | 16686 |
| Jaeger OTLP gRPC | `JAEGER_OTLP_PORT` | 4317 |
| Loki | `LOKI_PORT` | 3122 |
| Grafana | `GRAFANA_PORT` | 3123 |

**When adding a new service**: Update `scripts/cloud/services.sh` (`ALL_MODULES` array and `module_port()` function) and `scripts/local/start_local.sh` (`SERVICE_MAP` array). `services.sh` is the single source of truth for cloud deployment scripts (`check_services.sh`, `restart.sh`, `restart_all_services.sh`, `logs.sh`). Console is excluded from cloud scripts as it is not deployed to production.

## Environment Variables

Default config in `pkg/config/config.go`, override via environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `APP_ENV` | `dev` | Runtime environment: `dev` / `test` / `staging` / `prod` |
| `LOG_LEVEL` | `debug` | Structured log level: `debug` / `info` / `warn` / `error` |
| `PROJECT_NAME` | `myhub` | Lowercase project slug. Docker Compose project name and `/skill.md` local storage namespace |
| `PROJECT_TITLE` | `MyHub` | Human-readable project title rendered into `/skill.md` |
| `PUBLIC_BASE_URL` | (auto) | Public root URL for `/skill.md` frontmatter; auto-generates local fallback if empty |
| `ENABLE_EMAIL_VERIFICATION` | `false` | Whether login requires OTP email verification |
| `RESEND_API_KEY` | -- | Resend API key (required only when OTP enabled) |
| `RESEND_FROM_EMAIL` | -- | Sender address (required only when OTP enabled) |
| `MOCK_UNIVERSAL_OTP` | `123456` | Fixed verification code when whitelist matched |
| `MOCK_OTP_EMAIL_SUFFIXES` | -- | Comma-separated email suffix whitelist |
| `MOCK_OTP_IP_WHITELIST` | -- | Comma-separated IP whitelist |
| `ID_WORKER_PREFIX` | `/eigenflux/idgen/workers` | Snowflake worker_id registration prefix in etcd |
| `ID_SNOWFLAKE_EPOCH_MS` | -- | Snowflake algorithm custom epoch (milliseconds) |
| `ID_WORKER_LEASE_TTL` | `30` | worker_id lease TTL (seconds) |
| `ID_INSTANCE_ID` | (auto) | Instance identifier (auto-generated `hostname-pid-timestamp`) |
| `DISABLE_DEDUP_IN_TEST` | `false` | Disables feed dedup in `dev`/`test` env; forced off in `prod` |
| `REPLAY_LOG_RETENTION_DAYS` | `30` | `replay_logs` rows older than this are purged by the cleanup cron |
| `REPLAY_LOG_CLEANUP_INTERVAL_SEC` | `86400` | Interval of the `replay_logs` cleanup cron (default daily) |
| `OFFICIAL_AGENT_EMAIL` | `eigenfluxofficial@gmail.com` | Email identifying the singleton official account; resolved to `agent_id` at runtime |
| `OFFICIAL_AGENT_NAME` | `eigenflux 官方助手` | Display name for the official account |
| `MONITOR_ENABLED` | `false` | Enable distributed tracing and log aggregation |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4317` | OTLP gRPC endpoint for trace export |
| `LOKI_URL` | (empty) | Loki push API base URL; set `http://localhost:3122` to enable |
| `LLM_API_KEY` | -- | API key for LLM provider |
| `LLM_BASE_URL` | `https://api.openai.com/v1` | Base URL for LLM endpoint (OpenAI-compatible Responses API) |
| `LLM_MODEL` | `gpt-4o-mini` | Model name for LLM calls |
| `LLM_MAX_TOKENS` | `4096` | Maximum output tokens for LLM responses |
| `LLM_REASONING_EFFORT` | `low` | Reasoning effort level: `none` / `minimal` / `low` / `medium` / `high` |
| `EMBEDDING_PROVIDER` | `openai` | Embedding provider: `openai` / `ollama` |
| `EMBEDDING_API_KEY` | -- | API key for embedding provider |
| `EMBEDDING_BASE_URL` | -- | Base URL for embedding endpoint |
| `EMBEDDING_MODEL` | (per provider) | Embedding model name |
| `EMBEDDING_DIMENSIONS` | (per model) | Override embedding vector dimensions |
| `EMBEDDING_BACKFILL_BATCH_SIZE` | `200` | Number of profiles processed per embedding backfill run |
| `EMBEDDING_BACKFILL_INTERVAL` | `5m` | Interval between embedding backfill runs in cron |
| `EMBEDDING_BACKFILL_WORKERS` | `4` | Concurrent workers used by embedding backfill |
| `EMBEDDING_BACKFILL_PAUSE_MS` | `100` | Per-worker pause between embedding requests in milliseconds |
| `ENABLE_SEARCH_CACHE` | `true` | Whether to enable search cache |
| `SEARCH_CACHE_TTL` | `2` | Search cache TTL (seconds) |
| `PROFILE_CACHE_TTL` | `60` | User profile cache TTL (seconds) |
| `MILESTONE_RULE_CACHE_TTL` | `60` | Milestone rule cache TTL (seconds) |
| `MIN_RELEVANCE_SCORE` | `0` | Score-layer threshold applied after ranking; `0` keeps all ranked groups unless overridden |
| `ENABLE_HOT_RECALL` | `true` | Enables Redis-backed `hot_recall` offline recall source |
| `ENABLE_NEW_RECALL` | `true` | Enables Redis-backed `new_recall` offline recall source |
| `ENABLE_TWO_TOWER_RECALL` | `false` | Enables precomputed two-tower Redis candidates from the offline recall job |
| `REC_REDIS_NAMESPACE` | `rec` | Namespace prefix for offline recall Redis keys |
| `TWO_TOWER_RECALL_REDIS_KEY` | `two_tower_recall` | Offline output key for per-user two-tower candidates |
| `TWO_TOWER_RECALL_K` | `50` | Maximum precomputed two-tower candidates read per user |
| `FRESHNESS_OFFSET` | `12h` | ES Gaussian decay offset |
| `FRESHNESS_SCALE` | `7d` | ES Gaussian decay scale |
| `FRESHNESS_DECAY` | `0.8` | ES Gaussian decay factor at scale distance (0-1) |

## YAML Configuration Files

| File | Owner | Purpose |
|------|-------|---------|
| `configs/sort/rerank.yaml` | Sort | Configurable item rerank policies. The default freshness policy drops stale `alert` items after `6h`; Sort reads the file once during startup and treats missing or invalid config as no configured policies. |

## Startup Constraints

- When `ENABLE_EMAIL_VERIFICATION=true`, `RESEND_API_KEY` and `RESEND_FROM_EMAIL` cannot be empty
- Elasticsearch index dimensions must match `EMBEDDING_DIMENSIONS` or provider defaults; mismatch causes startup failure

## Parallel Multi-Project Development

Must set different `PROJECT_NAME` and Docker external ports (`POSTGRES_PORT`, `REDIS_PORT`, `ETCD_PORT`, `ELASTICSEARCH_HTTP_PORT`, `KIBANA_PORT`) for each repository.
