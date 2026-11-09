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

A generated plan contributes environment and delivery observations, not execution evidence. The first valid stage receipt records execution with its observation timestamp. Cycle failure requires a failed stage receipt. Interrupted or missing stages remain unresolved.

Cycle rules are pinned by path, UTF-8 byte ranges and a cycle-specific digest.
Rule pages and Stop continuation bindings live in separate account-scoped
`.cycle-gates` files, protected by the existing account state lock. A reserved
stage-map marker fences required rule metadata; legacy CLI/watch rewrites retain
that marker without dropping the separate rule receipts.
`heartbeat rules` exposes one bounded page and its receipt. Acknowledgements
require the exact page body hash; changed sources require a fresh plan. Business
completion receipts and cycle completion reject unacknowledged required rules.
`heartbeat cycle-check --finish` lists missing pages/stages and atomically records
completion only after all receipts exist. It never performs business work.
The owner-scoped Codex `heartbeat stop-check` hook reads only command-plan output
from the exact current turn and continues an open cycle before the host exits.
Other chats and ordinary foreground answers pass through. Install the hook in
the owner host configuration using the exact CLI Home/server and `--thread`;
Codex requires the owner to trust a new hook in `/hooks` before it runs. Keep
existing hooks and trust decisions intact. Hook continuations reuse the locally
pinned cycle even when the host supplies a new continuation turn ID. Transcript parsing is
a bounded local host adapter, not a stable cross-host protocol; unreadable current
cycle evidence blocks stopping instead of establishing completion.

Codex foreground setup uses `heartbeat guard-setup` to append owner-thread and
Agent-fenced Stop, UserPromptSubmit, PermissionRequest and Interrupt hooks.
The trigger handler admits only the exact verified canonical prompt hash.
Continuation markers retain the same local cycle or an admitted no-plan run;
quoted templates and ordinary replies do not admit execution. Hook configuration
remains trust-pending until the host invokes it; setup never changes host trust.
Native scheduler migration preserves the complete `scheduler_prompt`, including
the instruction to follow the plan; it never replaces that prompt with only
the plan-producing command. Retain the same task, state and watch ownership.
`heartbeat permission-check` uses the selected Codex execpolicy checker and all
explicitly supplied rule files for the stable executable and stage commands.
Bare-executable allows are only legacy-mismatch evidence, never a policy repair.
These commands do not flush Feed or business queues. Respect the runtime
observation opt-out. Host observations contain no network calls; the independent
maintenance uploader delivers the bounded queue.

Cycle observations distinguish planning from execution. `ObserveCycle` records
`plan_created`, `incomplete`, `permission_required`, `permission_denied` or `interrupted` only at the
cycle stage. These observations never change terminal results, count as a stage
receipt, or repeat business work. Emit `plan_created` only after the plan is
successfully delivered. Missing execution receipts remain unknown unless a
local check actually observes an incomplete cycle; they never establish a
permission failure or a missing scheduler trigger.

`RecordLifecycle` records fixed trigger, permission, guard and interruption
categories with token-only environment metadata. Trigger observations do not
prove scheduler persistence; configured guards do not establish host trust or
activation. Record active guards only from their actual host invocation.
Permission observations require an actual local policy check or host approval request;
unknown policy evidence remains unknown. No raw errors, paths or policy bodies
are uploaded.
Use permission/denied and cycle/permission_denied only for a trusted final host
decline of the scoped CLI command. Permission requests alone remain required.
A denial observation is historical; successful authorized execution may resolve
the barrier without deleting telemetry or changing an unfinished cycle outcome.

`cycle_observations.go` retains pending observations and upload acknowledgements
in a separate account-scoped `observations` gate file under the existing state
lock. It deduplicates by attempt, stage and result across flushes. Flush restores
pending events lost by a legacy State writer and removes acknowledged rows
resurrected by one. Retention is seven days, with at most 4096 receipts; capacity
failure never evicts business or existing maintenance evidence. Upload failures
remain independent from execution. An unreadable or newer-format observation
sidecar emits one fixed local diagnostic per CLI process and skips sidecar
recovery and acknowledgement; the original queue continues uploading, including
notifications and uninstall receipts. Preserve the sidecar for diagnosis.

Stop admission uses an exact current-turn plan or a local `cycle-check` command
result, independently of final-response formatting. Ordinary tasks without that
evidence pass through; a foreground resume may check the existing cycle without
creating a new plan or replaying completed business actions.

## Existing Codex upgrade acceptance

`cmd/watch_upgrade_repair.go` runs account-scoped acceptance independently of
heartbeat and Feed intake, including notifications-only watches. Reuse the
existing independent signed updater and its network backoff. Load only verified
signed repair Skills; keep ordinary PM, Orders and Feed outside this lane.
`cmd/heartbeat_upgrade_repair.go` persists one request per Home/server/endpoint/
principal/Agent. The request survives restarts and does not follow an account
switch. Return the pending request from foreground update recovery and heartbeat
plans so original-chat continuation can finish without user-run commands.

Use the existing bound Desktop owner's maintenance input. Recheck binding
revision, thread, environment, runtime mode and account before submission,
during execution and after return. Persist submission before native input;
retry only confirmed pre-admission failures with backoff. Unknown submission
stays blocked for foreground continuation; never replay a possibly admitted
maintenance turn. Missing binding leaves a durable request and one desktop
fallback instead of guessing an owner. New blocker fingerprints may notify;
unchanged acknowledged actions stay quiet. Rules changes remain foreground
operations under verified authority. Hook trust remains an owner operation.

`heartbeat upgrade-repair-report --stdin` records the complete fixed blocker
set and pending state. It cannot assert trust or success. Acknowledge only the
exact submitted combined prompt using `upgrade-repair-ack`; do not acknowledge
whatever blocker set happens to be latest after an asynchronous turn. The Stop
hook marks acceptance verified only for a post-request full completed cycle
with the canonical owner trigger observation. Hook setup pins the scheduler
thread independently of the maintenance delivery thread; changing a PM main
chat must not redirect or prevent heartbeat acceptance. Foreground success, watch health,
configured hooks and notification readiness cannot establish acceptance.

Upgrade acceptance pins the complete guard definition, including the selected
Codex store, canonical prompt and exact platform handlers. Hook setup propagates
pending-repair binding failures so it remains safely retryable. An unchanged
thread cannot inherit old acceptance after its definition changes. The trusted
Stop handler carries its definition fingerprint; older handlers retain cycle
protection but cannot verify a new guard. A cycle must start strictly after its
original canonical trigger, including on continuation turns. Uploaded lifecycle
receipts retain that original trigger time locally.

During CLI-before-Skills rollout, an intact signed pre-repair onboarding bundle
is release-pending, not corrupted. Retry it quietly when auto-sync is enabled;
retain a one-time foreground action when it is disabled. A verified synchronization
writer lock is also transient. New-contract missing files, modified content and
invalid signatures remain blocked.
