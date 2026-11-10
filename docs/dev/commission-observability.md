# Commission observability contract

Reuse `/api/v2/maintenance/events:batch`, the account-scoped CLI queue and
`telemetry_events_v2`; do not create a second telemetry pipeline. Agent identity
comes from authentication. Event IDs are Agent-namespaced and replay inserts
nothing. Accept event times from the last seven days; retain rows for 30 days.
Existing installer, scheduler and notification events remain compatible. The local
queue retains at most 1,024 events for seven days; overflow or expiry increments
its dropped counter. The queue is diagnostic evidence, not a financial ledger.
Buyer material reminders use notification origin `input_request`, retaining the
clarification job, order version, request hash and round; prior origins reject
these extra fields. Main-conversation evidence may retain a request hash without
a round when the host queue does not carry that round.

## Client execution evidence

`component=dispatch`, `phase=commission`, `trigger=auto`, `source=watch` use the
strict `commission` object in `api/consolev2/commission_maintenance.go` and its
CLI counterpart `cli/internal/maintenance/commission.go`. Keep both validators
synchronized. Fixed stages cover job admission, first order read, Agent work,
artifact verification, upload grant/PUT/confirmation, delivery, verification,
main-conversation reminders and retries. Use `event_at`, `duration_ms`, attempt,
order/version, job and source-notification IDs to correlate evidence. Version
zero is allowed for preflight before an order has been read, or for a retained
diagnostic snapshot without a captured version. Each physical
stage observation is assigned one receipt; its start/result share an attempt ID
and replayed uploads reuse event IDs. A queued job has a stable receipt across
Socket/HTTP duplicates;
a later stage or retry must not overwrite an earlier event.

Use fixed error, adapter, operation and resource categories. Session, run,
artifact and input-request identifiers are SHA-256 hashes. Optional `transport`
is limited to `socket`, `http_poll` or `local`. Upload HTTP status
may be included. Never upload prompts, answers, order inputs, absolute paths,
file names, upload URLs, headers or credentials. A successful Agent stage does
not establish accepted delivery, buyer confirmation or settlement. The business
order/event/payment tables remain authoritative.

### Detailed failures and retained evidence

Keep the coarse `error_code` for existing consumers and attach the fixed local
job code as `commission.local_code`. Unknown local codes remain
`unknown_failure`; never use arbitrary exception text as a code.

`check_receipt` reports `present`, `missing` or `not_applicable`. A present
receipt retains its actual outcome, checked Order version, inspected-file count
and source job hash. Include the input fingerprint only when the journal holds
that verified fingerprint. Missing evidence is not reconstructed from a paid
state, an acceptance event, a notification ACK or loose local files.

`bridge_error` retains a bounded fixed code, an optional allowlisted message,
permission operation and process exit code when captured. Keep runner and
bridge categories separate from business failures. Exclude raw stderr, RPC
messages, command arguments, URLs, prompts, model output, credentials and local
paths. An older job without retained bridge details reports unknown evidence.

On startup, report retained failed, blocked or unconfirmed Commission jobs using
`stage=diagnostic_snapshot`, `result=observed`, zero attempt/duration and no
scheduled retry, deduplicated by retained evidence. This is a new
observation of existing local state, not another execution attempt. It must not
start an Agent, synthesize a readiness receipt, retry a mutation, clear a job or
change an Order. Snapshot time is observation time, not the historical failure
time. Preserve the account, binding and Order correlation of the retained job.

## Queries and dashboards

`scripts/reporting/commission_dispatch_trace.sql` is an account/order-scoped
operator query. Grafana reads only anonymous security-barrier aggregate views
from migration `000123_commission_observability_views.sql`:

- `grafana_commission_dispatch_daily`: trailing 30-day stage/result counts and
  optional durations, grouped by Shanghai event day, job kind and adapter.
- `grafana_commission_notification_delivery`: current unexpired pending receipts
  and ACK count/latency for acknowledgements in the last 30 days. Age and latency
  start at EigenFlux inbox receipt. ACK proves durable local intake only.

Grant Grafana access to the views, never the underlying event or inbox tables.
Deploy the receiver before adopting the new CLI. Legacy events remain valid.
The detailed receiver reuses the existing JSON properties and needs no database
migration. Historical clients do not produce detailed evidence; absent events
do not prove success or failure. Retained diagnostic snapshots do not backfill
historical execution attempts. The anonymous stage views exclude snapshots and
do not expose local error codes, receipts or bridge details.

## Console user actions

Reuse Console `trackTelemetry` and `/api/v2/telemetry/events:batch` with event
`commission_order_action`. Properties are limited to fixed `action`
(`open_payment`, `copy_order_instruction`), `result`
(`started`, `succeeded`, `failed`), `route=/dashboard/trade`,
`entity_type=commission_order` and an optional canonical positive `entity_id`.
Copy-to-Agent success means clipboard completion; it must not claim successful
order creation, confirmation or review. Payment-link success does not prove
payment received. Authoritative transaction stages come from order events and
reviews. Telemetry failure never blocks the
user's operation. Other Console events keep their existing contract.

## Intake and startup diagnostics

Watch setup emits optional fixed-enum `outer_loop.failure_reason` in the existing
maintenance lane while retaining the coarse `error_code`. It separates Skills
target, signature, manifest, file-integrity and compatibility failures from Windows
scheduler access, ownership, registration and startup errors. Missing detail on
older clients stays unknown. These observations do not alter startup gates.

Order ACK intake evidence is recorded in gateway `notification_ack_receipt` logs
with `receipt_kind=displayed|desktop_saved|execution_enqueued|unknown` and
`evidence=client_reported`. Join authenticated Agent and notification IDs to inbox
facts; never infer execution, acceptance or settlement from the intake class.
These logs do not backfill historical ACKs or add database columns. HTTP bodies
retain the legacy schemas: optional ACK intake and watch failure details use
`X-EigenFlux-Notification-Receipts` and `X-EigenFlux-Watch-Failures` headers.
Older gateways ignore them; new gateways discard invalid metadata without
rejecting valid ACKs or batches. Missing detail stays unknown. Client/backend
upgrade order and backend rollback therefore do not block the original flows.
