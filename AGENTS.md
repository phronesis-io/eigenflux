# CLAUDE.md - EigenFlux Server Development Guidelines

## Project Overview

Agent-oriented information distribution platform, built with Go and CloudWeGo microservices architecture. Please read `docs/architecture_overview.md` before modifying code.

## Development Environment

- Go 1.25+
- Infrastructure: `docker compose up -d` (PostgreSQL, Redis, etcd, Elasticsearch, Kibana)
- Monitoring (optional): `docker compose -f docker-compose.monitor.yml up -d` (Jaeger, Loki, Grafana). Then set `MONITOR_ENABLED=true` in `.env`
- Default connection config in `pkg/config/config.go`, override via environment variables
- Build: `bash scripts/common/build.sh` (core), `./console/console_api/scripts/build.sh` (console)
- Start: `./scripts/local/start_local.sh` (core), `./console/console_api/scripts/start.sh` (console)
- All tests: `go test -v ./tests/...` (requires all services running)

## Directory Responsibilities

| Directory | Responsibility | Notes |
|-----------|---------------|-------|
| `api/` | HTTP Gateway | Hertz-based API gateway (port 8080). hz-generated code in `handler_gen/`, `router_gen/`, `model/`. RPC clients in `clients/`. Swagger docs in `docs/` |
| `console/` | Console subsystem | Independent Go module (`console.eigenflux.ai`). Own IDL, codegen, DAL, and build workflow. API (port 8090) and Web UI (Vite + Refine + Ant Design). Must not import root module packages |
| `rpc/*/` | RPC services | Kitex-based microservices (auth, profile, item, sort, feed, pm, notification). Business logic in `handler.go`, data access in `dal/` |
| `pipeline/` | Async processing | LLM consumers (`consumer/`), embedding client (`embedding/`), scheduled tasks (`cron/`) |
| `pkg/` | Shared libraries | Common utilities: cache, impr, idgen, es, mq, email, logger, validator, stats, milestone, reqinfo, rpcx, audience, dedup, telemetry |
| `idl/` | Thrift IDL | RPC contracts and public API definitions only. Console IDL lives under `console/console_api/idl/`. Regenerate code after changes: `kitex` for RPC, `hz update` for HTTP |
| `kitex_gen/` | Auto-generated code | **DO NOT manually modify**. Regenerate after IDL changes |

All project documentation must be written in English.

## Code Conventions (Universal Rules)

- Database time fields: `int64` Unix millisecond timestamp (`time.Now().UnixMilli()`)
- Keywords/domains: comma-separated strings, convert via `strings.Split/Join`
- Processing status codes: `0=pending, 1=processing, 2=failed, 3=completed, 4=discarded`
- API responses: must include `code` (0=success) and `msg`; data in `data` field as object type
- IDs: `BIGINT/i64` internally, string in HTTP JSON externally
- ID generation: snowflake via etcd-leased `worker_id`
- String length: multi-language weighted (ASCII=1, CJK=2), see `pkg/validator/string_length.go`
- Feed pagination: cursor-based via `last_updated_at`, sorted by `updated_at DESC`

For data models and detailed conventions, see [docs/dev/conventions.md](docs/dev/conventions.md).

## Module References

Detailed documentation is organized by module. **Before modifying code in any area, read the corresponding module doc first.** Key triggers:
- Changing IDL or database schema → read `idl_and_db.md`
- Adding/modifying API endpoints → read `api_endpoints.md` + `conventions.md`
- Working on pipeline or embedding → read `pipeline.md`
- Touching notification, PM, feed, cache, console, auth → read the matching doc
- Adding a new service or port → read `configuration.md`

| Module | File | Covers |
|--------|------|--------|
| Code Conventions | [docs/dev/conventions.md](docs/dev/conventions.md) | Data models, ID conventions, API response format, coding standards |
| IDL & Database | [docs/dev/idl_and_db.md](docs/dev/idl_and_db.md) | IDL modification workflow (kitex/hz/console), hz constraints, database migrations |
| Authentication | [docs/dev/auth.md](docs/dev/auth.md) | Login flow, OTP, security mechanisms, mock OTP whitelist |
| API Endpoints | [docs/dev/api_endpoints.md](docs/dev/api_endpoints.md) | All HTTP API endpoints, skill document structure, Swagger |
| Pipeline | [docs/dev/pipeline.md](docs/dev/pipeline.md) | Item processing flow, async messaging, embedding config, LLM |
| Notification | [docs/dev/notification.md](docs/dev/notification.md) | Notification service DAL, Redis keys, delivery dedup, audience expressions, reqinfo |
| PM Service | [docs/dev/pm.md](docs/dev/pm.md) | Private messaging, friend/block relations, icebreaker, conversation types |
| Feed & Cache | [docs/dev/feed_and_cache.md](docs/dev/feed_and_cache.md) | Feed flow, impression recording, multi-level cache (L1-L5), cache config |
| Console | [docs/dev/console.md](docs/dev/console.md) | Console service, build/start, API endpoints, frontend development |
| Infrastructure | [docs/dev/infra.md](docs/dev/infra.md) | Distributed tracing, logging conventions, RPC bootstrap (pkg/rpcx) |
| Configuration | [docs/dev/configuration.md](docs/dev/configuration.md) | All environment variables, service ports, startup constraints |
| Testing | [docs/dev/testing.md](docs/dev/testing.md) | Test directories, run commands, manual integration scripts |

# IMPORTANT — Rules That Apply to Every Task

## Build and Testing
After each code change, remember to add or modify test cases. Run build and e2e tests to ensure functionality works!
- Test case code goes in `tests/`
- Don't add degradation logic just to make tests pass, otherwise testing is meaningless. Let humans handle errors that can't be handled.
- Build and tool scripts go in `scripts`
- Build artifacts must go in `build/` directory, never in source directories. Always use `-o build/<name>` when running `go build` manually (e.g. `go build -o build/auth ./rpc/auth/`). Running bare `go build .` will dump a binary named after the module into the current directory — do not do this. Use `bash scripts/common/build.sh` for core services and `./console/console_api/scripts/build.sh` for console

## Documentation Updates
After each code change, remember to check if documentation needs updating, especially README.md and CLAUDE.md (including module docs under `docs/dev/`). These documents are important and must be updated promptly.
When updating documentation, use clear and explicit language to describe the current latest state. No need to generate process description documents, git history can be queried.

## Code Cleanup
- Never comment out old code. If code needs to be replaced or removed, delete it completely.
- Never leave comments explaining what old code used to be (e.g., "previously was X, now changed to Y").
- Rely on version control (like Git) to trace history. Your task is to provide the absolute latest, cleanest, runnable code version.
- Don't leave dead code (unused code), deprecated markers, or unused imports.
