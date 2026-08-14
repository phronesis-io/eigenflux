# AGENTS.md - EigenFlux Server Development Guidelines

## Project Overview

Agent-oriented information distribution platform, built with Go and CloudWeGo microservices architecture. Read `docs/architecture_overview.md` before modifying code.

## Development Environment

- Go 1.25+
- Infrastructure: `docker compose up -d` (PostgreSQL, Redis, etcd, Elasticsearch, Kibana)
- Monitoring (optional): the full stack (Jaeger, Loki, Grafana, Prometheus, dashboards, alerting) lives in the private `phronesis-io/eigenflux-observability` repository — clone it and follow its README for local start. Then set `MONITOR_ENABLED=true` in `.env`. Application-side instrumentation (metrics/tracing code) stays in this repository.
- Default connection config in `pkg/config/config.go`, override via environment variables
- Build: `bash scripts/common/build.sh` (core), `./console/console_api/scripts/build.sh` (console)
- Start: `./scripts/local/start_local.sh` (core), `./console/console_api/scripts/start.sh` (console)
- All tests: `go test -v ./tests/...` (requires all services running)

## Directory Responsibilities

| Directory | Responsibility | Notes |
|-----------|---------------|-------|
| `api/` | HTTP Gateway | Hertz-based API gateway (port 8080). hz-generated code in `handler_gen/`, `router_gen/`, `model/`. RPC clients in `clients/`. Swagger docs in `docs/` |
| `console/` | Console subsystem | Independent Go module (`console.eigenflux.ai`). Own IDL, codegen, DAL, and build workflow. API (port 8090) and Web UI (Vite + Refine + Ant Design). Must not import root module packages |
| `rpc/*/` | RPC services | Kitex-based microservices (auth, profile, item, sort, feed, pm, notification). Business logic in `handler.go`, data access in `dal/`. Sort owns item recall, ranking, reranking, and feed ordering. |
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
| `api_endpoints.md` | Gateway API endpoints, skill document structure, Swagger, feed `output_contract` delivery |
| `configuration.md` | Service ports table, environment variables, startup constraints |
| `console.md` | Console build/start, directory structure, API endpoints, frontend dev |
| `pm.md` | PM service methods, conversation types, friend/block relations, WebSocket push |
| `infra.md` | Distributed tracing (Jaeger/Loki/Grafana), logging convention, RPC bootstrap (`pkg/rpcx`) |
| `testing.md` | Test directories, run commands, manual email integration |
| `sort.md` | Sort service responsibilities, item recall, ranking, reranking, and feed ordering |
| `rerank.md` | Item candidate contract and active freshness, boost, injection, and source-limit policies |

# IMPORTANT

## Production Deployment

`aliap` and `/data/git/eigenflux` are deployment-only. They are never a development workspace or a source of truth.

- Never edit source files, create a branch, commit, stash, or push from `aliap`.
- Every change must be developed in a local worktree created from the latest `origin/main`, verified locally, committed, pushed to a feature branch, reviewed in a pull request, and merged into `main` before deployment.
- Deploy only the resulting `main` commit. Never deploy an uncommitted worktree, a feature branch, or a commit that is not contained in `origin/main`.
- Before any deployment on `aliap`, read `/etc/eigenflux/DEPLOYMENT_POLICY.md` and require `git status --short` to be empty. If the production worktree is dirty, stop. Archive the changes, migrate them to a local worktree, and use the normal PR workflow; do not repair or commit them in place.
- The only permitted Git update on `aliap` is a read-only fetch followed by a fast-forward to `origin/main`. Verify the deployed SHA, migrations, service health, and a clean worktree after deployment.
- Production console deployment is managed separately and must not be inferred from a backend deployment.

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

## Agent skills

### Issue tracker

Issues tracked in GitHub Issues (`phronesis-io/eigenflux`) via the `gh` CLI; external PRs are not a triage surface. See `docs/agents/issue-tracker.md`.

### Triage labels

Default label vocabulary: needs-triage, needs-info, ready-for-agent, ready-for-human, wontfix. See `docs/agents/triage-labels.md`.

### Domain docs

Single-context: one `CONTEXT.md` + `docs/adr/` at the repo root. See `docs/agents/domain.md`.
