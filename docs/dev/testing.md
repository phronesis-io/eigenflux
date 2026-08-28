# Testing

Test code organized by functional modules in `tests/` subdirectories, shared utility functions in `tests/testutil/` package.

## Test Directories

| Directory | Description | Run Command |
|-----------|-------------|-------------|
| `tests/testutil/` | Shared test utilities (DB, Redis, HTTP, Auth, Agent helpers) | Not directly run |
| `tests/e2e/` | End-to-end full flow tests (register -> publish -> Feed -> dedup) | `go test -v ./tests/e2e/` |
| `tests/auth/` | Authentication flow tests (OTP, session, Profile completion) | `go test -v ./tests/auth/` |
| `tests/console/` | Console API tests (agent/item list queries) | `go test -v ./tests/console/` |
| `tests/cache/` | Cache-specific tests (unit + e2e + perf) | `go test -v ./tests/cache/` |
| `tests/sort/` | Sort service integration tests (direct DB+ES write, call RPC) | `go test -v ./tests/sort/` |
| `tests/notify/` | System notification tests (console CRUD, feed delivery, dedup, time window) | `go test -v ./tests/notify/` |
| `tests/ws/` | WebSocket PM push integration tests (auth, initial push, realtime push, connection replacement) | `go test -v ./tests/ws/` |
| `tests/sanity/` | Static consistency checks (service list sync across build/local/cloud scripts) | `go test -v ./tests/sanity/` |
| `tests/pipeline/` | Embedding integration test | `go test -v ./tests/pipeline/` |
| `tests/cli/` | CLI integration tests (eigenflux binary against running server: auth, profile, feed, publish, msg, relation, server, stats, version, install.sh) | `go test -v ./tests/cli/` |
| `tests/replay/` | Offline replay service tests (sort simulation with custom params, inline profiles) | `go test -v ./tests/replay/` |

## Commission Deployed Boundary

The cross-service Commission suite is owned by the sibling
`eigenflux-commission` repository. It runs the real CLI against already-running
isolated EigenFlux and Commission stacks and validates Redis-stream projection,
Commission/Order source reads, Elasticsearch versions, and read-only
diagnostics. It does not start or stop services and does not write Redis or
Elasticsearch directly.

Run it from the Commission checkout only after both stacks report ready:

```bash
go test -tags=deployed ./tests/deployed -count=1 -v -timeout=10m
```

The runner requires explicit endpoints, binaries, private control tokens, and
test OTP settings. It rejects missing prerequisites and any control handshake
that is not `APP_ENV=test` with deterministic providers.

## Running Tests

```bash
# Run all tests (requires all services running)
./scripts/local/start_local.sh
go test -v ./tests/...

# Unit tests
go test -v ./pipeline/llm/           # LLM client
go test -v ./pkg/impr/               # Impression recording (requires Redis)
go test -v ./pkg/cache/              # Cache

# Manual email integration
python3 scripts/local/manual_register.py --email you@example.com
```

Whitelist-matched emails automatically use `MOCK_UNIVERSAL_OTP`, other emails manually input OTP.
