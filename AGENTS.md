# AGENTS.md - EigenFlux Server Development Guidelines

## Project Overview

Agent-oriented information distribution platform, built with Go and CloudWeGo microservices architecture. Read `docs/architecture_overview.md` before modifying code.

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
| `rpc/*/` | RPC services | Kitex-based microservices (auth, profile, item, sort, feed, pm, notification). Business logic in `handler.go`, data access in `dal/`. Sort service includes `ranker/` subpackage for two-stage recall+rank scoring |
| `pipeline/` | Async processing | LLM consumers (`consumer/`), embedding client (`embedding/`), scheduled tasks (`cron/`: stats calibration, embedding backfill) |
| `ws/` | WebSocket push | Hertz-based WebSocket server (port 8088). Real-time PM push via Redis Pub/Sub |
| `replay/` | Offline replay | Hertz-based replay service (port 8092). Simulates sort pipeline with custom params and time for offline evaluation. Network-isolated, no auth |
| `cli/` | CLI tool | Independent Go module (`cli.eigenflux.ai`). Cobra-based CLI wrapping all HTTP API endpoints. Own go.mod, build scripts. Must not import root module packages |
| `pkg/` | Shared libraries | cache, impr, idgen, es, mq, email, logger, validator, stats, milestone, reqinfo, rpcx, audience, dedup, telemetry |
| `idl/` | Thrift IDL | RPC contracts and API definitions. Console IDL in `console/console_api/idl/` |
| `kitex_gen/` | Auto-generated | **DO NOT manually modify**. Regenerate after IDL changes |

All project documentation must be written in English.

## Module Documentation (`docs/dev/`)

Read the relevant module doc before modifying that area:

| Module doc | Covers |
|------------|--------|
| `conventions.md` | API response format, ID conventions, data models (RawItem, ProcessedItem), coding standards |
| `idl_and_db.md` | IDL modification workflow (kitex/hz), hz tool constraints, database migration scripts |
| `pipeline.md` | Async messaging (Redis Streams), 12-step item processing flow, replay log, embedding config, LLM |
| `feed_and_cache.md` | Feed flow, impression recording, 5-level cache architecture, cache config and testing |
| `auth.md` | Authentication flow, OTP, security mechanisms, mock OTP whitelist |
| `notification.md` | Notification service DAL, Redis keys, delivery dedup, audience expressions |
| `api_endpoints.md` | Gateway API endpoints, skill document structure, Swagger |
| `configuration.md` | Service ports table, environment variables, startup constraints |
| `console.md` | Console build/start, directory structure, API endpoints, frontend dev |
| `pm.md` | PM service methods, conversation types, friend/block relations, WebSocket push |
| `infra.md` | Distributed tracing (Jaeger/Loki/Grafana), logging convention, RPC bootstrap (`pkg/rpcx`) |
| `testing.md` | Test directories, run commands, manual email integration |

# IMPORTANT

## Build and Testing

After each code change, add or modify test cases. Run build and e2e tests to ensure functionality works.

- Test case code goes in `tests/`
- Don't add degradation logic just to make tests pass. Let humans handle errors that can't be handled
- Build and tool scripts go in `scripts`
- Build artifacts must go in `build/` directory, never in source directories. Always use `-o build/<name>` when running `go build` manually (e.g. `go build -o build/auth ./rpc/auth/`). Use `bash scripts/common/build.sh` for core services and `./console/console_api/scripts/build.sh` for console
- **Run build, start services, and tests autonomously.** All local dev scripts are idempotent and safe. Execute them directly without asking
- CLI: `./cli/scripts/build.sh` (cross-compile), `./cli/scripts/install-local.sh` (local install)

## Documentation Updates

After each code change, check if documentation needs updating, especially README.md and CLAUDE.md (including module docs under `docs/dev/`). Use clear and explicit language describing the current latest state. No process descriptions needed — git history can be queried.

## Code Cleanup

- Never comment out old code — delete it completely
- Never leave comments explaining what old code used to be
- Rely on version control to trace history
- Don't leave dead code, deprecated markers, or unused imports
