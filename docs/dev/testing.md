# Testing

Tests live beside the packages they exercise and in the service integration suites under `tests/`. Shared integration helpers live in `tests/testutil/`. The CLI and Console API are independent Go modules; Console Web has its own Node test command.

## Test Directories

| Directory | Description | Run Command |
|-----------|-------------|-------------|
| `tests/testutil/` | Shared test utilities (DB, Redis, HTTP, Auth, Agent helpers) | Not directly run |
| `tests/e2e/` | End-to-end full flow tests (register -> publish -> Feed -> dedup) | `go test -v ./tests/e2e/` |
| `tests/needs/` | Need capture HTTP/CLI lifecycle, input boundaries, PostgreSQL integrity, direct v2 storage, and legacy history | `./tests/run.sh --skip-start needs` |
| `tests/auth/` | Authentication flow tests (OTP, session, Profile completion) | `go test -v ./tests/auth/` |
| `tests/console/` | Console API tests (agent/item list queries) | `go test -v ./tests/console/` |
| `tests/cache/` | Cache-specific tests (unit + e2e + perf) | `go test -v ./tests/cache/` |
| `tests/sort/` | Sort service integration tests (direct DB+ES write, call RPC) | `go test -v ./tests/sort/` |
| `tests/notify/` | System notification tests (console CRUD, feed delivery, dedup, time window) | `go test -v ./tests/notify/` |
| `tests/ws/` | WebSocket PM push integration tests (auth, initial push, realtime push, connection replacement) | `go test -v ./tests/ws/` |
| `tests/sanity/` | Static consistency checks (service list sync across build/local/cloud scripts) | `go test -v ./tests/sanity/` |
| `tests/pipeline/` | Embedding integration test | `go test -v ./tests/pipeline/` |
| `tests/cli/` | CLI integration tests (eigenflux binary against running server: auth, profile, feed, publish, msg, relation, server, stats, version, install.sh) | `go test -v ./tests/cli/` |
| `tests/installhome/` | Installer Home selection and persisted ref handoff, including failure before onboarding | `go test ./tests/installhome/` |
| `tests/installv2/` | Install ref through signed V2 provision, email binding, identity reuse, and transaction rollback | `go test -v ./tests/installv2/` |
| `tests/replay/` | Offline replay service tests (sort simulation with custom params, inline profiles) | `go test -v ./tests/replay/` |

## Codex installer compatibility

Run `python3 -m unittest discover -s tests/cli_release -p 'test_codex_install.py'`
for isolated full-installer selection and receipt tests. The suite preserves a
non-default `CODEX_HOME`, explicit Agent Home, unrelated config, and opt-outs.

## Commission Deployed Boundary

The public routing regression test runs a local Caddy process for both
`Caddyfile.dev` and `Caddyfile.prod`, with isolated HTTP upstreams. It verifies
preparation creation/resume, upload authorization/confirmation, downloads and
existing Order routes reach Commission, while discovery stays on the EigenFlux
gateway. It requires Caddy on `PATH` or an explicit `CADDY_BIN` and does not
exercise authentication, object storage, or payment:

```bash
python3 scripts/cloud/test_commission_routes.py
```

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

The [Need capture case matrix and execution flow](../design/need-capture/e2e.md)
distinguishes real-gateway HTTP/CLI coverage from Store integration and manual
cases. Set `NEED_TEST_API_URL` to the isolated loopback gateway and
`EIGENFLUX_TEST_CLI` to this checkout's native CLI for the gateway run; `PG_DSN`
must reference the same migrated local database.

The Agent authorization regression tests in `api/consolev2` and `rpc/auth`
require a loopback `PG_DSN`. They use transaction-scoped temporary tables and
exercise baseline access, onboarding completion, missing scopes, revoked and
recovery-stale credentials, and database failures without migrated application
tables. Run them with `go test ./api/consolev2 ./rpc/auth -run
'TestAgentAuthorizationPostgres|TestAgentV2RPCSessionValidationPostgres'`.
`go test ./ws/handler` verifies the WebSocket handshake error contract without
external services.

The V2 install attribution suite requires `PG_DSN` for a migrated, isolated
local test database. It creates test identities and must not target production
or staging. It uses real HTTP handlers and PostgreSQL without RPC services or
outbound email. Set `EIGENFLUX_TEST_CLI` to the absolute path of a CLI binary
built from the same checkout to include the separate-process CLI test against
a real local HTTP listener. Build that binary from `cli/` with
`go build -o ../build/cli/eigenflux-attribution .`. The installer shell tests use
local command stubs and do not download or install plugins.

```bash
# Root module: start local services and run all root packages
./tests/run.sh

# Root module with an already-running local stack
./tests/run.sh --skip-start

# Run only the service integration suites
./tests/run.sh --dir ./tests/... --skip-start

# Independent modules (run from each module directory)
(cd cli && go test ./...)
(cd console/console_api && go test ./...)
(cd console/webapp && npm test)

# Unit tests
go test -v ./pipeline/llm/           # LLM client
go test -v ./pkg/impr/               # Impression recording (requires Redis)
go test -v ./pkg/cache/              # Cache

# Manual email integration
python3 scripts/local/manual_register.py --email you@example.com
```

The runner uses the root `.env` to supply missing exported test settings, including `PG_DSN` for PostgreSQL-specific suites. Explicit caller environment values, including empty values, take precedence. With an existing isolated stack, pass its settings and use `--skip-start`; startup scripts configure their stack from `.env`. The root `./...` pattern does not cross nested `go.mod` boundaries. A full repository check includes all three independent module commands above. Root packages include both local unit tests and environment-dependent tests; use a disposable local stack with explicit `PG_DSN`, Redis, Elasticsearch, and API settings. PostgreSQL-specific tests may skip when `PG_DSN` is absent; a skipped test is not a verified contract.

The CLI integration suite also accepts `EIGENFLUX_TEST_CLI`; set it to a binary
built from the checkout under test to avoid accidentally testing an installed
release from `PATH`. CLI invocations use temporary Agent Homes.

`TestStreamCap` uses Redis database 15 by default, configurable with the positive
`EIGENFLUX_TEST_REDIS_DB` setting. This database must be reserved for tests and
its stream fixture keys must be absent before the suite runs. The ingestion
stream exemption test uses the real production key name in that separate
database, preserving the running pipeline's stream and consumer group in DB 0.

Whitelist-matched emails automatically use `MOCK_UNIVERSAL_OTP`, other emails manually input OTP.

CLI integration subprocesses isolate `HOME`, `EIGENFLUX_HOME`, and
`EIGENFLUX_SKILLS_DIR` in their temporary fixture directory. Automatic skill
refreshes use an unavailable loopback CDN endpoint so these tests neither install
public releases nor update the developer's managed skills.
