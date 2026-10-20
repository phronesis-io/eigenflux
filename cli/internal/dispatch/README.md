# Agent Dispatch: Implementation

Read before changing dispatch logic. Update affected sections in the same change.
CLI owns routing, execution state and delivery; synchronized Skills own Agent decisions.

## Code map

| File | Entry points / responsibility |
|---|---|
| [commission_trial.go](../../cmd/commission_trial.go), [trial decision](commission_trial.go) | Mandatory publish trial using the bound runner, fresh draft, signed trial rules and verified local artifacts |
| [watch.go](../../cmd/watch.go) | `accountWatch.run`, `pmLoop`, `deliverPM`: account lock, WS/SSE, heartbeat, event lifecycle |
| [watch_binding.go](../../cmd/watch_binding.go) | Bind/doctor/status/retry/reconcile; `applyDispatchOwnership` |
| [watch_update.go](../../cmd/watch_update.go), [watch_update_unix.go](../../cmd/watch_update_unix.go), [watch_update_windows.go](../../cmd/watch_update_windows.go) | Close execution admission before CLI adoption; drain admitted work, release account lock, preserve arguments and pinned identity; supervised watches notify the host after draining |
| [watch_main_session.go](../../cmd/watch_main_session.go) | Codex setup-thread capture, host-native scheduler bootstrap, explicit switching and heartbeat status |
| [watch_restart.go](../../cmd/watch_restart.go), [watch service](../watchservice/README.md) | Restart an explicit existing OS task; preserve its account/wrapper; distinguish process startup from runtime readiness |
| [watch_commission.go](../../cmd/watch_commission.go) | Commission notification intake, durable enqueue before ACK, bounded HTTP reconciliation |
| [watch_commission_intake.go](../../cmd/watch_commission_intake.go), [watch_commission_materials.go](../../cmd/watch_commission_materials.go) | Seller inspection, fixed identity/order/Skill, verified downloads, atomic paid-work enqueue |
| [watch_commission_clarification.go](../../cmd/watch_commission_clarification.go), [commission_clarification.go](commission_clarification.go) | Order-scoped seller questions, durable buyer answer lane, strict result and input receipt validation |
| [main_notice.go](main_notice.go), [main_notice_codex.go](main_notice_codex.go), [codex_owner_ipc.go](codex_owner_ipc.go), [codex_owner_ipc_windows.go](codex_owner_ipc_windows.go), [main_notice_probe.go](main_notice_probe.go) | Exact main target, Desktop owner IPC, read-only completion receipts and connectivity probe |
| [main_notice_acp.go](main_notice_acp.go), [ACP bridge contract](../../../docs/dev/acp-main-conversation.md) | Negotiated existing-owner hidden input, exact opaque ACP target and correlated final result; no ordinary prompt/load/cancel fallback |
| [watch_commission_fulfillment.go](../../cmd/watch_commission_fulfillment.go), [watch_commission_artifacts.go](../../cmd/watch_commission_artifacts.go), [commission_fulfillment.go](commission_fulfillment.go) | Separate paid local work, strict decision/evidence, retained outputs and recovery |
| [watch_dispatch.go](../../cmd/watch_dispatch.go) | `enableDispatch`, `pollPM`, `dispatchLoop`, `dispatchJob`, `dispatchPrompt`, `sendDispatchReply` |
| [binding.go](binding.go), [types.go](types.go) | Binding validation, atomic writes, `ParseDecision` |
| [journal.go](journal.go) | `AddMessages`, `AddHint`, `AddCommissionNotification`, `Next` / `NextKind`, `Update`: persistence, deduplication, sessions, recovery |
| [desktop.go](desktop.go), [desktop queue](../desktopqueue/README.md) | `CommissionBlockers`: validated routing metadata for unresolved local work; desktop delivery remains separate from execution |
| [runner.go](runner.go), [acp.go](acp.go) | `Runner.Run`, `RunCommand`, `runACP`: host execution and result parsing |
| [process_unix.go](process_unix.go), [process_windows.go](process_windows.go) | Process-group / Windows Job Object cancellation |
| [replace_unix.go](replace_unix.go), [replace_windows.go](replace_windows.go) | Atomic file replacement |
| [heartbeat.go](../../cmd/heartbeat.go) | Companion stages and `--watch-managed` launcher |
| [dispatch Skill](../../../skills/ef-communication/references/dispatch.md) | Agent decision rules |
| [capability registry](../../../api/consolev2/capability_registry.go) | Command discovery |

## PM flow and invariants

CLI 1.0.22 accepts explicit ACP `main_session_id` and optional `main_command`
for the separate main-notice lane. The bridge override uses a copied binding
and clears ordinary args; seller checks, fulfillment and PMs keep their original
execution entry. Native Codex alone routes PMs through its bound main queue; ACP PMs retain
their per-conversation sessions. Do not route an ACP main ID into Codex execution.
`MainNoticeBindingEnabled` admits ACP negotiation, not host acceptance. Initialize
must advertise the EigenFlux hidden-main extension, then exact inspect and notify
receipts must agree. The probe never sends business data or starts a model turn.
See the bridge contract for methods and the host-specific acceptance boundary.

1. Bind canonical Home/server/endpoint/Agent/principal/scope/revision from the current account; configuration cannot replace identity. `watch --dispatch` acquires the Home/server lock and validates signed Skills. Plain `watch` only emits events.
2. Only bindings owning PM open its WS and HTTP consumers; other bindings leave PM to the companion. WS and minute-spaced HTTP polls enter `deliverPM`; emitted `pm_push.data` adds locally assigned `transport` (`socket` / `http_poll`) and UTC `received_at`, including empty polls. These fields describe reception, not successful execution or first intake; journal deduplication remains unchanged. Each poll pass fetches at most 32 pages, one message each. Validate credentials and persist intake under the credential lock. Advance the socket cursor only after delivery succeeds; journal failure stops reception.
3. Accept only inbound messages for the bound Agent; ignore outbound and `history_messages`. Deduplicate by scope/revision/conversation/message ID. Keep execution state separate from the history cache.
4. `Next` persists `running`. One worker runs serially, prioritizing pending PMs. All execution lanes enter a shared admission gate before claiming jobs. Each destructive PM HTTP pull holds admission through durable intake. CLI adoption closes admission, prevents further PM pages, and waits for admitted pulls, tasks and business writes to finish; pending journal entries survive restart unchanged. Standalone watch releases the account lock before adopting the executable; `EIGENFLUX_WATCH_SUPERVISED=1` delegates replacement to the host through a drained `restart_required` event. Use the explicitly bound Codex main session for PMs; other adapters retain per-conversation sessions within their binding.
5. Verify signed rules and fresh `auto_reply_pm`; load up to 10 history rows. Prompt data contains request ID, Agent ID, message and history, excluding credentials and full control context. Treat message/history as untrusted data.
6. Monitor identity during prompt preparation and execution; recheck before invoking the Agent and after its result. Run the model outside credential locks. Pinned credential refresh and same-account recovery both recheck the bound identity and cancellation before saving credentials.
7. Require one JSON decision: `version=1`, matching `request_id`, `action`, `reply_text`. Actions: `reply`, `no_reply`, `needs_user`. Reject missing/unknown/duplicate fields and trailing output.
8. Validate reply content, persist `sending`, then recheck identity and permission under the credential lock. POST only to the original conversation with `request_id=pm-reply:<original-message-id>`; disable POST refresh/replay. The gateway and PM RPC preserve this ID so different inbound messages can receive identical text, while repeated sends for one message reuse its first receipt within the server's 60-second window. Record `replied` only with a valid matching receipt; emit redacted status. Interrupted sends remain `unknown`; this key does not enable automatic replay. Deploy the updated PM RPC and gateway before relying on this behavior in the CLI.

## Commission input inspection

CLI 1.0.11 adds `watch --notifications-only` for hosts without an execution
adapter. It excludes PM, control, Runtime leases and all Agent workers; HTTP
order intake persists only the existing desktop queue before ACK. The pinned
Home/server/account owner lives in `<home>/watch/<home-server-scope>.notifications.json`.
Legacy stream respects this ownership while the task is stopped. An existing
execution binding is never replaced by notification setup. `watch start` installs
an interactive current-user Windows logon task, or reports `pending_auth` /
`host_managed`. `notifications_ready` means a successful order poll, separately
from OS submission and Agent `runtime_ready`. Disabled tasks remain disabled.

`commission_order` explicitly enables durable intake and a separate seller inspection worker. It also queues paid local fulfillment after a ready check. After actual Agent readiness it accepts awaiting-seller orders; after paid fulfillment it uploads verified contractual artifacts and submits delivery. Subscription alone does not prove host execution readiness or establish a buyer SLA.

With PM subscribed, notifications reuse its socket; Commission-only uses an initial HTTP pull and 60-second reconciliation because the server socket fetches PM on connect. Preserve before ACK, at most 50 IDs per ACK, and deduplicate order/version/role within the pinned binding. Notification cursors never replace PM cursors. Legacy stream respects persisted ownership; binding changes and legacy read/ACK share the credential lock.

When desktop notifications are enabled, save both the execution job and desktop
message before ACK. The desktop queue deduplicates notification IDs separately,
so same-version reminders still appear. Desktop delivery for subscribed buyer and seller events
does not require Agent execution. `CommissionBlockers` exposes only validated
job/order/role/status/code for unresolved local work; retain `Snapshot` payload
redaction. Desktop delivery and OS retries never change business execution state.
Setup runs before any order arrives.
Windows setup is silent. `registered` / `pending_first_notification` means the
sender exists but its OS setting is not known yet; allow the first real queued
message to establish it. This is neither a denial nor a `ready` permission claim.
CLI and watch also save bounded Home/server-local observations, readable through
`notify log`. Submission and retries do not claim banner visibility or attention.
Notification observations bind to the account at creation and enter the existing
maintenance upload lane. Import pending local events before each flush; never
reassign another account's records. Only setup captured before authentication
may bind to the first account. Installer/manual/test/watch sources remain
distinct; remote upload failure never changes notification delivery state.
`watch_notification_permission.go` independently performs a silent OS `Check`
every minute while desktop notifications are enabled. Startup, changes and
five-minute renewals add an installation-scoped `device_id` and sanitized
`device_name` to status observations in the same bounded maintenance lane.
Checks never register, prompt or display a notification; Windows first-use
registration remains unknown. Delayed uploads retain their observation time,
so the server can mark offline devices stale. This lane adds no PM notifications.
Setup's `desktop_notification_status` keeps
the existing status and adds fixed `code`/`action` on failure: missing helper,
unrequested permission, denied permission, timeout, unsupported platform or
other setup failure. Never expose raw provider data. Update the complete Mac
CLI/helper package, run `notify setup`, and restart watch with its pinned account
parameters for helper-only or same-version test replacements. A running watch
adopts a newer installed CLI automatically after all active jobs finish.
`watch restart --launch-agent <plist>` or `--scheduled-task <task-path>` restarts
the explicit existing OS task. Its definition selects the account, not CLI Home
flags. Source installation can invoke it with `--watch-launch-agent`; notification
setup failure does not skip that restart. A `process_started` result does not
establish network readiness; require fresh `runtime_ready` and business evidence.
`started` and `runtime_ready` include the running `cli_commit` and `pid` so two
test builds with the same version can be distinguished after restart.

Watch notification lifecycle observations use `notification.action=watch` in
the maintenance channel. Correlate order version, role, source notification or
local blocker job/status/code, notification hash, lifecycle stage and attempt.
`queued`, `prepare_failed`, `os_submitted` and `os_failed` do not claim human
visibility or business completion. Immutable correlation is stored in the
desktop queue; legacy entries omit unknown historical metadata.
Invalid local observations retain original fields with
`rejected=true/rejection_code=invalid_observation`; they do not block subsequent
valid receipts. Transient I/O or lock errors stay retryable. No PM observation
or desktop notification is added. Deploy API validation before the matching
CLI; older APIs reject the new action and leave batches pending.

Refund processing errors, cancelled-order late-payment refunds, wallet credit
success/errors, and validation processing errors are informational. The intake
worker completes these result notices before reading the order or calling an
Agent, even if their payload reports an actionable state. Keep the existing
order/version/role execution key; desktop delivery independently preserves each
notification ID. Terminal and desktop use the same event priority:
recognized `payload.event_kind`, then envelope `type`, then order state. Seller
rejection uses `command_kind=reject_order`. Wallet credit does not mean withdrawal.

Desktop copy follows the current user's first OS UI language: Chinese variants
use Simplified Chinese; other languages or detection failures use English.
Restart watch after changing OS language; already queued messages keep their
text. Detection and native boundaries live in `desktopnotify/README.md`.

`watch_desktop_copy.go` owns buyer/seller title/body templates from the approved
copy document; terminal summaries stay unchanged. Render event deadlines at
intake in local time with the UTC offset, referring to the order page if absent.
`watch_desktop_service.go` resolves the frozen contract title through the existing
order detail API in the desktop worker, never under the intake credential lock.
Validate the target URL, order and recipient account, sanitize/limit the title,
and recheck identity before OS submission. Lookup failures retain the notification
for existing queue retries with `service_name_unavailable`; older queued copy
without the service placeholder remains unchanged. This adds no Agent calls.

`Next` excludes Commission inspection, fulfillment and clarification jobs;
`NextKind` serializes their separate workers. Buyer input-request events enter
only the answer lane. Other buyer notices do not invoke seller execution. The
separate main-notice lane is capability-gated. The seller worker re-reads identity,
order state, frozen contract and fulfillment Skill. Only `awaiting_seller` invokes
model input inspection; pending payment waits and in_progress reuses retained
evidence. Read-only preparation and execution share a local 180-second ceiling,
excluding queue time; the host's shorter timeout still applies.

Official dispatch rules must be signed manifest members, read through `skills.ReadSignedFile`; third-party fulfillment Skills remain explicitly bound local content. Fetch the authenticated V2 order-detail input manifest, then each immutable snapshot/path grant using pinned credentials. Check manifest role/version/buyer, SHA-256 and byte count before passing local files to the Agent. Inputs use their own snapshot IDs, not the current order snapshot. Bound downloads to 128 files / 64 MiB total; keep URLs and credentials out of prompts. The public endpoint requires the exact GET detail Caddy route.

Use a fresh Agent session. Require matching request/order/version and `ready`, `needs_input`, `needs_user` or `failed`, a brief summary and inspected logical paths. `ready` must cover all supplied verified inputs; required materials cannot be empty. Recheck identity and unchanged order. For awaiting-seller `ready`, POST seller-authenticated acceptance with expected version and deterministic key; uncertainty becomes `unknown`, without automatic replay. Atomically store `commission_result`. Preserve the result when completed notification bodies are compacted. Preserve the actual pre-acceptance ready result and input fingerprint across acceptance/payment versions; explicit retry may recheck only before acceptance. Interruptions remain `unknown`; do not replay automatically. Clean up owned temporary downloads after the run. Results are local status data, not user-visible or buyer-visible receipts.

## Paid local fulfillment

`SaveCommissionReadiness` persists the original ready check before acceptance. `QueueCommissionFromReadiness` atomically queues paid fulfillment after fingerprint validation, retaining the original inspection version separately from the current execution version. Protect unconsumed readiness from ordinary log compaction. Deduplicate by binding scope/revision/order, including across notification versions. A new paid notification without verifiable readiness blocks; it never invents or repeats a pre-acceptance model check. Historical orders are not bulk-scanned.

The separate fulfillment worker re-reads the same paid order version, resolves signed `fulfillment-dispatch.md`, and verifies freshly downloaded inputs against the retained contract/input/file fingerprint. It uses the binding's host timeout, not the intake budget. Poll order state every 10 seconds independently of one-second local identity checks; a blocked network read cannot delay identity cancellation. Changed identity/order or interrupted execution stops work without automatic replay.

Persist a new private output directory inside the bound WorkDir before invoking a fresh Agent session. Skills perform the frozen procedure, write new local deliverables and self-check contractual conditions. CLI requires a matching structured decision, then verifies all declared regular files through a confined filesystem root, rejects symlinks/traversal and duplicates, and computes SHA-256/byte counts from bounded reads. Limits: 128 outputs / 64 MiB. Portable validation cannot prove hard-link origin. Models may only claim local completion; unsupported tools, permissions or external effects require user action.

Preserve output directories, including partial/unknown work; temporary input copies are removed. Persist exact output paths, checks and hashes. For `artifacts_ready`, freeze verified output bytes into private upload copies, persist `sending` with artifact evidence, upload/confirm under fixed keys, and submit the manifest with expected order version. Read back a later validating/awaiting-buyer/completed state before `commission_delivery_confirmed`. Uncertain writes remain `unknown` and require reconciliation. Buyer acceptance stays separate. Retry uses a new output directory and retains old files; unknown execution requires operator reconciliation. Order-level deduplication follows the journal retention window; manually completed records eventually trimmed from the latest 1024 records are not permanent tombstones. Recheck order and bytes before any later submission.

Native/command reuse the existing Runner. ACP Agents need their own usable filesystem tools; this client supplies no filesystem/terminal RPC. Unit fixtures or cross-compilation do not establish a host's ability to produce artifacts. The opt-in `EIGENFLUX_COMMISSION_NATIVE_SMOKE=1 go test ./cmd -run TestCommissionNativeCodexLocalFulfillment -v` uses authenticated Codex with isolated fixture orders and can consume model usage. On 2026-09-28, real Codex inspection and file generation passed in 35.4s and 83.9s; this does not verify real Commission service orders or the buyer deadline.

## Order execution issue reporting

CLI 1.0.13 captures the authoritative order version and `execution_revision`
when seller inspection or paid fulfillment begins. Its journal retains this
fence separately from private inputs, including after completion compaction.
An independent 15-second worker submits at most 16 pending observations per
pass to Commission `POST /api/v1/orders/:order_id/execution-report` using a
stable body-derived idempotency key. Reports contain only order/job IDs, the
captured versions, stage, blocked/resolved status and a fixed error category.
Skill absence, unreadability, invalid content and invalid contract are distinct.
The order service derives the seller from authentication, checks order ownership
and CAS revisions, persists a resolved tombstone, and projects the current issue
to Console list/detail reads. Buyer projections contain only a generic category.
The gateway's existing trade BFF passes through this projection.

Successful report receipts must contain exactly the next execution revision.
Conflicts retire a superseded observation without refreshing its fence or
replaying business work; other failures remain pending across restarts. Neither
reporting nor notification delivery changes execution or transaction state.
Only a context that observed an existing issue can report recovery. Paid intake
hands recovery to fulfillment rather than clearing concurrently with that lane.
Order version changes and terminal states hide obsolete issues server-side.
Old jobs without a captured fence are not uploaded by guessing current state;
explicit retry captures a fresh one. Retention follows the execution journal's
existing capacity limits, rather than a permanent audit history; pending recovery
observations are retained ahead of ordinary completed dedup receipts. A journal
containing only pending observations still obeys that hard limit. Operator-verified
completion can clear only the exact blocked revision acknowledged by the server,
without guessing or fetching a replacement fence.

Desktop order blockers use fixed reason-specific text and retain the exact
account/order link. A payment event says awaiting delivery; it cannot certify
that a local Agent is executing. PM states never enter desktop delivery.
Unconfirmed inspection, fulfillment, acceptance and delivery submission have
distinct local titles and recovery instructions. Agent-call errors identify the
failed call without inventing a timeout or asserting order failure. Interrupted
work identifies the interruption without guessing its cause. Acceptance and
delivery instructions require checking existing records before repeating writes;
raw runner errors and local paths never enter notification copy.
Install the Commission execution-report API and its database migration before
this CLI to enable Console issue visibility; a missing API leaves observations
queued and does not stop or replay execution. Installing only frontend code does
not establish working backend reporting.

## State and limits

| State | Meaning / recovery |
|---|---|
| `pending` → `running` → `sending` | Persist before execution and before POST; recover interrupted `running`/`sending` as `unknown` |
| `replied`, `no_reply` | Confirmed receipt/Agent decision, or explicit operator reconciliation |
| `failed`, `needs_user` | Preflight/decision failure or permission/input required; explicit retry or operator-verified reconciliation allowed |
| `unknown` | Uncertain execution/send; ordinary retry forbidden; operator-verified reconciliation only; PM: `replied/no_reply`; optional event: `completed/failed` |
| `completed`, `accepted` | Optional event not due/profile stamp confirmed/operator verified; `accepted` is a legacy unverified terminal state |

Only `unknown/failed/needs_user` allow verified reconciliation; active and completed jobs do not. Unverified optional-event results remain `needs_user`. Reconciliation to `failed` requires confirmation that work did not complete; it never queues a retry.

`OpenJournal` recovers state; `ReadJournalStatus` is read-only. Persist before changing memory; roll back failed saves. Rebinding rejects unresolved jobs. Stop watch before mutating binding/retry/reconcile; they share its lock. Read-only binding preflight runs while watch remains active. `watch retry --order-id ORDER_ID` recovers one missed seller intake from authoritative order detail only in `awaiting_seller` or `in_progress`. It requires the `commission_order` subscription, preserves the ordinary order/version/role dedup key, records `operator_retry`, and never synthesizes or acknowledges a notification. Existing fulfillment records and uncertain intake executions require recovery by job ID. Status and doctor expose `commission_subscription`; a PM-only binding reports `disabled_orders_not_consumed`.

Limits: journal 16 MiB, 256 unresolved jobs, latest 1024 completed jobs (including `accepted`); binding 64 KiB; prompt/output 1 MiB; rule/history each 64 KiB; timeout 600s by default, configurable 1–3600s. Completed jobs drop message content/event data on save, including legacy records; deduplication keys and receipts remain. Status omits payloads.

## Runners and ownership

### Opt-in Codex PM main session

The canonical onboarding scheduler entry is now `skills/ef-onboarding/references/host-setup.md`;
its Codex PM setup retains the main-session preflight, scheduler readback and
unchanged-route recovery contract. `recurring-trigger.md` is a compatibility entry.

`main_session_id` selects an existing Codex thread for PMs only. Native Codex
bindings must pin an absolute `env.CODEX_HOME`. Validation checks the exact UUID
against the host-owned `sessions` rollout metadata, excluding archived sessions;
unavailable or unsupported storage fails closed. No transcript is copied or
selected by title, directory recency, or another conversation's activity.
The runner resumes that UUID and requires the native result to return the same
thread ID before accepting its decision. Other events keep their current routing.
Other hosts do not yet support this field.

CLI 1.0.5 never infers the setup thread from the absence of a watch binding.
A new native Codex PM binding requires the host's verified heartbeat target
or an explicit `main_session_id` in its configuration. This preserves the main
conversation of existing users enabling watch for the first time. The Codex store
resolves from explicit selection, binding environment, host environment, then
the user's standard `.codex` directory; its absolute path is persisted.

For existing installations, heartbeat plans report `main_session_binding`
(`bootstrap_required`, `ready`, `unavailable`) and include the signed
`ef-communication/references/main-session.md` when repair is needed. CLI 1.0.10
prepends `main_session_bootstrap` before business work in full native Codex Skill
plans after onboarding; the upgrade completion plan uses the same entry. The
host completes initial binding automatically, without another enable request.
Narrow watch-owned handlers defer bootstrap to the independent host cycle.
Missing or unsigned rules cannot schedule the automatic stage. The host
reads its complete native scheduler inventory and submits it through
`watch bind --scheduler-stdin` or `--scheduler-file` with the existing revision
and empty expected ID. A new watch binding accepts the same input with `--config`.
`--scheduler-json` remains available for small input; stdin/file avoid OS argv
limits while retaining the 2 MiB inventory bound.
CLI reuses scheduler inventory validation: one active owned EigenFlux heartbeat
for the exact Home/server, with a validated existing `thread_id`. Paused historical
tasks never select the target. Duplicate task IDs still fail across the complete
inventory. Titles and
private scheduler files do not establish ownership. Scheduler bootstrap is
initial-only; it cannot replace an established main session. Host lifecycle owns
stopping/restarting watch. `--preflight` validates without acquiring the live
watch lock or changing binding/jobs, so invalid routing leaves the consumer
running. Apply a fresh native scheduler readback after stopping watch and pass
the preflight's `--expected-task-id` and `--expected-thread-id`; changed task,
target, active state, ownership or identity fails before the write. New creation
also carries empty expected session/revision and refuses a newly present binding.
The host owns freshness of the supplied inventory; CLI cannot query its native
scheduler. Restore the same previously running entry on failed writes with
unchanged identity/routing. Preserve inactive/disabled entries and report unsafe
or failed cleanup explicitly. Neither heartbeat planning nor PM handling writes
the binding. Unavailable established sessions require explicit owner switching.

Preflight while watch is active, then stop it before `watch bind --main-session-id UUID --expected-id OLD_ID
--expected-revision REVISION --codex-home ABSOLUTE_PATH`. For initial binding,
pass an empty expected ID. `--current-session` explicitly reads the calling
`CODEX_THREAD_ID` instead of `--main-session-id`; ordinary heartbeat or message
processing never selects it. Both versions of the CLI must use the same Codex store.
The update preserves the journal revision and dedup receipts, rejects stale
updates, and leaves uncertain jobs for explicit reconciliation. Session reset,
fork, or migration requires an explicit update; there is no automatic successor
discovery. Rebinding the full configuration remains subject to its unresolved-job
guard.

`dispatch_started` records job ID, requested session, configured main session,
and binding revision. `dispatch_status` includes the returned session ID and
existing decision/send receipt. A returned session mismatch is `unknown` and is
never sent or automatically retried. Missing metadata yields `needs_user`.

Native Codex PM jobs with an explicit main session use `codex queue --thread`
against the pinned Codex store. The host serializes the queued turn with desktop
and heartbeat work; the CLI never acquires another writer through exec/resume.
Queue admission is not a decision receipt. Observe only newly appended host
rollout records and require the exact submitted input, matching thread/turn,
final Agent message and matching `task_complete` before validating the decision.
`dispatch_decision` joins job ID, session, host turn (`run_id`) and action to the
existing send receipt. No second conversation store or callback endpoint exists.
Timeout, cancellation, changed stores and missing completion remain unconfirmed;
never resubmit them automatically. Cancellation stops observation, not an already
queued host turn. The CLI rechecks identity and reply permission before sending.
The host must support `codex queue` and the verified append-only rollout receipt
format. Unsupported formats fail closed. Automatic successor events and Windows
desktop lifecycle remain unverified. Fixtures do not prove a live PM receipt.

- Native: Codex exec, Claude Code print, OpenClaw `--local` with fixed `host_agent` and a fresh UUID session for first use, Hermes quiet chat. OpenClaw local JSON is parsed directly; aborted/yielded/error results fail. An active Gateway sharing its state directory must be stopped or isolated. ACP/command require fixed local argv; never derive commands from PMs. Command mode uses stdin prompt/stdout decision. Windows shims require an explicit executable/interpreter; oversized command lines yield `needs_user` before launch (use ACP/command stdin).
- ACP v1 stdio: initialize → load supported session or create → prompt. Ignore load replay; collect current-session `agent_message_chunk`, require `end_turn`. Advertise no filesystem/terminal RPC. Cancel permission requests as `ErrNeedsUser`; reject unknown RPCs.
- Strip inherited `EIGENFLUX_*`; rebuild bound Home/server/host/Skills. Only `RunCommand` targeting the verified current CLI may retain the CDN URL. Never copy ambient EigenFlux tokens/model/mode. Windows merges environment keys case-insensitively, rejects duplicate configured keys, and gives configuration precedence; Unix preserves case. Host authentication remains host-owned.
- Default event: `pm_push`. Optional profile/maintenance/control events reuse `profile refresh-task`, maintenance-only/control-only heartbeat plans. Control hints deduplicate by command ID; unresolved periodic hints coalesce. Explicit retry persists `operator_retry`; only retried profile tasks use `refresh-task --force` to bypass their prior claim cooldown.
- Companion `heartbeat plan --watch-managed` skips subscribed work; scheduler migration preserves this flag for a bound account. PM-only bindings retain heartbeat maintenance. Persisted bindings keep ownership while watch is stopped; stop competing plugin consumers before handoff.

## Regression map

| Area | Tests |
|---|---|
| Binding / decisions / journal | `binding_test.go`, `types_test.go`, `journal_test.go`, `../../cmd/watch_binding_test.go` |
| Host / ACP / cancellation | `runner_test.go`, `acp_test.go`, `process_windows_test.go` |
| Main target / Desktop IPC / hidden receipts | `main_notice_test.go`, `main_notice_codex_test.go`, `main_notice_probe_test.go`, `codex_owner_ipc*_test.go`; opt-in real host: `main_notice_native_test.go` |
| Intake → Agent → reply / identity / permission | `../../cmd/watch_dispatch_test.go`, `../../cmd/watch_test.go` |
| Commission intake / inspection / materials / ownership | `journal_commission_test.go`, `commission_intake_test.go`, `commission_fulfillment_test.go`, `../../cmd/watch_commission*_test.go`, `../../cmd/order_notifications_test.go`, `../../cmd/order_result_notifications_test.go`, `../skills/read_signed_test.go` |
| Heartbeat ownership / discovery | `../../cmd/heartbeat_modes_test.go`, `../../cmd/heartbeat_migration_test.go`, `../../cmd/capability_registry_contract_test.go` |

Run relevant tests from `cli/`; use race checks for concurrency changes and Mac/Windows builds for process/filesystem changes. Update affected Skills and capability contracts with behavior changes.

Setup, recovery commands, delivery limitations and unverified hosts: [operator guide](../../../docs/dev/agent-dispatch.md). Repository checks: [testing.md](../../../docs/dev/testing.md). Buyer-facing receipts remain separate; A2A is excluded.

## Listing publish trial

`commission publish` requires `--trial-input` (a nonempty UTF-8 buyer-input file)
and `--trial-materials` (a local input directory) when the draft requires files.
Each invocation reads the owned expected draft, verifies the Commission execution
binding and signed `publish-trial.md`, and runs a fresh `commission_publish_trial`
request using the fulfillment runner's binding, workdir, permissions and timeout.
It uses an isolated session and never creates an Order or uploads artifacts.
Skill resolution is shared with paid fulfillment, including host-directory fallback.

Sample files are bounded, verified copies; symlinks are rejected. The runner must
produce contractual files and concrete self-check evidence. The CLI validates
strict result identity and output containment, actual regular files, sizes and
digests. Draft, identity, binding and Skill changes prevent publishing. Preserve
trial files and local evidence under `.eigenflux-publish-trial-*`; report the path
on stderr. Every publish repeats the trial. An unconfirmed run requires inspection
before retry; no automatic trial replay or reusable readiness flag exists.

This is a CLI gate. Direct Commission API publishing remains independently
available; server-enforced attestation is outside this change. A successful sample
proves only that invocation, not every future input or external capability.

## Main-conversation transaction notices (test branch)

Codex on macOS uses Desktop owner IPC: the pinned CODEX_HOME/ipc/ipc.sock,
initialize v0, exact thread-owner-discovery v1, then one owner-directed
thread-follower-start-turn v2. Require supportsUntrustedAppInput. Validate private
socket/directory ownership, modes and peer UID. This is a version-sensitive local
Desktop interface, not app-server proxy. Windows uses the fixed local named pipe
`\\.\pipe\codex-ipc`, confirmed in the installed Desktop 26.1013.40934 win32
router code. Dial with go-winio anonymous impersonation and a five-second timeout.
Before initialize, verify the pipe server process token has the current user SID
and login SessionId; close on any failure. The pipe is not scoped by CODEX_HOME.
Preserve the pinned store reader, exact thread/owner discovery, hidden envelope
and matching final receipt checks. Same-user identity is not a Codex account proof.

Windows is implemented and enabled for test bindings since CLI 1.0.21, but native
Desktop/UI acceptance has not run. Report implemented_untested and host_verified=false;
macOS remains verified_macos. Separate implementation admission from acceptance,
so Windows can be tested without a new override. Other ACP hosts remain disabled;
the generic session/load skeleton does not implement their main-session binding
or hidden-input host adapter. Never select another Home/thread/owner, create/resume/fork a writer,
fall back to visible queue/exec, or automatically replay uncertain work.

Send input=[] with signed rules as application additionalContext and routing data
as untrusted context. The Desktop-native untrusted_input responseItems pair
activates its busy checks. The existing owner retains settings and starts the
turn. Already-busy rejection is verified; the host's inject_items -> turn/start
sequence is not an atomic idle-only transaction. Do not claim concurrent user
input can never race it. The local worker remains serial and does not interrupt
or automatically retry. No raw prompt is printed or sent as a user message.

A separate short-lived app-server --stdio child only initializes and reads the
exact thread/turn history. It never resumes or starts a turn. Confirm a new
owner-returned turn ID, completed status, its final_answer and no userMessage;
old turns, commentary, admission alone or a visible input never count. Cancellation
closes IPC and the owned reader, not the Desktop or its admitted turn.

watch doctor --probe-main sends only initialize/discovery. owner_ready means the
exact target is reachable and the implemented adapter is enabled, not that it is idle
or a business event completed. Its implementation_status and host_verified fields
distinguish Windows implementation from Mac acceptance. Plain doctor remains static. Recheck account and
binding before returning. On Desktop 26.1013.40934 / Codex 0.163.0-alpha.10.2,
real Go hidden-turn execution passed in 3.614 seconds; the owner confirmed the
result appeared without a prompt bubble. A second call during a controlled busy
turn was rejected and created no second turn.

main_notice_queue.go persists one notice per notification ID before ACK, separate
from seller execution and desktop delivery. Explicit main-notice capacity overflow
skips only that notice; business intake, desktop enqueue and ACK continue. Other
persistence errors retain the existing fail-closed behavior. Signed main-notice.md pins the exact
Home/server read prefix. Guard identity before/during/after execution. Failure is
local skipped/unconfirmed with no alternative target or Order mutation. Restarted
running entries become skipped and are not replayed; retention is bounded.
Notifications-only mode never invokes an Agent. Internal input coordination remains
silent and does not enter this lane.

The same serial worker synchronizes existing seller CommissionBlockers into local
notices keyed by scope/job/order-version/status/code. Use the actual execution version when
available. Retain receipts while each exact blocker exists, so bounded server
receipt eviction cannot replay it; retire resolved or changed blockers. Before
calling the Agent, recheck the job and current seller Order/version/state. Pass
only fixed diagnostic categories; delayed server issue projection is not local
success. These notices explain the blocker and never retry the job or change ACKs.

## Order-entry inspection reuse

Only awaiting_seller invokes the model to inspect inputs. Save the real ready
result and canonical contract/buyer-input/file-manifest fingerprint before accept.
Pending payment does not inspect again. Once paid, verify identity, payable state
and the same input fingerprint, then queue fulfillment using the original checked
version and the current paid execution version separately. Fulfillment retains
file download/digest checks but never repeats the model readiness assessment.

Upgraded Orders without the new local inspection evidence remain blocked; never
invent a historical inspection or silently recheck. Current input changes also
block. Ordinary buyer workspace writes remain closed after Order activation; the
request/version-fenced staged supplement is a separate operation.

## Pre-acceptance clarification and supplementation

For awaiting_seller needs_input, CLI posts the seller's bounded question through
Commission request-inputs with expected_version and the 64-hex job ID as both
request correlation and idempotency key. A pending question waits without another
model inspection or request. The server permits one pending question and at most
three rounds, increments the version, seals a snapshot and atomically notifies
the fixed buyer. Existing cancellation/rejection deadlines are not extended.

Only the authenticated buyer's order.inputs.requested.v1 notice can queue
commission_clarification. Atomically finish notification intake and admit one
job per scope/revision/order/question. Use signed clarification-dispatch.md and
a fresh existing execution adapter; this lane does not imply hidden main-thread
support or access to previous chats. Supply the frozen Order, existing buyer_input
and verified local copies of that buyer's uploaded input files. The buyer-only
reader validates manifest role/parties/version, immutable snapshot grants, paths,
file count, aggregate size and digests. Recheck Order identity/version after
download; remove private temporary files after execution. Ambient files and prior
chats remain inaccessible. Accept a strict answered/waiting decision; unknown facts remain
waiting. Fixed missing-file requests wait without running the model or uploading
unverified files. Do not scan ambient files. The execution result never contacts
the owner itself; the CLI handles a verified waiting result separately.

Before supplement-inputs, re-read the bound buyer and unchanged Order/question.
Use the buyer job's 64-hex idempotency key, disable POST refresh/replay, and verify
the returned parties, version, request and answer. Decode the appended JSON
receipt after the exact original buyer_input; raw substring checks are invalid
for escaped text. The server preserves the original input, adds the answer and
notifies only the seller. That new version permits one normal readiness check,
then the existing guarded acceptance. Payment and later input access stay fixed.
Interrupted/uncertain writes become unknown and are not automatically replayed.

Both order.inputs.requested.v1 and order.inputs.supplied.v1 are Agent coordination,
not desktop/main notices. Persist them before ACK; notifications-only consumers
leave them pending for an execution owner. This does not authorize ordinary PMs
to mutate Orders. For unresolved new facts or files, material-notice routing
persists one desktop/main reminder before completing the answer job. Re-read the
buyer, Order version and request before enqueue and dispatch; discard obsolete
reminders after title resolution. Recheck every five seconds during a main call
and at completion; an admitted host turn cannot be retracted. Notification-only
owners use the pinned buyer identity to validate a prior reminder without an
execution binding. The buyer explicitly selects files in the account-protected Order
page. Staged create/confirm bind buyer/order/request/version; atomic supplement
admits confirmed objects, seals a snapshot and wakes seller reinspection. Normal
workspace upload guards and original deadlines remain unchanged. Deploy matching
Commission migrations 15/16 and Console BFF; verify browser PUT CORS. See
cmd/watch_commission_material_notice.go and api/tradebff/ORDER_INPUTS.md.

## Publisher notification permission and buyer confirmation

Before publication approval, Skills run notify status --publish-preflight --format
json on the seller's computer and show any warning with its settings link and
manual path. The independent read-only Check returns ready, permission_denied,
permission_required or unknown; it does not register, prompt, show or upload
telemetry. The publish command retains bilingual stderr guidance as a fallback.
Preserve existing approval; permission warnings do not add a publication gate or
claim that clicking the settings link enabled notifications.

The read-only order pending-confirmations command paginates authoritative buyer
Orders and returns reminders for every normal cycle without a read receipt. Exact
Commission access denial returns not_enabled and remains quiet, rather than empty.
Full completed-access heartbeat appends commission_confirmations; standalone Feed
uses the same rule in feed.md, contract.md and the synchronized served static
contract. Pending confirmation is an explicit exception to Feed silence, including
empty or unchanged Feed cycles. Do not expand maintenance/control or baseline access,
append prose to machine Feed JSON, or automatically confirm receipt.

## WorkBuddy publication preflight

Notification helper filesystem denial is `unknown/local_access_denied`, not OS
permission denial. Retry the same read-only check through host-authorized local
execution. Preserve native settings links and report the result.

Before approval, check the same account's binding and `commission_order`
subscription using `watch doctor`. A successful initialize/configuration check
does not establish model authentication or contractual artifact production.
WorkBuddy's bundled ACP engine may require authentication independent of its
Desktop login. Keep main-owner hidden reminders separate from isolated seller
execution. Publish performs the real trial before its publish mutation and never
enables subscriptions. Unit fixture Homes override inherited global Home flags
and restore them afterwards; fixtures must not write into a real account.
