# Account maintenance observations

Update this map whenever queue or event logic changes.

`events.go` owns validation, account-scoped atomic storage, bounded retention,
deduplication, backoff and acknowledgements. Local events retain at most 1024
entries for seven days. Network work happens outside the state lock; only
acknowledged event IDs are removed.

`Flush` sends at most fifty observations, prioritizing existing maintenance events.
Runtime observations use separate batches and backoff so an older API rejecting
them cannot block existing maintenance uploads.
`FlushUninstall` selects only the current uninstall attempt and bypasses that
backoff, so an online removal is observable before its CLI exits even with an
old maintenance backlog. It preserves unrelated history. The caller pins account
identity, supplies a bounded HTTP budget, and carries no free-text reason into
events. See `cli/cmd/UNINSTALL.md` for the uninstall lifecycle and server contract.

Notification `status` observations may carry a complete `device_id` /
`device_name` pair. Device IDs are random 32-hex installation IDs; names are
nonempty sanitized strings of at most 64 Unicode characters. Setup and send
events cannot carry this pair. Old records without device metadata remain
valid. The existing `result`, `error_code`, `platform` and original `event_at`
describe permission observations; queue upload time must not refresh their age.

Commission diagnostics reuse this queue and its upload acknowledgements. Keep
[the stage contract](../../../docs/dev/commission-observability.md) synchronized.

Updater observations use the same queue with an identity-pinned token and endpoint.
Uploads do not refresh credentials or mutate business state. CLI update phase
observers remain best effort; rollback finishes before cancellation returns.
`RecordCLIAdoption` atomically retains the CLI installation event, principal and
endpoint independently of upload acknowledgements. A matching new CLI process
records `execute.executed` under that original attempt and clears the pending
receipt. Retention remains seven days; account/endpoint/version mismatches do
not establish adoption. This does not establish runtime readiness.

Heartbeat CLI results include `reason` for skipped, locked, deferred and failed
checks; `next_check_at` identifies the next eligible heartbeat check. Successful
checks wait 24 hours; failed or interrupted attempts retry on the next heartbeat
after five minutes, with `reason=retry_interval` during that wait. Existing
binary-scoped state is reused; no timer or scheduler is added. These deferred
checks do not create failure observations. CLI phase failures and Skills
plan failures emit a local `[eigenflux:maintenance]` line with `attempt_id`,
`phase` and a fixed `error_code`. Observations contain fixed classifications,
never raw error text. Local Skills validation reports the current CLI, required
CLI and manifest revision without relaxing signature or rollback checks.

The heartbeat verifies the local manifest signature even when automatic sync
retains an existing installation. Compatible, correctly signed offline Skills
remain usable without creating verified-remote compatibility evidence.

A replacement command's nonzero exit retains its original exit status and a
local diagnostic. If the same attempt already recorded the target CLI as
`execute/executed`, later business failure does not overwrite that update result.

Runtime environment observations and heartbeat stage receipts share this queue.
`cycles.go` pins the planned stages to an account and a random cycle ID. State
updates are locked/atomic; at most 256 cycles are retained for seven days.
Completion requires all planned stage receipts; repeats do not create events,
terminal failures cannot be rewritten as success. Missing receipts remain
unknown. `EIGENFLUX_RUNTIME_OBSERVATIONS=0` disables creation. Upload errors never
repeat business actions. OS/architecture come from the running binary; model
and host are reported evidence, not inferred hardware or user participation.

A generated plan contributes environment evidence only. The first valid stage receipt records execution with its observation timestamp. Cycle failure requires a failed stage receipt. Interrupted or missing stages remain unresolved.

`need_capture.go` retains local submission receipts for at most 64 Need plans for
seven days using the same scoped atomic storage. These records are independent
of runtime telemetry and never enter the upload queue. They hold selected Intent
IDs/Intent versions/capture revisions, CLI/Skills versions and fixed result/error codes, not source text.
Confirmed submissions survive later failed retries. Missing responses remain
unconfirmed; local receipts do not replace the server's Pending selection.
