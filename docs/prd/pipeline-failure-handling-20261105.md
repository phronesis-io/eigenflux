# Pipeline failure handling repair

Safety-provider request failures must remain undelivered and become failed processing with a durable dead-letter record, rather than content-policy discards. Actual unsafe verdicts and every distribution filter remain unchanged. Existing bounded safety retries remain; no alternate provider bypass or historical requeue is introduced.

Stop and join the delivery heartbeat before owner-fenced ACK/DLQ finalization, retaining the local active-delivery fence until finalization finishes. Ownership loss still cancels processing, and only the Redis owner can acknowledge.

Acceptance: failure persistence and storage-error regression tests, native Redis ownership/finalization tests, pipeline build and CI, then the managed main deployment with health, queue progress and new logs inspected. No schema or API changes. Pascal explicitly waived offline replay for these repairs. Rollback uses a reviewed revert and managed deployment. Check next-day processing failures, DLQ and fixed-cohort Agent positive feedback on 2026-11-06; no ranking change is intended.
