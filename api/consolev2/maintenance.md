# Agent maintenance observations

Read before changing `maintenance_handlers.go`; update this file with the implementation.

`POST /api/v2/maintenance/events:batch` uses Agent V2 `settings:write` authentication. Actor identity always comes from the authenticated session. Browser telemetry remains a separate endpoint.

Accept 1–50 validated events, at most 128 KiB, from seven days ago through five minutes in the future. Reject unknown fields and invalid categories, version claims or durations. Reuse the existing per-Agent telemetry rate limiter.

Persist a batch atomically in `telemetry_events_v2` as `maintenance_attempt`, with 30-day retention. Prefix the hash of the authenticated Agent ID and client event ID with `maintenance:` before applying the existing unique key. Browser telemetry rejects the colon, so its caller-selected IDs cannot reserve maintenance keys. Retries deduplicate within one Agent; another Agent cannot reserve that key. Return 202 with the number of newly inserted rows; database failure returns 500.

These are client-reported maintenance observations, not proof of order fulfillment or delivery. The local watch keeps business execution independent of ordinary maintenance-upload failures.

No schema migration is required. Validate the HTTP contract and run `TestMaintenancePostgresAgentAuthIdentityAndIdempotency` against an isolated loopback PostgreSQL database; it rolls back temporary test tables. Production verification must not create test telemetry or orders.

## Notification observations

Accept `component=notifier`, `phase=notification`, trigger `manual`/`auto`, source
`installer`/`watch`/`manual`/`test`, and platform `windows`/`darwin`. The nested
notification object requires action `setup`/`status`/`send` and visibility
`unknown`. Setup/status accept ready, registered, disabled or failed; sends
accept submitted, disabled or failed and require a 64-character SHA-256 ID hash.
Validate matching fixed error codes, helper stage/HRESULT pairs, compiled commit,
and a duration no greater than 60 seconds. Reject raw content, URLs, client
identity, unknown nested fields, and claims of banner visibility or human views.

Reuse the existing authenticated identity-derived event keys and table; no
migration or new anonymous endpoint. Collection must deploy this API contract
before the new CLI. Queries distinguish installer setup from watch/test/manual
activity. PostgreSQL tests verify notification persistence and idempotency.

### Watch order notification correlation

`notification.action=watch` requires `trigger=auto`, `source=watch`,
`visibility=unknown` and a 64-character lowercase SHA-256 `id_hash` identifying
the logical local notice. `stage` is `queued`, `prepare_failed`, `os_submitted`
or `os_failed`; corresponding results are `queued`, `failed`, `submitted` and
`failed`. Only `prepare_failed` accepts `service_name_unavailable`; `os_failed`
accepts the fixed notification error codes. Queued/submitted errors are empty.
Device fields and HRESULT are forbidden. These observations do not establish
current notification permission or human visibility.

The nested `notification.watch` object contains `origin`, canonical positive
int64 string `order_id`, `order_version`, `role` (`buyer`/`seller`) and `attempt`.
New records require a positive order version. Origin `order_event` requires a
canonical positive int64 string `source_notification_id` and forbids job fields.
Origin `local_blocker` requires a 64-character lowercase hex `job_id`,
`job_kind` (`commission_order`/`commission_fulfillment`), `job_status`
(`unknown`/`failed`/`needs_user`) and a fixed `job_code`; it forbids a source
notification ID. Unknown local codes map to `execution_blocked` before upload.
Origin `legacy` requires order version zero and forbids source/job fields;
missing historic correlation must not be reconstructed. Queued attempt is zero;
other stages require a positive attempt number. Setup/status/send forbid watch
metadata. Reject raw titles, bodies, errors, session IDs, paths and account IDs.

Correlate by authenticated Agent and `id_hash`. Distinct local notices for the
same order have different hashes; repeated attempts of one notice retain its
hash and normally increase `attempt`. Each real preparation/OS call has a
separate receipt ID; a crash before the desktop receipt is saved may repeat
the retry counter without merging the two physical submission records.
A submitted observation confirms OS submission only. Missing records cannot
prove no notification occurred. Reuse maintenance
retention, identity isolation and event-ID deduplication; no migration.

## Notification device status

Status observations may include `notification.device_id` (32 lowercase random
hex characters, stable for the local Home) and `device_name` (1–64 Unicode
characters, no control/format characters or surrounding whitespace). Both must
be present together, only on `action=status`. Existing events remain valid.
The identity is installation generated, never a hardware identifier.

`GET /api/v2/console/notification-devices` uses the active Console cookie
session and returns `devices` plus `checked_at` in Unix milliseconds. Select
the latest status per device for that authenticated Agent from seven days of
maintenance observations, ordered by observation time, not upload time. Return
at most 50 devices and never cache the response. A send receipt or old CLI
event without device metadata cannot establish current device permission.

Each device contains `device_id`, `device_name`, `platform`, `permission_state`,
`observed_at` and `stale`. Permission states are `ready`, `denied`,
`not_requested`, `unavailable`, or `unknown`. Windows first-use registration
is unknown. After ten minutes without an observation, or with more than one
minute of future clock skew, return stale/unknown instead of a denied claim.
The device name is account-private display text; never insert it into markup.
The API does not identify the browser's current machine or control remote OS
settings. Deploy API support before the matching CLI/web; no migration.

## Uninstall observations

Accept phase=uninstall with trigger=manual and results started, removed, partial, failed or blocked. Sources are agent/manual; platforms darwin/windows/linux. Require reason_category: not_provided, other, usability, cost, performance, privacy or no_longer_needed. Reject this metadata outside uninstall; reject free-text reasons, paths and client-selected account identity. Count distinct attempt_id across accounts; only removed counts toward the reason pie chart. No CLI, Skill or other business behavior changes are included in this backend contract.
