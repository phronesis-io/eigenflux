# Agent maintenance observations

Read before changing `maintenance_handlers.go`; update this file with the implementation.

`POST /api/v2/maintenance/events:batch` uses Agent V2 `settings:write` authentication. Actor identity always comes from the authenticated session. Browser telemetry remains a separate endpoint.

Accept 1–50 validated events, at most 128 KiB, from seven days ago through five minutes in the future. Reject unknown fields and invalid categories, version claims or durations. Reuse the existing per-Agent telemetry rate limiter.

Persist a batch atomically in `telemetry_events_v2` as `maintenance_attempt`, with 30-day retention. Prefix the hash of the authenticated Agent ID and client event ID with `maintenance:` before applying the existing unique key. Browser telemetry rejects the colon, so its caller-selected IDs cannot reserve maintenance keys. Retries deduplicate within one Agent; another Agent cannot reserve that key. Return 202 with the number of newly inserted rows; database failure returns 500.

These are client-reported maintenance observations, not proof of order fulfillment or delivery. The local watch keeps business execution independent of ordinary maintenance-upload failures.

No schema migration is required. Validate the HTTP contract and run `TestMaintenancePostgresAgentAuthIdentityAndIdempotency` against an isolated loopback PostgreSQL database; it rolls back temporary test tables. Production verification must not create test telemetry or orders.

## Commission execution diagnostics

Commission observations retain `component=dispatch`, `phase=commission`,
`trigger=auto`, `source=watch`, their existing stage/result rules and the stable
coarse `error_code`. Existing clients may omit all detailed fields. The optional
`commission.local_code` accepts only the fixed CLI journal-code allowlist in
`commission_diagnostics.go`; unrecognized producer codes become `unknown_failure`
before upload. The API rejects unrecognized wire values.

`commission.check_receipt` distinguishes `missing`, `present` and `not_applicable`.
Only `present` includes evidence: outcome `ready`, `needs_input`, `needs_user` or
`failed`; positive `checked_order_version`; explicit `inspected_file_count` from
0 through 128; and a 64-character lowercase hexadecimal `source_job_id`. The
source job may be an earlier intake rather than the current paid-fulfillment job.
An optional 64-character lowercase hexadecimal `input_fingerprint` is accepted
only for a `ready` receipt and represents retained readiness evidence, never a
fresh server check. A positive event order version must not be older than the
checked version. `missing` and `not_applicable` cannot carry receipt evidence.

`commission.bridge_error` requires a fixed safe bridge `code`. Its optional
`message` must equal that code; raw provider messages, prompts, stderr and paths
are forbidden. Optional operation is `read`, `write`, `execute` or `network`.
Optional process `exit_code` is an integer from -1 through 255. Optional JSON-RPC
`rpc_code` is one of -32700, -32600, -32601, -32602, -32603 or -32000. A bridge code
of `unknown_failure` cannot claim a message, operation or numeric details when
the original bridge error was not retained.

Stage `diagnostic_snapshot` uploads retained journal diagnosis as `result=observed`,
with zero attempt, duration and next-attempt time. It requires all three detailed
fields. Order version zero means the snapshot lacks a current version; an actual
retained check receipt may still state its own positive checked version. This
stage never represents a new Agent run, seller acceptance or business retry.
Snapshots must remain outside execution-stage aggregates. Historical missing
receipts and bridge details stay missing or `unknown_failure`; collection must
not execute old jobs to invent evidence.

Detailed fields are retained in the existing account-namespaced JSONB properties,
queryable by authenticated Agent, order, job and version. They are private
client-reported diagnosis and must not be added to anonymous metrics or labels.
The endpoint never accepts a client-supplied actor ID, modifies Order state or
introduces a new public diagnostics API. Unknown nested fields and private free
text reject the entire batch before persistence. The API and independent CLI
module share the positive/negative wire fixture in
`testdata/commission_diagnostics_wire.json`; tests also check allowlist parity,
lossless typed JSON round trips and real PostgreSQL persistence. Deploy API
collection before adopting the corresponding CLI.

## CLI update policy observations

Agent-authenticated PUT /agents/me/settings accepts an optional auto_cli_update
boolean as a client observation. Persist supplied true and false values as
cli_update_policy events in telemetry_events_v2 with server observation time,
authenticated agent_id, CLI version, host and mode. Missing/null remains unknown
and creates no observation. Retain these events for 30 days. This field never
writes agent_settings or changes the administrator policy. Authenticated GET
/agents/me/settings returns auto_cli_update from ENABLE_CLI_AUTO_UPDATE (default
true), independently of client observations. Restart API after changing this
environment setting. Agent and Console settings writes cannot change this policy.

Select the latest policy observation per active Agent in the reporting window;
retain an unknown cohort for Agents without an observation. New CLIs report the
effective received or cached policy, ignoring historical local overrides. Missing
server policy fields default to true. Deploy API collection before the CLI.

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
the latest retained status per device for that authenticated Agent, ordered
by observation time, not upload time. The existing 30-day telemetry retention applies. Return
at most 50 devices and never cache the response. A send receipt or old CLI
event without device metadata cannot establish current device permission.

Each device contains `device_id`, `device_name`, `platform`, `permission_state`,
`observed_at` and `stale`. Permission states are `ready`, `denied`,
`not_requested`, `unavailable`, or `unknown`. Windows first-use registration
is unknown. Preserve the latest reported permission regardless of observation
age; absence of a new report does not change permission. An observation more
than one minute ahead of server time returns `unknown` and `stale=true` until
it is within that clock-skew tolerance. Other observations return `stale=false`.
`observed_at` is the last observation, not proof of a live device.
The device name is account-private display text; never insert it into markup.
The API does not identify the browser's current machine or control remote OS
settings. Deploy API support before the matching CLI/web; no migration.

## Uninstall observations

Accept phase=uninstall with trigger=manual and results started, removed, partial, failed or blocked. Sources are agent/manual; platforms darwin/windows/linux. Require reason_category: not_provided, other, usability, cost, performance, privacy or no_longer_needed. Reject this metadata outside uninstall; reject free-text reasons, paths and client-selected account identity. Count distinct attempt_id across accounts; only removed counts toward the reason pie chart. No CLI, Skill or other business behavior changes are included in this backend contract.

Notification device projections include `cli_version` from the same device status
event’s `from_version`, regardless of observation age. Missing versions stay empty;
never substitute another device’s or the account’s version. No migration is needed.

When no valid device records exist, return `account_cli_version` from the signed-in
Agent’s `agent_settings.cli_version`. This is the last account observation, not a
device version or OS permission claim. Missing settings remain empty; query failures
return 503. When devices exist, leave the account fallback empty.

## Runtime and cycle observations

The same authenticated batch accepts `component=runtime`, phase `environment`
with result `observed`, or phase `cycle` with started/completed/failed. The bounded
`runtime` object contains product/version, OS, CPU architecture, reported model,
CLI/Skills revision and (for cycles) stage/plan mode. No device names, physical
hardware identifiers, raw errors or business payloads. Existing agent-derived
idempotency, rate limits, timestamps and 30-day retention apply. These events
never enter agent_activity_log and cannot inflate activity retention.

Set `ENABLE_RUNTIME_OBSERVATIONS=false` to acknowledge and discard only these
observations without affecting business operations or other maintenance events.
Deploy collection before CLI adoption; rollback can disable collection without
schema changes. A client completion receipt is an assertion, not a server-side
proof of every business effect. Analysis requires a matching start receipt.

A generated plan contributes environment evidence only. The first valid stage receipt records execution with its observation timestamp. Cycle failure requires a failed stage receipt. Interrupted or missing stages remain unresolved.

## Plan and execution lifecycle observations

Accept runtime `cycle` results `plan_created`, `incomplete`,
`permission_required` and `interrupted` only with `runtime.stage=cycle` and a
valid plan mode. Preserve `started` for actual execution evidence. Nonterminal
observations do not change business outcomes, establish retention, or prove
that a scheduler failed to trigger. Missing receipts remain unknown.

Accept runtime phase `lifecycle` with an empty plan mode and fixed stage/result
pairs: trigger/observed; permission/allowed, legacy_mismatch, prompt, forbidden,
unknown or required; guard/configured or active; execution/interrupted. Keep
runtime metadata token-only and reject error codes and unrelated metadata.
Configured guards do not prove host trust or activation; permission failures
require actual local evidence. Continue using authenticated Agent ownership,
maintenance event deduplication and the existing telemetry retention. Deploy API
acceptance before clients emitting the new categories.
