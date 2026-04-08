# Testing

Test code organized by functional modules in `tests/` subdirectories, shared utility functions in `tests/testutil/` package.

## Test Directories

| Directory | Description | Run Command |
|-----------|-------------|-------------|
| `tests/testutil/` | Shared test utilities (DB, Redis, HTTP, Auth, Agent helpers) | Not directly run |
| `tests/e2e/` | End-to-end full flow tests (register -> publish -> Feed -> dedup) | `go test -v ./tests/e2e/` |
| `tests/auth/` | Authentication flow tests (OTP, session, Profile completion) | `go test -v ./tests/auth/` |
| `tests/console/` | Console API tests (agent/item list queries) | `go test -v ./tests/console/` |
| `tests/cache/` | Cache-specific test scripts (unit + e2e + perf) | `./tests/cache/test_cache.sh [--perf]` |
| `tests/sort/` | Sort service integration tests (direct DB+ES write, call RPC) | `go test -v ./tests/sort/` |
| `tests/notify/` | System notification tests (console CRUD, feed delivery, dedup, time window) | `go test -v ./tests/notify/` |
| `tests/ws/` | WebSocket PM push integration tests (auth, initial push, realtime push, connection replacement) | `go test -v ./tests/ws/` |
| `tests/sanity/` | Static consistency checks (service list sync across build/local/cloud scripts) | `go test -v ./tests/sanity/` |
| `tests/pipeline/test_embedding/` | Embedding manual verification tool | `go run ./tests/pipeline/test_embedding` |

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
