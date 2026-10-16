# Notification maintenance observations

## Notification setup and submission

CLI 1.0.10 adds `component=notifier`, `phase=notification` to the same authenticated
transport and event table. `source` is `installer`, `watch`, `manual`, or `test`;
`platform` is Windows or macOS. The nested `notification` object contains the
action (`setup`, `status`, `send`), `visibility=unknown`, optional hashed message
ID, compiled commit, fixed helper stage, and numeric HRESULT. No notification
text, URL, raw message ID, local path, or provider exception is uploaded.

Setup/status results are `ready`, `registered`, `disabled`, or `failed`; send
results are `submitted`, `disabled`, or `failed`. `registered` carries
`pending_first_notification`; it does not claim permission or visibility.
`submitted` confirms only the OS API accepted the message. The server rejects
`shown`, `seen`, unknown fields, and incompatible action/result pairs.

The Windows installer calls silent `notify setup --source installer`. Setup,
status, and explicit test commands attempt one bounded upload; `notify flush`
can retry manually. Watch uploads in its existing minute maintenance lane, and
heartbeats import pending local observations before their normal flush. Sources
remain distinct so explicit tests and ordinary watch startup are not counted as
installations or business messages. Telemetry failure cannot retry a submitted
message or turn successful installation into failure.

Each local observation has a stable event ID and an account binding captured at
creation. Only pre-authentication setup may bind to the first authenticated
account in the same Home/server. An unbound send is never reassigned; events
already bound to another account remain isolated. `queued` in `notify log`
means copied into the maintenance queue, not acknowledged by the server.
`notify flush` reports `pending_auth`, `queued`, or `reported`. Retention,
dropped events, backoff, authenticated-account guards, and server deduplication
remain those of the maintenance channel. Installation statistics are therefore
observed authenticated samples, not all anonymous downloads.

Deploy the API contract before using CLI 1.0.10 for collection. An older API
rejects the notification category; observations remain queued for retry. There
is no database migration. Read-only setup/message/error statistics are in
`scripts/reporting/notification_statistics.sql`. PostgreSQL acceptance verifies
all three results, account identity, duplicate suppression and queryability.

### Watch order notices

Watch order notices also emit `notification.action=watch` through this channel.
Stages distinguish durable queue admission (`queued`), service-name preparation
failure (`prepare_failed`), OS submission (`os_submitted`) and OS failure
(`os_failed`). OS submission does not prove a visible banner or human view.
The nested `notification.watch` carries the order ID/version, buyer/seller role,
origin and attempt number. Server order events use `source_notification_id`;
local execution blockers use `job_id`, `job_kind`, `job_status` and a bounded
`job_code`. Historic entries without trustworthy correlation use `legacy` with
version zero. No titles, bodies, raw errors, session IDs or paths are uploaded.

For duplicate investigations group by authenticated `agent_id`, then
`notification.id_hash`: one hash with distinct physical-attempt receipt IDs
(`attempt_id`) means repeated local attempts; the retry counter can repeat when
a crash prevents its desktop receipt from being saved. Upload retries preserve
the same receipt ID and do not count as another physical attempt;
different hashes with the same order and different job IDs mean distinct task
notices. Event IDs deduplicate upload retries independently. Do not treat a
queued record as OS submission, or a missing upload as proof of no local notice.
The read-only per-order query is `scripts/reporting/watch_notification_trace.sql`.
These events cannot change the device permission projection. The exact accepted
cross-field contract is in `api/consolev2/maintenance.md`; deploy its API support
before collecting the matching CLI records. No database migration is required.

## Device permission observations

Status observations may include an installation-generated device ID and an
account-private display name. The authenticated Console endpoint
`GET /api/v2/console/notification-devices` projects the latest observation per
device, with stale observations returned as unknown. Sends and watch receipts
do not establish permission. The API cannot identify the browser's current
machine or change device settings. See the endpoint contract for freshness,
validation, and response fields.

## Rollout and coverage

API-only deployment enables acceptance and projection on the existing telemetry
table; it requires no migration or RPC restart. It does not install CLI, Skills,
or frontend changes. Collection depends on matching client adoption and a
successful authenticated upload. Missing events cannot prove an action did not
occur. Local queues are bounded and may drop records; this is sampled operational
telemetry, not a complete audit trail. The SQL reports are read-only.
