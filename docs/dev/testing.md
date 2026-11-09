# Testing

Tests live beside the packages they exercise and in the service integration suites under `tests/`. Shared integration helpers live in `tests/testutil/`. The CLI and Console API are independent Go modules; Console Web has its own Node test command.

## Test Directories

| Directory | Description | Run Command |
|-----------|-------------|-------------|
| `tests/onboarding_prefill/` | Paired isolated-Agent prefill regressions: capabilities/history versus an explicit ongoing commission goal | See [prefill regression procedure](../../tests/onboarding_prefill/README.md) |
| `tests/dashboardsearch/` | Opt-in deployed Dashboard search, Console cookie and CLI checks; requires a disposable EigenFlux + Commission stack | See [Dashboard search](dashboard_search.md#validation) |
| `tests/testutil/` | Shared test utilities (DB, Redis, HTTP, Auth, Agent helpers) | Not directly run |
| `tests/e2e/` | End-to-end full flow tests (register -> publish -> Feed -> dedup) | `go test -v ./tests/e2e/` |
| `tests/needs/` | Need capture HTTP/CLI lifecycle, input boundaries, PostgreSQL integrity, direct v2 storage, legacy history, and versioned capture maintenance | `./tests/run.sh --skip-start needs` |
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

## CLI CI scope and command-test groups

`CLI Auth Cross-Platform` classifies the complete Git diff before starting Go
runners. Maintenance-only changes to `README.md`, `AGENTS.md`,
`skills/AGENTS.md` and Markdown under `docs/` run lightweight source/checker tests. Skills content and
onboarding-regression changes run the full command suite and Skills compatibility
suite on Linux. CLI code, dependencies, installer/release tooling, CI changes,
mixed changes and unrecognized paths require the full platform suite. Manual
runs, missing Git history and unknown diff identities also require full checks.

Linux always runs all `cli/cmd` tests. macOS and Windows exclude only tests
explicitly reviewed in `scripts/ci/portable-cmd-tests.json`: these cover prose
contracts, command/schema registration and pure JSON transformations. The
manifest records exact test names and normalized source hashes. New tests stay
cross-platform by default; edits to a reviewed file restore its tests to the
platform group until its behavior and dependencies are reviewed again. Update
the manifest's names and SHA-256 only after that review. Keep path, permission,
process, notification, installer and host-integration behavior cross-platform.

Run from the repository root:

```sh
python3 -m unittest discover -s tests/ci -p 'test_*.py'
python3 scripts/ci/cmd_tests.py all
python3 scripts/ci/cmd_tests.py portable
python3 scripts/ci/cmd_tests.py platform --list
```

The source job also tests the onboarding regression checker. Actual isolated
Agent generations and semantic review remain required by the
[behavioral regression procedure](../../tests/onboarding_prefill/README.md);
checker tests alone do not establish model behavior.

Release planning reuses CI only after the exact main-push workflow run succeeds
and its current attempt includes successful Linux, macOS, Windows, installer,
source and artifact-handoff jobs. A green docs/Skills-only run, skipped matrix,
missing jobs or unavailable evidence requires the existing release tests.
Superseded PR checks are cancelled only within the same workflow and PR;
main/manual runs stay isolated, and publication locks remain non-cancelling.

## Local Agent dispatch

Windows regressions close queue readers before atomic replacement and preserve
Skills rollback journals when the recovery target is a file or symlink. Tests
isolate both `HOME` and `USERPROFILE`; isolated PowerShell uninstall fixtures
use process-local `-ExecutionPolicy Bypass`, without changing machine policy.
Feed contract assertions account for the renderer's documented trimming.
`TestCodexHiddenPMWindowsOwnerAndSilentReceipt` exercises an isolated named pipe
and read-only child reader, including empty completed turns and rejection of
visible input, commentary, missing items, failed turns and permission requests.

From `cli/`, run `go test ./...` and
`go test -race ./internal/dispatch ./cmd -run 'Test.*(Dispatch|Binding|Decision|Journal|Runner|ACP|Watch)'`.
The dispatch tests use isolated Homes, signed Skills fixtures, local WebSocket
and HTTP servers, and fake subprocess Agents. They cover socket/poll deduplication,
fixed account routing, permission revocation, uncertain sends, crash recovery,
and ACP session/permission/cancellation behavior without contacting real users
or model providers. See [Local Agent Dispatch](agent-dispatch.md) for binding,
Windows packaging, manual acceptance, and unsupported-host boundaries.

## Agent V2 authentication regressions

Use an isolated PostgreSQL database for `PG_DSN`. Run
`go test ./api/consolev2 -run 'TestAgentRefreshPostgres|TestRefreshDatabaseFailureClassificationAndRedaction|TestAgentAuthorizationPostgres' -count=1`
to exercise sliding refresh windows, idempotent response-loss recovery, expired
versus revoked sessions, nonce/rotation/revocation SQL failures, commit failures,
and sanitized rejection logs. These tests create synthetic temporary tables and
must not point at production. Run the full package against a migrated test
database to include the existing Console flows.

In `cli/`, run `go test ./...` and
`go test -race ./cmd ./internal/auth -run 'TestStream|TestV2|TestExpiredV2|TestHeartbeatStops|TestCommandFailure' -count=1`.
The suites use temporary Agent Homes, local HTTP servers, and real local
WebSocket connections. They cover same-key recovery without account creation,
concurrent refresh reuse, repeated stream renewal with cursor preservation,
single-attempt heartbeat failures, and host notification/exit classification.
Heartbeat authentication-failure regressions use a signed local CDN and verify
Skills synchronization before a single credential attempt. Feed/Runtime missing
key tests verify that no replacement identity or authentication request is made.
Temporary credential failures, including recovery rate limits, retain exit 2
and the structured non-login notice;
unrelated endpoint failures do not receive that notice.
Run `go test ./tests/installv2` with a migrated test database and
`EIGENFLUX_TEST_CLI` pointing at the built CLI. The real CLI/handler recovery
test verifies preserved Agent, principal, key, profile, draft, and attribution,
with public registration enabled and disabled, no extra account or handoff, and
the guarded manual recovery command using the same endpoint. Revoked expiry
must exit 4 without provisioning. Unavailable local keys in guarded provisioning
must also exit 4 with an owner-notification notice and preserve saved credentials.
Owner-action regressions verify separate instructions for inaccessible local
identity files and rejected V2 sessions, including the current Home and server,
an explicit owner reply, revocation confirmation, and preservation of the
existing host instruction and exit code. They verify that notice generation
does not modify saved identity files or disclose their contents, that legacy
failures omit V2-specific actions, and that credential 503/429 failures retain
their non-login classification without an owner recovery action.

## Business access logging and cached telemetry

Run `go test ./api/consolev2 ./api/middleware` for rejection reason, route-template,
trace retention, and credential/error redaction coverage. The PostgreSQL Agent
authorization contracts above verify the unchanged access predicates against
native array/NULL types. In `cli/`, `go test ./cmd -run TestProfileFunnel`
checks that expired cached access causes no HTTP or authentication request and
leaves credentials unchanged, while usable V1/V2 access still reports events.

## Host installer compatibility

Run `python3 -m unittest discover -s tests/cli_release -p 'test_openclaw_install.py'`
for isolated full shell-installer gateway selection, preservation on unsupported
versions, and continuation of other requested host setup. Run the same command
with `test_windows_installation.py` for the PowerShell selector and Windows
installation regressions. The gateway selector runs wherever PowerShell is
available; the remaining Windows cases require Windows. Skipped cases do not
establish runtime acceptance.

Run `python3 -m unittest discover -s tests/cli_release -p 'test_codex_install.py'`
for isolated full-installer selection and receipt tests. The suite preserves a
non-default `CODEX_HOME`, explicit Agent Home, unrelated config, and opt-outs.
It covers the invoking runtime path, current macOS app bundle layouts, and
fallback from missing or incompatible candidates.

Set `EIGENFLUX_TEST_CODEX` to a compatible native Codex executable and run
`python3 -m unittest discover -s tests/cli_release -p 'test_codex_marketplace_cleanup.py'`
for marketplace removal/reinstall acceptance. The suite uses temporary host
Homes and a local Git catalog with external Git transports disabled. It covers
registered and orphaned marketplace roots and preservation of unrelated state;
it never installs a plugin or uses the user's live Codex configuration. Without
the explicit executable this native suite is skipped, not counted as acceptance.

Run `python3 -m unittest discover -s tests/cli_release -p 'test_skills_install.py'`
for GitHub Skills bootstrap coverage when signed CDN synchronization fails.
The suite checks CLI compatibility, preservation of existing Skills on rejection,
and complete installation of `ef-support`, including its references and binary
QR asset. Development-only and unrelated Skills remain excluded.

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

CLI preparation regressions run from `cli/` with
`go test ./cmd -run 'TestCreateOrder(PreparationOrderPresence|UploadsLongTextAndResumesFinalizedPreparation|DoesNotFinalizeFailedUpload)'`.
Both create and resume must upload and confirm material bytes before finalizing
when the response has no Order, null, an empty object, or `order_id: 0`.
Only a positive Order ID matching the preparation permits skipping uploads on
a finalized retry; malformed, negative or mismatched IDs stop the command.

## Legacy authentication fixtures

Legacy login no longer creates identities. `LoginAndGetToken` and
`RegisterAgent` seed a legacy identity and profile in the test database before
performing real API login. Direct `LoginStart` and `LoginStartRaw` calls never
seed data; login-policy and OTP tests must explicitly prepare existing identities
with `EnsureLegacyTestAgent`. Unknown-email tests assert code `410` and no new
Agent or OTP challenge.

Run `go test ./tests/testutil -run TestEnsureLegacyTestAgentPostgres -count=1`
with an explicit loopback `PG_DSN` to validate fixture creation and preservation
of existing identity/profile fields. The test copies the migrated table schema
into a disposable schema and removes it afterward.

## Running Tests

Publish persistence has colocated failure, cancellation and own-listing tests
in `rpc/item`. Native PostgreSQL tests also exercise competing dispatchers,
Redis failures, lost SQL acknowledgements, interrupted cleanup, legacy ownership
and migration rollback guards. These contracts accept only
an explicitly configured loopback `pgc_publish_*` test database:

```bash
PUBLISH_PG_TEST_DSN='postgres://user:password@127.0.0.1:5432/pgc_publish_atomic?sslmode=disable' \
  go test -race ./rpc/item -run '^TestPostgresPublish' -count=1
```

Each case creates and drops its own schema. The PostgreSQL cases skip when that
dedicated variable is absent; a skipped case does not establish native database
acceptance. Ordinary service suites continue to use `PG_DSN` separately.

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

## Search and recommendation discovery

The opt-in [process E2E suite](../../tests/discoverye2e/README.md) starts actual
API/Feed/Sort/Item binaries and verifies the enabled online pipeline through HTTP,
real stores and replay consumption. Build first, then run
`DISCOVERY_E2E=1 ./tests/run.sh --skip-start discoverye2e -count=1` against isolated,
migrated infrastructure with no other RPC services registered in its etcd.
It owns service startup; do not run `start_local.sh` for this suite. Embedding and
external commission/order RPCs use deterministic fixtures.

Run `go test ./rpc/sort/... ./rpc/feed/... ./pkg/replaylog ./api/consolev2 ./pipeline/consumer` for contract, ownership, page, idempotency and replay checks.

Real-store tests require `DISCOVERY_TEST_DSN` (an isolated migrated PostgreSQL database), `DISCOVERY_TEST_ES` (isolated Elasticsearch URL), and `DISCOVERY_TEST_REDIS` (isolated Redis address). Run `go test ./rpc/sort/discovery ./pipeline/consumer -run 'TestPostgres|TestDiscoveryReplayPostgres' -count=1`. These tests use temporary schemas/indices and fixture rows; do not point them at production or run alongside integration suites that reset the same database. Agent projection and ranking hydration use real stores; Commission catalogue/statistics snapshots are fixtures written through the projection store. Validating remote Commission event production remains a cutover check.

CLI checks run in the independent `cli` module: `go test ./cmd ./internal/client ./internal/cache ./internal/feedevent`. Signed release builds still use the existing signing workflow; a local compilation uses `go build -o ../build/eigenflux-cli .`.

For asynchronous Need vectors, migrate the isolated database through 000110 and
set `DISCOVERY_TEST_REDIS` to its loopback Redis address. Run
`go test ./tests/needs -run TestNeedEmbedding -count=1` with `PG_DSN` set. Tests
exercise the real embedding HTTP client, worker, generation-specific job leases
and Redis cache. `tests/discoverye2e` additionally verifies cold/warm vector use
through real API/Feed/Sort processes. Unit tests under
`rpc/sort/discovery/needembedding` verify cache identity and invalid vectors.

## CLI release integrity and Windows download tests

Run `python3 -m unittest discover -s tests/cli_release -p 'test_install_attribution.py'`
for bounded installer reporting retries, HTTP/business failures, and invalid
responses. The production shell functions run with an isolated curl fixture.
`go test ./cmd ./internal/auth ./internal/client` in the CLI module covers V2
report failure and reprovision retry using the selected server and saved ref.
The real-CLI `TestCLIInstallRefToV2Identity` in `tests/installv2` also verifies
that provision repairs a pending install and repeated reports preserve the
first conversion timestamp.

Run `python3 -m unittest discover -s tests/cli_release -p 'test_cli_artifacts.py'`
for checksum generation, publisher preflight, failed uploads, public CDN
verification and version-promotion ordering. Tests use temporary directories,
a stub object-store client and a local HTTP server, with no production writes.

On Windows, build the CLI and run the download suite:

```powershell
New-Item -ItemType Directory -Force build | Out-Null
Push-Location cli
go build -o ../build/eigenflux.exe .
Pop-Location
python -m unittest discover -s tests/cli_release -p 'test_windows_download.py'
python -m unittest discover -s tests/cli_release -p 'test_windows_attribution.py'
```

Both Windows PowerShell 5.1 (`powershell`) and PowerShell 7 (`pwsh`) must be on
PATH. The suite parses the production installer's download helper, downloads
and executes the real CLI from a loopback server, and checks missing/malformed/
mismatched checksums, HTTP errors, retry exhaustion and temporary-file cleanup.
Failed verification must preserve the previously installed bytes. The existing
CLI Auth Cross-Platform workflow runs these tests in an independent Windows
download job, separate from the identity tests.
Non-Windows hosts skip this suite. This is download integration coverage, not
full host onboarding or Smart App Control acceptance; see [CLI release
integrity](cli-release.md).

The Windows attribution suite uses the real CLI with temporary Homes and a
loopback HTTP server to verify parameter validation, per-server referral
persistence, existing-identity preservation, retry bounds, and failures before
Skills setup. It runs on both Windows PowerShell 5.1 and PowerShell 7 in the
existing Windows installer CI job. A non-Windows host can run PowerShell 7
coverage by setting `EIGENFLUX_TEST_PWSH` and `EIGENFLUX_TEST_CLI` to portable
PowerShell and a native CLI built from this checkout; this does not establish
Windows PowerShell 5.1 acceptance.

## Skills and Feed queue OS locks

See [`cli/internal/filelock/README.md`](../../cli/internal/filelock/README.md)
for the shared Mac/Windows implementation and upgrade boundary. Skills sync,
bundle install/uninstall and Feed enqueue/flush retain their lock files; file
presence no longer indicates an active owner. Signed Skill reads probe ownership
without creating or modifying a lock. Run the filelock, skills and feedevent
packages with `-race`; verify forced child termination permits immediate reuse,
old marker contents do not block an idle target, active owners remain exclusive,
and retained idle locks do not disable signed dispatch. Keep atomic Skills
recovery and preservation tests. Stop old CLI writers before adopting OS locks;
normal Skills directory cleanup may still require WorkBuddy authorization.


CLI feedback completion regressions run with `go test ./cmd ./internal/feedevent`
from `cli/`. They cover concurrent subprocess writers, process death while a
report lock is held, 72-hour queue expiry, exact exposure attribution, scheduled
whole-page pulls that always fetch, opt-in saved batches, a corrupt ledger that never blocks Feed or feedback,
lifecycle commands that skip the queue flush, Unicode status bounds and health
fields. HTTP fixtures exercise the real
CLI command paths; production cohort metrics remain rollout acceptance checks.

Feed batching regressions also cover cache/lock failure fallback, old-ledger
upgrade, concurrent process handouts, open-before-backlog order, 24-hour backlog
expiry, owner checks that always fetch, re-hand limits, current control context,
post-push surface reminders that the pre-push check never consumes, busy-ledger
status output and local health-counter isolation. Run `go test -race ./cmd ./internal/feedevent
./internal/cache` from `cli/`.

`cli/cmd/feed_batch_output_test.go` models a large page with ten authors, ten
discovery entries, eight Chinese intent instructions and five actions per item.
Its five-item agent batch must stay within 32 KiB, omit foreign cards, duplicate
discovery previews and contract details, and carry instruction references.
The suite also checks exact-text matching against current intents, unchanged
JSON rendering, contract hashes/content/retention/write-failure fallback and
160 pre-upgrade exposures that remain pending without being re-handed.

With isolated migrated infrastructure and built services, run
`DISCOVERY_E2E=1 EIGENFLUX_TEST_CLI="$PWD/build/cli/eigenflux-darwin-arm64"
./tests/run.sh --skip-start discoverye2e -c TestFeedCLISavedBatchesKeepOneExposureSet`
(select the binary for your platform). This suite starts its own application
processes. It scores a batch, reads two saved batches, then polls locally and
checks the real replay/feedback rows retain the single original impression.
It also reads the next batch in agent format, verifies the referenced served
contract file and re-hands a skipped item without creating a new exposure.

Build the CLI with `cd cli && go build -o ../build/eigenflux-cli .`, then run
`EIGENFLUX_TEST_CLI="$PWD/build/eigenflux-cli" ./tests/run.sh --skip-start e2e -c TestCLIFeedbackProcessRecovery`
from the repository root. This acceptance test kills a real CLI process after
the local API accepts feedback but before the response reaches the client. The
restarted CLI drains the durable queue; the real worker stores one feedback row
and increments the score counter once. It uses an isolated Agent Home and local
HTTP proxy, with no host delivery or production writes.

## Enterprise SDK acceptance

The managed integration suites use an explicit loopback PostgreSQL DSN and
isolated schemas. Run `go test -race ./pkg/integrationidentity
./pkg/integrationprofile ./pkg/integrationfeed ./api/integrations
./scripts/integration_admin` with `INTEGRATION_TEST_PG_DSN` set to a migrated
throwaway database. These suites cover anonymous slots, operator controls,
context/privacy, Feed admission/retry, observations and safe credential files.

`TestEnterpriseSDKAcceptance` and `TestEnterpriseSDKFailedContextAcceptance` require the private binary wheel and
its Python virtualenv. It runs a native gateway, actual worker/ranking/Feed code,
and the runnable B-side consumer with fixed external dependencies. It advances
only isolated timestamps and performs no production access. Run that test alone;
see [the full acceptance instructions](enterprise-integrations.md#reference-application-and-end-to-end-acceptance).
## Broadcast detail inspection and attribution

Run `go test -race ./pkg/followuplog` with a loopback `PG_DSN` to test event
identity, request-cancellation survival and the exact SQL attribution export.
The PostgreSQL case uses temporary tables and checks foreign Agents/items,
non-deliveries, other source kinds, missing and ambiguous impressions,
before-exposure timestamps, time-window boundaries and search separation.
From `cli/`, run `go test -race -short ./cmd -run TestFeedGet` for explicit,
handed, unhanded and missing impression handling.

Against isolated running services, `./tests/run.sh --skip-start e2e -c
TestFeedDetailSignals` verifies successful detail reads reach the real stream
consumer and PostgreSQL once per exposure; unattributed reads stay separate.
Author/failed/unauthenticated reads produce no signal, clients cannot mint the
server-only event, and reads create neither scores nor Swing seeds.
`TestFeedCLISavedBatchesKeepOneExposureSet` in the discovery process suite also
checks actual CLI attribution and the V2 source-detail route. See that suite's
startup-isolation instructions above.

## Native CLI self-update regressions

From `cli/`, run `go test ./internal/selfupdate -count=1`. The CLI cross-platform
workflow includes this package and triggers for changes to its sources. Windows
runners exercise complete signed-package updates with real handles preventing
staging-directory rename: temporary release permits activation, persistent
refusal stops within the two-second retry window, and cancellation preserves
the old release and pointer. Existing native real-CLI tests cover consecutive
complete updates through the stable entry. macOS runners verify bounded local
tool diagnostics, plist errors, helper version mismatch and invalid helper
signatures; every validation failure leaves the package unactivated.

Feed context-budget regressions in `cli/cmd/feed_context_budget_test.go` cover
exact rendered bytes for agent/JSON, dropped-candidate hand state, explicit
oversize reporting, cycle isolation/expiry/close and read-only recovery of cached
details. Run `go test -short ./cmd ./internal/feedevent ./internal/cache` from
`cli/`; the short flag excludes native self-update packaging, not Feed coverage.
API preview provenance and health snapshots are covered by
`go test ./api/consolev2 ./api/dal ./pkg/feedcontract`. Apply migration 000125
before validating settings against a real local database.

## Superseded pull-request checks

CLI cross-platform, PostgreSQL contracts, and release annotation validation group
PR runs by workflow and PR number. A new commit cancels older checks in that
group. Main pushes and manual runs use unique run IDs and are not canceled by
this policy. Publication workflows retain their existing serialization.
