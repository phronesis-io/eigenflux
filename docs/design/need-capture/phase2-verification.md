# Need capture phase 2 verification

Validated locally on 2026-11-06. The implementation is split into two stacked
branches: `codex/need-phase1` contains the heartbeat execution and receipt changes;
`codex/need-phase2` adds decision guidance and explicitly selected historical
`no_need` re-review. Review phase 2 against phase 1 to see only the second stage.
Neither branch changes the production deployment by being pushed.

## Changes and runtime prerequisites

- CLI 1.0.16 and ef-broadcast 0.16.3 preserve phase 1's two-Intent limit and
  120-second soft admission budget. A current extraction is never killed by it.
- The capture guidance defines a recommendation Need, considers all supported
  types, explains legitimate `no_need`, and includes positive/negative cases.
  Missing optional details or analyze-only policy do not suppress a supported Need.
- `need capture review` reads one current server review. `need capture reopen`
  requeues only its explicitly selected `no_need`, without editing the Intent.
- Migration 000132 adds review revision fencing. Late old submissions cannot
  finish a reopened review. Reopening retries cannot erase a newer result.
- No history is automatically reset on migration, Skill update or heartbeat.
  No third-stage content filter or priority rotation is included.

Deploy migration 000132 and the updated API before selecting historical reopens.
Upgrade the participating Agents to CLI 1.0.16 and the matching Skills. An initial
review still omits revision zero, preserving its original canonical request hash.
An older client cannot complete reopened work. Do not drop the migration after
any reopen; its Down guard protects against old requests becoming valid again.

## Validation completed

| Check | Result |
| --- | --- |
| CLI module `go test ./...` | Passed |
| CLI capture and local maintenance tests with `-race` | Passed |
| `go test ./pkg/need ./api/consolev2 ./migrations` | Passed |
| All migrations through 000132 on isolated PostgreSQL 16 | Passed |
| Existing capture lifecycle and Pending-bound HTTP/PostgreSQL tests | Passed |
| Selected re-review lifecycle, ownership, scopes and onboarding gates | Passed |
| Concurrent identical reopens and late old completion | Passed |
| Repeated legitimate no_need, captured protection, unchanged Intent | Passed |
| Migration preserves existing rows; Down refuses to drop an active fence | Passed |
| Built CLI through real HTTP/PostgreSQL initial and reopened capture | Passed |
| Skill frontmatter validation and `git diff --check` | Passed |

The database tests used a disposable loopback-only PostgreSQL 16 container.
They did not read or mutate production accounts. The CLI tests used generated
test credentials and isolated Homes. The model-level cases in
[e2e.md](e2e.md#prompt-acceptance) remain a separate acceptance step: these
checks do not establish a reduction in false `no_need` or a production fill rate.

## Reproduce on another host

Fetch and check out `codex/need-phase2` to get both stages, or `codex/need-phase1`
for the first stage alone. From the repository root, build a native CLI using
`cli/scripts/build.sh`, or build in the CLI module with output under `build/`.
Use the repository's isolated database test setup and migrate it through 000132.

```sh
# Repository root; PG_DSN must identify an isolated local migrated database.
go test ./pkg/need ./api/consolev2 ./migrations
PG_DSN='<local test DSN>' go test ./tests/needs -run 'TestCaptureReview|TestCapturePending|TestSelected|TestConcurrentReopen|TestCaptureReopen' -count=1
PG_DSN='<local test DSN>' EIGENFLUX_TEST_CLI='<absolute built binary>' go test ./tests/needs -run TestNeedInputCLI -count=1

# CLI module.
go test ./...
go test -race ./cmd ./internal/maintenance -run TestNeedCapture -count=1
```

For a deliberately selected real Agent/Intent, retain its exact Home and server
prefix, inspect `need capture review --intent-id ID --intent-version VERSION`,
then use `need capture reopen` with those same flags and
`--expected-capture-revision REVISION`. Read the returned current state; a replay
can already be completed. Let a subsequent full heartbeat process Pending work
normally, and verify that Feed/Agent output remains independent of Need outcomes.
