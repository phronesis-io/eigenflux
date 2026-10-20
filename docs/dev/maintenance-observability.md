# Maintenance observations

The CLI records observations independently of business success. `executed` means
that the replacement CLI started with its actual compiled version; it does not
mean a watch is ready, a model turn completed, or a business action succeeded.
Each watch emits its own `runtime_ready` adoption observation after the supervisor
has verified that account's process and connections. An installation shared by
several Homes has one installation and separate account adoption attempts.

## Transport and identity

`POST /api/v2/maintenance/events:batch` accepts Agent V2 bearer authentication
with `settings:write`. It does not accept a Console session or a caller-supplied
Agent ID. The existing Agent middleware also enforces identity, revocation,
expiry, onboarding and scope restrictions. The body contains 1–50 `events` and
is limited to 128 KiB. Unknown fields are rejected at every JSON object level.
The endpoint reuses `telemetry_events_v2`; its event type is
`maintenance_attempt`. The global event key hashes the authenticated Agent ID
and client event ID, preventing one account from consuming another's key.
Identical retries return 202 and insert no additional rows.

Each event contains `event_id`, `event_at` (Unix milliseconds), `attempt_id`,
`component`, `trigger`, `phase` and `result`. Optional bounded fields are `host`,
`mode`, `from_version`, `to_version`, `running_version`, `error_code` and
`duration_ms`. Integration mode is `skill`, `plugin`, or empty when unknown.
No raw errors, paths, credentials, message contents or model context are sent.
The server rejects unknown categories and impossible component/result pairs.
Runtime and loaded observations require matching target and running versions.

Components are `cli`, `skills`, `plugin` and `scheduler`. Triggers are `auto`,
`manual` and `adoption`; adoption is not another installation. Phases are
`check`, `download`, `verify`, `probe`, `install`, `execute`, `adoption`,
`load`, `read` and `migration`.
Results are `started`, `no_update`, `installed`, `executed`, `runtime_ready`,
`loaded`, `rules_read`, `verified`, `failed`, `rolled_back`, `restart_required`,
`blocked`, `waiting_online` and `not_installed`. Do not interpret `installed`
as active-process adoption. Missing final observations remain unknown.

## Local state and handoff

`cli/internal/maintenance` stores a queue under the existing Home maintenance
directory, partitioned by server and Agent ID. An OS file lock and atomic
replacement serialize updates. The queue retains at most 256 events for seven
days, tracks dropped events, and retries one batch with bounded exponential
backoff. HTTP batch requests time out after five seconds; credential refresh
uses the existing bounded V2 refresh path. Flush outside watch's connection and
lease loop. Concurrent uploads are safe; only acknowledged IDs are removed.
The server accepts the same seven-day event window and five-minute clock skew.

CLI update details are persisted before re-execution; a non-secret attempt ID
is passed to the replacement process. The process may confirm execution only
when its actual version and the account's stored installation attempt agree.
The `EIGENFLUX_UPDATE_REEXEC` marker applies only to that short command. A supervised watch starts with a clean environment; standalone watch adoption
clears re-execution markers and pins the account identity before replacement.

`heartbeat plan` emits `skills_read_receipt` only for compatible, signed,
unchanged local rules. After reading every selected source, the host submits
that exact receipt to `heartbeat maintenance-report --stdin`. The CLI rejects
a receipt from another account, an expired attempt, or a changed rules revision.
It retains at most 32 recent Skills plan attempts so a concurrent same-revision
heartbeat does not invalidate an Agent that is still reading. It timestamps the actual
read confirmation when received. Disk installation alone
does not produce `rules_read`. `heartbeat plugin-check --stdin` records fresh
plugin observations and retains the attempt across `restart_required` and
`loaded`. A cached receipt is never current process-load evidence. OpenClaw native workers
record `loaded` only after live Gateway and watch readiness verification; their
`not_due` plan fields are cached discovery timing, not fresh process evidence.
Native maintenance does not emit `rules_read` on behalf of an absent Agent turn.

`heartbeat maintenance-status` reports the current account's queue, dropped
count, retry time and latest component observations. A queue or telemetry
failure does not trigger an installation retry or business replay.

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

## Queries

These queries are read-only against the existing event table. Filter to a
specific target and host when evaluating a release. Event collection is an
observed sample: offline machines and dropped events prevent a complete
population success rate. CLI/Skills version coverage remains the separate
runtime-settings metric. The latest phase uses created time only as a tie
breaker; a retried old event cannot supersede a later observation.

```sql
WITH latest AS (
  SELECT DISTINCT ON (agent_id, properties->>'attempt_id', properties->>'component')
    agent_id, properties, event_at
  FROM telemetry_events_v2
  WHERE event_type = 'maintenance_attempt'
    AND event_at >= (extract(epoch FROM now() - interval '7 days') * 1000)::bigint
  ORDER BY agent_id, properties->>'attempt_id', properties->>'component',
    event_at DESC,
    CASE properties->>'result'
      WHEN 'loaded' THEN 4 WHEN 'runtime_ready' THEN 4
      WHEN 'rules_read' THEN 4 WHEN 'verified' THEN 4 WHEN 'executed' THEN 4
      WHEN 'started' THEN 0 WHEN 'installed' THEN 1 ELSE 2 END DESC,
    created_at DESC, event_id DESC
)
SELECT properties->>'component' AS component,
       properties->>'trigger' AS trigger,
       properties->>'host' AS host,
       properties->>'to_version' AS target,
       CASE WHEN properties->>'result' = 'started' THEN 'unknown'
            ELSE properties->>'result' END AS result,
       count(*) AS observed_attempts
FROM latest
GROUP BY 1,2,3,4,5
ORDER BY 1,2,3,4,5;
```

```sql
SELECT properties->>'component' AS component,
       properties->>'phase' AS phase,
       properties->>'error_code' AS error_code,
       count(DISTINCT (agent_id, properties->>'attempt_id')) AS attempts
FROM telemetry_events_v2
WHERE event_type = 'maintenance_attempt'
  AND properties->>'result' IN ('failed', 'blocked', 'rolled_back')
  AND properties->>'trigger' = 'auto'
GROUP BY 1,2,3;
```

Separate `trigger=adoption` from installation attempts. For CLI, `executed`
confirms the short replacement command; a watch is operational only after its
own `runtime_ready`. For Skills use `rules_read`, plugins use `loaded`, and
scheduler migration uses `verified`. Keep `restart_required`, `waiting_online`,
`blocked` and unknown visible rather than counting them as successful updates.

## Verification

`go test ./internal/maintenance ./cmd` in `cli/` covers bounded retention,
identity partitioning, retries, concurrent recording, selected plan modes,
signed local rules and replacement execution. Root-module unit checks use
`go test ./api/consolev2 -run TestMaintenance`. With a loopback-only `PG_DSN`,
`TestMaintenancePostgresAgentAuthIdentityAndIdempotency` creates transaction-local
temporary tables and exercises real Agent authentication, scope denial,
identity-derived writes, duplicate delivery and SQL queryable results. It never
requires production data or RPC services.
