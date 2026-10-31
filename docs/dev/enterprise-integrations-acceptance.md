# Enterprise integration acceptance evidence

This document records the implementation boundary and local validation for the
five-PR enterprise SDK delivery. It is not a production activation record.

| Requirement | Implementation | Verification |
| --- | --- | --- |
| Stable anonymous identity, app ownership, Agent/product/SDK/install/runtime metadata and interval | `pkg/integrationidentity`, integration registration API, migration 125 | PostgreSQL slot races, stable retry binding, strict HTTP ownership; compiled SDK registers and reports updated runtime version without personal principals/onboarding |
| Summary/structured context, Card and private matching | `pkg/integrationprofile`, context/Card routes, migration 126, Sort/Discovery adapters | PostgreSQL stale-result/concurrent-edit fences and rebuilt Card privacy; actual ranking queries and returned items change between private Python/Go context revisions |
| Hourly server admission, shared quota and idempotency | `pkg/integrationfeed`, Feed routes, migration 127, additive generated RPC fields | 16-way admission produces one generator owner; persisted retries, UTC app quota, changed minimum, old sequences and unknown completion are fenced; immediate second round returns 429 |
| SDK outer loop and customer consumption | Private compiled Go runtime, thin Python adapter, binary wheel, runnable B reference Agent | Go race tests; actual compiled-runtime Python tests; installed-wheel tests; native backend HTTP -> context worker -> ranking/Feed -> SQLite consumer acceptance |
| Global/app/Agent/installation controls and bounded local permit | Shared server control checks, operator mutations, independent SDK control loop | Disabled stored payload/Card/context reads fail; stale runtime binding fails; blocked callback is cancelled by a control check while the permit still has 90 seconds; permit expiry has a separate test; SDK restarts disabled and resumes after operator re-enable |
| Operational visibility | `scripts/integration_admin`, `operations.go`, migration 128 and enterprise-only request observer | Real PostgreSQL dry-run/credential rotation/policy tests; inspection verifies versions, installations, effective cadence, request observations, exact admitted batches and fixed failure codes |

The final native acceptance run used an installed macOS arm64 `0.1.0` wheel and
its bundled runtime, an isolated fully migrated PostgreSQL database, a native
Hertz listener, and real context/Card/ranking/Feed implementation. It completed
two customer inbox batches and exactly two source generations. The first retry
returned the persisted batch; an early new sequence was refused. Disabled reads
were denied, the restarted SDK waited while disabled and resumed after enable,
and the superseded runtime instance lost access. One-hour fixture timestamps
were advanced explicitly; the actual 30-second registration retry was retained.

Fixed local model/search responses and a DB-backed Item RPC adapter replace
external dependencies. This validates the application-to-SDK/backend chain, not
an external vendor model, remote Elasticsearch cluster, actual Jarvis session,
or real customer deployment. The functional reference Agent durably consumes
Feed into its own inbox/notes and can be replaced with a customer's own Agent
queue. No production database or authorization list was accessed.

Additional checks passed locally:

- Actual migrations through 128 and existing test-account reset coverage.
- Race-tested identity/context/Feed/API/operator packages against isolated PostgreSQL.
- Feed, legacy Sort, Discovery, pipeline, migration and personal CLI Feed contract regressions.
- API/pipeline/Feed/Sort/operator builds and targeted `go vet`.
- Private SDK race tests and Python tests: busy, empty, accepted, unknown, cancellation, restart, lost response and opaque policy revision compatibility.
- Linux/macOS amd64/arm64 wheel builds, member/hash/source-exclusion checks and third-party notices.
- Actual wheel install into an isolated virtualenv and repeated compiled-runtime acceptance.

Request observations are best effort and exclude unmapped requests. Admission
counts are transactional. An unknown generator can conservatively skip a round;
an unknown consumer outcome remains held until authoritative resolution. The
SDK cannot forcibly undo customer side effects. Current personal CLI Feed
authorization/cadence remains unchanged. TypeScript, Webhook, Windows native
acceptance and client auto-update are deferred, not claimed as delivered.

See [contracts, operations and activation](enterprise-integrations.md) and
[the runnable customer example](../../examples/enterprise_sdk/README.md).
