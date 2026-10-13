# Bounded historical attribution repair

`go run ./scripts/repair_attribution -plan /private/approved-plan.json` validates
one reviewed plan without writing. `-apply` requires the row to remain completed
and expired, with matching item/author/source URL/group/expiry and exact old text.
It updates raw content and both summaries in one row-locked transaction. Identity,
classification, grouping, expiry and delivery counters are not updated.

The plan contains `item_id`, `author_id`, `group_id` (decimal strings), `url`,
`expire_time`, and `before`/`after` objects with `content`, `summary`, `summary_zh`.
Keep this source material private and preserve the plan as the repair record.

After the SQL transaction, the command queues the existing completed-item
projection repair. That branch regenerates the embedding and search projection
without re-extraction, deduplication or publishing a new broadcast. If interrupted
between SQL and queueing, rerun the same plan: already-corrected text is accepted
and projection repair is safe to repeat. Verify SQL, source-storage text and the
public detail response after the queue is consumed. Run only the reviewed,
main-merged tool with the intended environment; local tests use isolated services.

The apply path is covered against real PostgreSQL, including separate locked
reads of both tables and idempotent repetition. Run it with
`FIDELITY_REPAIR_TEST_DSN=<isolated-test-dsn> go test -race ./scripts/repair_attribution`.
It uses transaction-local temporary tables and rolls the fixture back.


`-pgc-summary-only` permits a reviewed summary correction on a completed PGC
item that has no expiry or has not yet expired. The old and new source content
must be byte-identical; the raw row is locked for validation but is not updated.
Publisher email must match configured authoritative PGC suffixes, and all
identity, group, expiry and exact-text preconditions still apply. UGC and raw-body
changes are rejected in this mode. The default expired-body repair is unchanged.

A live summary correction can change search text. Review the exact before/after
plan and include its effect/coverage in the fixed-cohort delivery replay before
applying it. This switch is not a bulk backfill or permission to refresh dates,
quality, classification, groups, counters, or lifecycle. Keep reviewed source
payloads and model-audit outputs outside Git.
