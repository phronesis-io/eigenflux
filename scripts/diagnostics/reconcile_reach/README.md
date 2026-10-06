# Historical Reach delivery floor

Dashboard Reach currently reads cumulative `item_stats.consumed_count`: delivery
occurrences, not distinct recipient Agents. Discovery now emits consumption for
new deliveries; it does not retroactively repair the earlier missing events.

This bounded command previews or raises that counter to the number of proven
historical broadcast deliveries still retained in `replay_logs`. It requires
explicit item IDs (at most 100), an exclusive historical cutoff at least ten
minutes in the past, and an explicit `PG_DSN`. Default execution is read-only.

```sh
go run ./scripts/diagnostics/reconcile_reach \
  --items 364695844269588480 --before-ms 1791097200000
# Apply only after reviewing the preview and obtaining production write approval.
go run ./scripts/diagnostics/reconcile_reach \
  --items 364695844269588480 --before-ms 1791097200000 --apply
```

The example cutoff is October 4, 2026, 15:00 Asia/Shanghai, before the delivery
producer fix. Preview emits the current counter, distinct historical deliveries,
distinct historical recipients and the proposed counter. Only rows with
`source_kind=broadcast`, `delivered=true` and a nonempty impression ID qualify.
Repeated `(impression_id, agent_id, item_id)` rows count once; another impression
for the same recipient counts as another delivery. Null/false delivered flags,
other kinds and rows at/after the cutoff do not count.

Apply locks existing statistics rows and uses `GREATEST`, preserving larger
counters and concurrent live increments. All requested items must have statistics
rows; an error rolls back the complete invocation. Lock/statement/command timeouts
bound execution, including the initial database connection; the command uses
one pooled connection. It changes only consumption and its update timestamp, without
creating feedback, milestones, streams or replay rows. Normal statistics cache
refresh/expiry applies; this command does not flush shared caches.

This is a manual diagnostic, not a scheduled sweep or request-path query. The
investigated production case's read-only `EXPLAIN` uses the existing
`idx_replay_logs_item(item_id, served_at)` index. That does not bound the cost of
every popular broadcast. A batch holds earlier row locks until transaction
completion and can delay live increments; start with the investigated item and
review its preview before applying a larger batch.

This is a conservative lower bound, not exact historical reconciliation. Existing
legacy counts may overlap retained delivery evidence, so adding the evidence to
the existing count would double-count. Purged replay history cannot be recovered.
The tool does not make the live stream consumer exactly-once or establish a
unique-Agent metric. A durable impression ledger and explicit metric migration
are required for those stronger contracts.

Native PostgreSQL tests use an isolated loopback database with per-test schemas:

```sh
REACH_TEST_DSN='postgres://user:password@127.0.0.1:5432/test?sslmode=disable' \
  go test -race -count=1 ./scripts/diagnostics/reconcile_reach
```
