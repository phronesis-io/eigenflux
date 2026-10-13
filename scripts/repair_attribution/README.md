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
