# Need capture validation

Run `go test ./pkg/need` for both schema versions, strict payload parsing, field
limits, code validation and source preservation. Run all CLI module tests in
`cli/` to check byte-preserving request forwarding and retry headers.

Run `./tests/run.sh --skip-start needs` with loopback `PG_DSN` pointing to a database
migrated through 000110. Set `EIGENFLUX_TEST_CLI` to the native CLI binary built from
this checkout. Set `NEED_TEST_API_URL` to the isolated gateway to test actual routing;
otherwise the suite starts a real HTTP listener using the production handlers.

Coverage includes:

- V1 compatibility and v2 direct capture, get/list, private responses and scopes.
- No normalized projection created by a successful new capture.
- Mandatory requirements, preferences and source quotes round-trip independently.
- Body/text/ID/amount/currency limits and malformed or derived-field rejection.
- Owner isolation, current Intent status/version, concurrent retries and Intent edits.
- Byte-equivalent JSON retry hashing, changed field/array conflict, pagination.
- Historical input/snapshot retention and eligibility invalidation after edits.
- Legacy normalized inputs readable without a projection; inactive statuses stay inactive.
- Historical projection FK, revision uniqueness and account/Intent deletion cascades.
- The v2 contract example through real CLI create/retry/get/list.
- Pending capture discovery, existing-type reuse, atomic multi-input completion,
  concurrent identical retry, changed completion conflict, `no_need`, source
  revision/status changes and failed-write rollback.
- Real CLI pending/complete/retry against production HTTP handlers.
- Bounded automatic Need selection retains all available requested types.
- Asynchronous embedding discovery, same-text reuse, generation isolation,
  cache eviction repair, retry backoff, lease fencing and inactive-source exclusion.
  These cases also require the isolated Redis configuration.

Migration verification must cover 105→106, safe empty downgrade/reapply, preserved
historical rows, and refusal to downgrade when new captures exist. Broad e2e tests
that require external LLM/embedding services need configured providers; report
those failures separately from the dependency-free Need capture tests.


## Selected re-review

With migration 000132 applied to an isolated local database, run:

```sh
PG_DSN='<local test DSN>' go test ./pkg/need ./tests/needs -run 'TestCapture|TestSelectedNoNeed|TestConcurrentReopen|TestReopen' -count=1
```

The HTTP/store cases verify ownership, exact Intent/review revisions, retained
Intent content, bounded Pending enrollment, legitimate repeated `no_need`, late
old completions, concurrent reopen retries, failed capture rollback, unchanged
successful captures and initial-review hash compatibility. In the CLI module,
`go test ./cmd ./internal/maintenance -run 'TestNeedCapture|TestCapabilityRegistry'`
checks command wiring, revision propagation and local receipt selection.

## Prompt acceptance

Run the same current Skills on each supported host/model with the cases below.
Keep raw model outcomes and count false `no_need`, unsupported extra types and
technical failures separately. These are expected semantic decisions, not claims
that a string-matching unit test proves model behavior. Never submit a production
review merely to exercise these cases.

| Confirmed Intent | Expected decision | Boundary |
| --- | --- | --- |
| Track PostgreSQL indexing lessons and summarize useful updates. | broadcast | Ongoing interest is enough; analyze-only is valid. |
| Follow advances in robotics; no timeframe specified. | broadcast | Missing optional detail does not erase a stated interest. |
| Find Agents developing offline tools to exchange experience. | agent | Collaboration does not imply paid delivery. |
| Find someone to translate my manual; budget undecided. | commission | Do not invent budget or require it. |
| Follow database reports and meet database practitioners. | broadcast, agent | Preserve multiple supported types. |
| Translate the supplied paragraph myself using only this text. | no_need | No external discovery or delegation requested. |
| Remind me to send my existing report at 18:00. | no_need | A local reminder alone has no discovery target. |
| Hire a database consultant to optimize my queries; no known provider. | commission | Candidate availability is checked later. |
| Follow AI tooling; contacting anyone is forbidden. | broadcast | Action restrictions do not suppress information discovery. |
| Fetching the selected Intent fails or its revision changes. | leave pending | A technical failure is not a semantic no_need. |

Verify that true `no_need` cases include a source-grounded reason, positive cases
omit unknown optional fields, and neither result changes the user's action policy.
Measure historical re-review decisions separately from new Intent decisions;
a CLI/Skill upgrade alone must not reopen existing reviews.
