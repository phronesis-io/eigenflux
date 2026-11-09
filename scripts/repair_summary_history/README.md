# Targeted historical summary suppression

This operator tool is restricted to the four reviewed incidents in PRD section 11,
approved on 2026-11-09. It does not regenerate summaries, substitute historical
author notes, or infer authorship for other records.

- 376715300894146560: suppress the reversed conclusion and its translations.
- 377856703162482688: suppress the unqualified causal attribution and translations.
- 377166422679224320: suppress the denominator error and translations; do not
  silently repair the ambiguous author summary.
- 375607914615799808: suppress only the Chinese summary translation expanding FTC.

The first three select a labeled original-body excerpt. Excerpt truncation can
omit qualifications; keep the full-body entry point. The fourth retains English.
Evidence is a read-only source/stored-text comparison, not an estimate of a
platform-wide error rate or confirmation of another author's 50-item audit.

## Prepare and apply

Apply migration 000133 and deploy upgraded readers/translation writers first.
Drain in-flight old writers before activation. Production execution must use
reviewed, main-merged code under the deployment policy. Keep credentials on-host.
Build to `build/repair-summary-history`; keep the JSON plan outside version control.

`--prepare --plan <new-private-file>` captures the exact source fingerprint and
old display fields under a read-only transaction. Inspect the four-item plan.
`--plan <file>` verifies preconditions without writes. `--plan <file> --apply`
atomically records the before images, activates suppression, and creates durable
completed-item repair jobs. Unexpected source, lifecycle, active repair, or
text/cache changes stop the transaction. Existing publication outbox rows must
drain before first activation. The tool never changes raw content/notes,
publication identity, safety or delivery eligibility.

The database trigger clears suppressed legacy and literal translation columns
on every write. The highlight handler and cron skip suppressed fields. Delayed
translation writes rejected by source/policy checks are not returned as valid
translations. Stale source fingerprints must remain rejected by Discovery until
its projections agree; do not bypass that guard to recover delivery counts.

## Verify, retry and reverse

SQL commit is not completion. Consume the durable repair jobs, then verify all
four records through API/detail, Chinese/English website cards, Feed and owned
search; compare each ES document with authoritative source/selected summary and
check forward-feature content hashes. The completed-item worker repairs ES and
forward features without summary generation. Retry the same plan after a
projection failure; never generate a replacement summary. Existing snapshots
and impression IDs remain audit records, not fresh publications.

Do not mark `projection_verified_at` until this cross-surface verification is
recorded. Invalidate any additional discovered text-bearing caches before calling
activation complete; Feed's ID-only queue is not itself a text cache. Include the
changed historical projection in the fixed-cohort release replay.

`--plan <file> --revert` verifies reversal; adding `--apply` restores only the
recorded fields, clears suppression, records reversal and queues projection
repair. It refuses concurrent source edits or unexpected active values. Reversal
can re-expose known-bad old text, so it is an explicit operator action, never an
automatic rollback fallback. Retain audit/schema after use; normal hard deletion of the source record
cascades its text audit, preserving the existing source-deletion contract. A repeat operation
cannot silently reactivate a reversed repair.
