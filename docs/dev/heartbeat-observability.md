# Codex heartbeat execution observations

Run `scripts/reporting/codex_heartbeat_execution.sql` with the existing read-only
PostgreSQL reporting connection. It contains two SELECT-only queries and needs
no migration. Deploy API collection before the matching CLI. Historic clients
without these events remain instrumentation-unknown; this report does not
backfill their execution outcomes from environment observations.

The cohort uses the existing Growth eligibility filters and current reported
runtime attribution, prioritizing `runtime_name` and falling back to
`client_host`. It includes all Codex Agents registered on the latest seven
mature registration days: today minus eight through today minus two in
Asia/Shanghai. Each Agent is evaluated only on its complete registration D1.
A later runtime switch can change current attribution; it is not an installation
snapshot. The report does not select email addresses, prompts, paths or device
names.

The first query groups cycle observations by Agent, D1 date and `attempt_id`.
Cycle attempt IDs correlate plan creation and stage receipts. Lifecycle attempt
IDs identify host turns or explicit checks and are not interchangeable with
cycle IDs. Each row describes receipts observed during D1; cycles crossing
midnight may have receipts outside that day. Agents without a cycle still
appear with a null attempt and instrumentation-unknown status.

The second query reports distinct observed attempts per Agent and D1 date:
trigger, plan creation, execution start, completion, failure, incomplete checks,
permission requests, final host declines, interruptions, local policy checks and configured/active
guards. Counters can overlap: an incomplete cycle can later complete, and a
permission request can later be approved. Use counts rather than summing these
columns into a funnel population. Completed evidence takes precedence in the
summary label; cycle rows retain the diagnostic flags.

Interpret labels by their evidence boundary:

- `planned_without_execution_evidence`: a plan was delivered, with no execution
  start or stage receipt in the observed D1 window.
- `permission_request_observed`: the host requested approval; no final rejection
  is established. `policy_forbidden` is a local static Rules-check result, not a
  runtime rejection. `policy_legacy_mismatch` is a command-prefix mismatch.
- `permission_denial_observed`: the host recorded a trusted final decline of
  the scoped CLI command in that turn. This is distinct from a request or static
  policy match. A later authorized retry may resolve the barrier; counts retain
  the earlier observation rather than claiming the current policy still denies it.
- `trigger_observed_without_plan_evidence`: a scoped canonical heartbeat prompt
  was observed, without a delivered plan in the window. A prompt observation
  alone does not prove that it originated from a persistent scheduler.
- `no_trigger_observed`: no trigger, plan or execution evidence was collected,
  while an active guard invocation was observed that day. It means unobserved,
  not that the scheduler definitely failed to trigger. Guard activity does not
  prove uninterrupted event coverage throughout D1.
- `instrumentation_unknown`: the report lacks sufficient runtime collection
  evidence. Configured hooks alone do not establish host trust or activation.

Event scans are bounded by authenticated Agent, event type and D1 `event_at`,
using the existing Agent/type/event-time index. A separate `created_at` range
bounds ingestion; the five-minute lower margin preserves the API's permitted
future clock skew. Registration scans use numeric `created_at` boundaries.
Keep event filters inside the observations CTE so cohort rows without events
survive the left joins. Validate query plans with read-only EXPLAIN before
expanding the cohort or time window.

## Upgrade repair delivery

CLI 1.0.26 adds a durable local acceptance request for existing Codex skill
accounts. A separate watch worker covers notifications-only installations and
submits verified signed repair instructions to the existing bound owner without
waiting for a heartbeat. It preserves the current scheduler and security
controls. Missing host binding, local Skills conflicts and uncertain submission
remain visible through the scoped request and a deduplicated desktop fallback.
Network failures retain automatic retries. The request contains local blocker
categories, not new server telemetry or a changed retention definition.

Treat `pending`, `submitted`, `user_action`, `blocked`, `deferred` and
`awaiting_run` as incomplete acceptance. Only an actual canonical scheduled full
cycle observed by the scoped Stop hook establishes local `verified` status.
Inspect the existing trigger, guard, permission and cycle observations for
production adoption; a published binary or an acknowledged repair prompt does
not prove client repair. Windows owner transport still requires real-device
acceptance independently of cross-platform builds and fixtures.
