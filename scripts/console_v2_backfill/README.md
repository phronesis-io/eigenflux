# Console V2 legacy backfill

The backfill omits the Chinese prefix for biographies without Han characters.
It preserves the original biography body, literal escapes, line endings and
240-character limit. Chinese and mixed-Han biographies retain the original
template. New empty biographies use an English fallback. All commands are
read-only unless `--apply` is supplied.

Build with `go build -o build/console-v2-backfill ./scripts/console_v2_backfill`.
Provide `PG_DSN` through the deployment environment without logging credentials.
The original account backfill uses the persisted `console_v2_legacy_agents_v2`
cursor; do not reset that cursor to repair existing goals.

## Historical goal repair

`build/console-v2-backfill --repair-goals` previews eligible records.
Add `--agent-id=<id>` to restrict the scan to one Agent. Add `--apply` to repair.
`--batch-size` controls candidate pagination; repair commits one Agent at a time,
with a 15-second transaction deadline, 10-second statement timeout and 2-second
lock timeout. A failed run can be resumed by running the same command again.

Eligibility requires an original `legacy-backfill-v2` system draft, a completed
onboarding, and an active goal that exactly equals both the archived draft goal
and the old template reconstructed from the archived biography. This includes
unchanged templates saved as `human_edit`, but excludes actual edits,
`agent_prefill` replacements and coincidentally similar text without migration
provenance. The current profile biography is never used to restore old content.
Historical empty biographies and biographies containing Han characters are
excluded. This conservative content check does not claim to identify the user's
locale. Repair only strips the injected Chinese prefix; the remaining goal
text is byte-for-byte unchanged.

The repair locks the context head and rechecks the goal to protect concurrent
edits. It retires the previous goal, inserts a system-derived replacement with
an incremented goal version, and appends an immutable context revision. The new
context preserves all unrelated fields and its schema version. Both active
context pointers advance together. Mismatched goal/context/onboarding state
aborts that Agent's transaction. Prior goals, context snapshots, onboarding
drafts and idempotency responses remain available as historical evidence.

The goal page reads the active control context. Today and Agent Card projections
read the active goal. No Redis cache invalidation is required. Existing browser
pages must reload to retrieve the new revision; old onboarding draft/history
endpoints intentionally continue to expose their historical content.

Deploy only the merged main commit. Preview first, repair a known affected Agent,
verify its active goal/context/onboarding pointers, then apply the full scan and
confirm a subsequent preview reports zero eligible records. No service restart
or schema migration is needed. The operations Console API has no administrative
network-goal repair endpoint; this bounded script maintains the same canonical
tables and immutable revision contract as the goal editor.

## Validation

`go test ./scripts/console_v2_backfill` covers multilingual text, unchanged
escapes/paths/line endings and the original truncation boundary.
Set `EIGENFLUX_GOAL_REPAIR_TEST_DSN` to an isolated loopback PostgreSQL URL to run
the transaction tests as well. These create a temporary schema using the actual
foundation migration definitions and remove it on completion. They verify
read-only preview, exact provenance, preservation of edits and unrelated
context, immutable history, idempotence, stale candidates and rollback.
