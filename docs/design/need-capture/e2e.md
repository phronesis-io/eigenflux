# Need capture E2E case matrix and execution flow

## Scope and test boundary

Branch: `codex/need-capture`.

The online path is confirmed active Intent → Agent source read → NeedInput
submission → atomic basic normalization → owner-scoped get/list → eligibility
after an Intent mutation. The CLI is a separate process calling the HTTP gateway.
Optional offline enrichment uses the internal `Store.Enrich` boundary; it has no
public HTTP endpoint.

The automated suite seeds completed identities, credentials, and confirmed
Intents in a migrated local PostgreSQL database. It does not prove Console UI
confirmation, onboarding, or an Agent's semantic interpretation of human text.
With `NEED_TEST_API_URL` it exercises the real running gateway; without that
variable it starts an in-process HTTP service and is an integration test.
Store concurrency and failure-injection tests remain integration tests in either
mode. Search, Feed, vocabulary generation, enrichment scheduling, purchases, and
outbound contact are outside this branch's acceptance boundary.

`eligible=true` means an active projection of a normalized input linked to the
current active Intent version. It does not imply taxonomy coverage, deadline
validity, candidate matching, or permission to perform the Intent's action.

## Case matrix

P0 cases protect ownership, source linkage, retry identity, and durable state.
P1 cases protect field limits, read semantics, and optional enrichment behavior.
Coverage below describes executable assertions, not a claim that every matrix
variant has been automated. `Manual` rows are explicit remaining checks.

| ID | Priority / boundary | Preconditions and action | Expected result / durable assertion | Coverage |
| --- | --- | --- | --- | --- |
| F01 | P0 happy path | Completed owner, active Intent v1; read sources and create a broadcast Need | Source ID is a string and version is 1; 201, replayed=false; normalized input and basic.v1 projection commit together; unmapped, empty taxonomy, eligible=true | HTTPAndPostgres |
| F02 | P0 one-to-many | Create broadcast, agent, commission inputs for the same Intent with different keys | Three independent inputs; kind remains on input, no duplicated source fields inside projection | RevisedFields |
| F03 | P0 CLI | Submit the complete Skill example, retry, get, list | One ID; replayed=true; original phrases and integer CNY budget round-trip | CLI |
| F04 | P0 source preservation | Submit whitespace/newlines and an ordered phrase array; read after normalization | Original strings/order preserved; derived text cleaned separately; source snapshot retained | HTTPAndPostgres, HTTPRevisionLifecycle; normalization unit tests |
| A01 | P0 authentication | Create without credentials | 401; no successful capture | HTTPAndPostgres |
| A02 | P0 readiness/scopes | Incomplete onboarding; then completed but read-only credential attempts create | 409 for incomplete onboarding; 403 for missing write scope | HTTPAndPostgres |
| A03 | P0 read isolation | Owner B gets A's ID and lists own inputs | Foreign get is 404; B's list excludes A; same key used by B remains independent | HTTPAndPostgres, HTTPRevisionLifecycle |
| A04 | P1 all read gates | Missing/expired/revoked credentials, incomplete onboarding, or write-only scope on source/get/list routes | Reject per V2 auth contract; no source/projection disclosure | Manual Need-route matrix; shared auth suite covers credential validation |
| I01 | P0 stale source | Read v1, update Intent through HTTP to v2, then create under a new key using v1 | 409 INTENT_REVISION_STALE; no new input | HTTPAndPostgres, HTTPRevisionLifecycle |
| I02 | P0 unavailable source | Submit paused/deleted/foreign Intent; also a never-existing Intent ID | Same 409 stale-link response; do not disclose foreign existence | HTTPAndPostgres covers paused/deleted/foreign; missing ID remains manual |
| I03 | P0 retry after edit | Replay original key/body after v2; create v2 under a new key | Old ID and source snapshot preserved, eligible=false; new ID eligible=true; get/list and current view agree | HTTPRevisionLifecycle |
| I04 | P0 mutation race | Hold uncommitted Intent version update; start capture using v1; commit update | Capture waits for source lock, then rejects stale version | WaitsForIntentMutation (Store integration) |
| I05 | P1 soft/hard deletion | Pause or soft-delete source; then hard-delete Intent/account | Soft states exclude projection from current view, preserve history; hard deletion cascades | IntegrityAndEligibility; HTTPAndPostgres |
| K01 | P0 idempotency | Same owner/key and identical body; then changed phrase under same key | 200 and same ID for replay; 409 IDEMPOTENCY_CONFLICT for changed input | HTTPAndPostgres |
| K02 | P1 canonical JSON | Retry with indentation/object formatting changed; then reverse array order | Formatting replays; array reordering conflicts even if normalization would yield equivalent meaning | HTTPRevisionLifecycle |
| K03 | P0 concurrent retry | Eight simultaneous Store creates with same owner/key/body | Exactly one input; all callers return the same ID | HTTPAndPostgres (Store integration); simultaneous HTTP retry remains manual |
| K04 | P1 lost response | Discard a successful create response and retry original key/body | Recover the same committed ID; never invent a new key on transport retry | Manual transport scenario; sequential retry contract automated |
| B01 | P1 description | ASCII 200/201, CJK 100/101, whitespace-only, embedded NUL | Boundary values accepted; excess/blank/NUL rejected with 400 INVALID_NEED_INPUT | HTTPBoundaries |
| B02 | P1 phrases | Arrays of 0/1/10/11 entries, including duplicates | Only 1–10 accepted before dedup; original duplicates retained, derived phrases deduped | HTTPBoundaries; normalization unit tests |
| B03 | P1 priority | Omitted, 0, 1, -0.01, 1.01, explicit null | Omitted/0/1 accepted; other values rejected; never copy Intent priority=10 into Need priority | HTTPBoundaries; normalization unit tests |
| B04 | P1 money/delivery | Commission budget=0 and delivery=0; negative/fractional budget; missing currency; budget on broadcast; unsupported currency | Zero preserved; invalid amounts/types rejected; budget needs CNY; commission-only fields rejected on other kinds | HTTPBoundaries, RevisedFields; contract unit tests |
| B05 | P1 deadline | deadline_ms=1 (past), 0, negative, omitted | Positive past value accepted and preserved; 0/negative rejected; omission stays unknown; past deadline alone does not make eligible=false | HTTPBoundaries covers 1/0; negative remains manual |
| B06 | P1 body bytes | Valid JSON padded with trailing whitespace to exactly 32768/32769 bytes | 32768 accepted; 32769 returns 413 NEED_INPUT_TOO_LARGE; no partial rows | HTTPBoundaries |
| B07 | P1 key length | ASCII keys of 7/8/128/129 bytes; internal space | Only 8–128 printable non-space ASCII accepted | HTTPBoundaries |
| B08 | P1 IDs | Numeric JSON ID, leading-zero string, int64 overflow | 400; source ID must be canonical positive int64 string | HTTPBoundaries |
| B09 | P1 strict JSON | Duplicate fields, derived normalized_need field, trailing JSON document | 400 and zero input/projection rows | HTTPBoundaries |
| B10 | P1 remaining field limits | Phrase 200/201 weighted chars; preferences 500/501; each constraint array 20/21, each value 100/101; empty values; invalid UTF-8 | Exact maxima accepted, excess/invalid values rejected; all rejected submissions leave zero rows | Manual complete boundary grid; selected contract unit coverage |
| R01 | P1 pagination | Two inputs, limit=1, follow next_cursor | Descending IDs, no duplicate, final cursor empty | HTTPAndPostgres |
| R02 | P1 read edge cases | Empty owner list; limit=0/1/100/101, malformed/negative/overflow cursor; malformed/missing get ID | Empty array with empty cursor; invalid pagination/ID 400; valid missing ID 404 | Empty list and limit=100: HTTPRevisionLifecycle; 1/101: HTTPAndPostgres; remaining variants manual |
| R03 | P0 privacy | Inspect create/get/list and error responses | Cache-Control: private, no-store | Shared HTTP request assertion |
| N01 | P0 unknown vocabulary | Capture without offline vocabulary/model/queue | Immediate unmapped basic projection with usable desc/phrases; no waiting | All successful HTTP capture cases; dependency independence established by implementation boundary |
| N02 | P1 unknown restrictions | lang=[English, unknown dialect], region=[US, unknown region] | Entire ambiguous OR dimension retained unresolved; do not silently narrow to known alternatives | Normalization unit tests; manual HTTP round-trip |
| N03 | P1 offline coverage | Enrich baseline with partial then full vocabulary | unmapped → partial → mapped; all eligible; source unchanged; three retained projections, one active | OfflinePublicationCoverageAndCAS |
| N04 | P0 publication race | Two publishers share expected active ID; stale publisher retries; change mappings under same taxonomy version | One winner; stale/changed revision conflicts; identical vocabulary replay preserves identity | ConcurrentOfflinePublishersAndOnlineRetries; OfflinePublicationCoverageAndCAS |
| N05 | P0 stalled/failing offline work | Hold unpublished supersede transaction; get/list/retry/create; rollback; inject allocation failure | Reads see committed result; online capture continues; failed publish retains prior active projection | OfflineEnrichmentDoesNotBlockOnlineReadsOrCapture; NormalizationAndEnrichmentAreAtomic |
| N06 | P0 atomic create | Fail projection ID allocation after input ID allocation | Transaction rolls back input and projection; no pending orphan | NormalizationAndEnrichmentAreAtomic |
| N07 | P1 legacy retry | Existing pending/failed input without projection; replay; superseded input replay | Pending/failed upgrade with same ID; superseded stays inactive | PendingInputRetryGetsOnlineBaseline covers pending; failed/superseded remain manual |
| D01 | P0 DB integrity | Insert projection for wrong owner/version, empty normalizer, duplicate revision, second active projection | Constraints reject each invalid linkage/state | IntegrityAndEligibility |

Test names in the table omit the `TestNeedInput`, `TestNormalizedNeed`, or `Test`
prefix where unambiguous. See `tests/needs/capture_test.go`,
`tests/needs/boundaries_test.go`, and `tests/needs/normalization_test.go`.

## Execution flow based on the matrix

| Step | Cases | Action / input | Required evidence |
| --- | --- | --- | --- |
| 0 | Prerequisites | Build this branch's gateway and native CLI. Start an isolated local stack with migration 105 and Console V2/control routes enabled. Use two completed test identities A/B with read/write scopes and separate active v1 Intents | Branch SHA, CLI path, loopback API/DB, migration version, build/start logs; no production data |
| 1 | F01, A03, R02 | With A read current source ID/version. With B list inputs before capture | A sees its active v1 source; B has an empty list |
| 2 | F01–F04, N01 | Create the basic broadcast example and the complete commission Skill example through HTTP/CLI; immediately get/list | 201, one input + one projection per key, same source snapshot, basic.v1/unmapped/eligible, CNY amount preserved |
| 3 | K01–K04 | Retry original payload/key, retry formatted JSON, change text/order, repeat key across owners; execute concurrent Store case | Same ID on valid replay; conflict on changed input; one durable row per owner/key |
| 4 | B01–B10, A01–A04, I02 | Run each negative/boundary case independently against an unchanged active Intent; use a fresh key per case | Expected status/error code; accepted cases have one input and projection; rejected cases have neither; retain manual outcomes separately |
| 5 | R01–R03 | Page through A's inputs and read an A ID with B credentials | Descending stable traversal, correct end cursor, no foreign disclosure, private/no-store |
| 6 | I01, I03 | Update Intent through PUT using expected_context_revision=1. Read v2. Try v1/new key, v1/original key, then v2/new key | Stale create rejected; old replay returns eligible=false and unchanged history; v2 capture eligible=true; current view contains only current source versions |
| 7 | I04–I05, D01 | Use fresh fixtures for source lock, soft/hard deletion, and invalid projection insertion cases | Rejected stale race; correct eligibility and cascade behavior; database constraints enforced |
| 8 | N02–N07 | Use fresh fixtures for unknown restrictions, partial/full enrichment, failure/lock/CAS and legacy recovery cases | Source text retained; previous projection survives failure; one active projection; historical version chain remains readable in DB |
| 9 | Cleanup | Let fixture cleanup remove only its A/B agents and dependent records; close temporary HTTP listeners; stop only the isolated services started for this run | Test process exit code, no unexpected skips, no fixture rows left; logs contain no credentials |

Minimal online input (replace ID/version with the source read):

```json
{
  "schema_version": "need_input.v1",
  "intent_id": "123",
  "intent_version": 1,
  "need_type": "broadcast",
  "target": {
    "desc": "Find PostgreSQL index tuning resources",
    "candidate_needs": ["PostgreSQL indexes", "database performance"]
  },
  "constraints": {"lang": ["English"]}
}
```

## Reproducible automated run

Configure the checkout's `.env` for a disposable local stack first. Startup
scripts source that file; use dedicated ports and `PROJECT_NAME`, a migrated
loopback `PG_DSN`, `ENABLE_CONSOLE_V2=true`,
`ENABLE_CONTROL_CHANNEL_V2=true`, and test-only Console bootstrap/OTP secrets.
Do not print those secrets in run evidence. Run the following from this checkout;
set `NEED_TEST_API_URL` to the API port actually configured in `.env`.

```bash
bash scripts/common/build.sh
(cd cli && go build -o ../build/cli/eigenflux-need-e2e .)
./scripts/local/start_local.sh
export NEED_TEST_API_URL=http://127.0.0.1:18083
export EIGENFLUX_TEST_CLI="$PWD/build/cli/eigenflux-need-e2e"
./tests/run.sh --skip-start needs -timeout=5m
go test ./pkg/need ./api/consolev2 ./api/middleware
(cd cli && go test ./cmd -run 'TestNeed|TestContextIntent')
```

`tests/run.sh` loads `PG_DSN` from `.env`; explicit exported settings take
precedence. To select only the new HTTP boundary/lifecycle cases:

```bash
./tests/run.sh --skip-start needs --case 'TestNeedInputHTTP(Boundaries|RevisionLifecycle)$'
```

Without `PG_DSN` PostgreSQL tests skip; without `EIGENFLUX_TEST_CLI` the CLI test
skips. Neither is acceptable evidence for a complete run. A green automated run
does not mark the manual matrix variants complete. Record each manual variant as
PASS/FAIL/NOT RUN with its actual response and database observation.

Run record fields: date, branch/SHA, endpoints (no credentials), migration,
command/exit code, passed/failed/skipped tests, manual case outcomes, remaining
gaps, and cleanup result.

## Recorded automated result — 2026-09-24

- Source: `codex/need-capture`, base commit `2dc59b7c` plus the boundary/lifecycle
  tests and this matrix in the working tree.
- Environment: isolated `ef-need-capture-test`, migration 105,
  gateway `http://127.0.0.1:18083`, local PostgreSQL port 15443.
- CLI: `build/cli/eigenflux-need-e2e`, built from this checkout.
- Core build: all 12 service binaries passed; local startup passed.
- Gateway run: `./tests/run.sh --skip-start needs -timeout=5m` with the gateway
  and CLI environment variables above; exit 0, all 12 top-level tests passed,
  including all 35 HTTP boundary subtests; zero skips.
- Additional checks: `go test ./pkg/need ./api/consolev2 ./api/middleware`,
  `go vet ./tests/needs`, CLI `go test ./cmd -run 'TestNeed|TestContextIntent'`,
  and `git diff --check` passed. The direct package command did not configure
  PostgreSQL; its PostgreSQL-only cases are not additional DB evidence.
- Gateway run log: `build/need-e2e-results.log` (local, untracked).
- Cleanup query: zero `need-%@test.com` agents, zero NeedInputs, and zero
  Normalized Needs remained in this isolated database. Application processes
  exited with their parent execution session.
- Manual matrix variants: NOT RUN. Console UI confirmation, full onboarding,
  Agent interpretation, HTTP transport fault injection, and Search/Feed delivery
  were not validated by this run.
