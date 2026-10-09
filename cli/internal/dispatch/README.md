# Agent Dispatch: Implementation

Read before changing dispatch logic. Update affected sections in the same change.
CLI owns routing, execution state and delivery; synchronized Skills own Agent decisions.

## Code map

| File | Entry points / responsibility |
|---|---|
| [watch.go](../../cmd/watch.go) | `accountWatch.run`, `pmLoop`, `deliverPM`: account lock, WS/SSE, heartbeat, event lifecycle |
| [watch_binding.go](../../cmd/watch_binding.go) | Bind/doctor/status/retry/reconcile; `applyDispatchOwnership` |
| [watch_commission.go](../../cmd/watch_commission.go) | Commission notification intake, durable enqueue before ACK, bounded HTTP reconciliation |
| [watch_commission_intake.go](../../cmd/watch_commission_intake.go), [watch_commission_materials.go](../../cmd/watch_commission_materials.go) | Seller inspection, fixed identity/order/Skill, verified downloads, atomic paid-work enqueue |
| [watch_commission_fulfillment.go](../../cmd/watch_commission_fulfillment.go), [watch_commission_artifacts.go](../../cmd/watch_commission_artifacts.go), [commission_fulfillment.go](commission_fulfillment.go) | Separate paid local work, strict decision/evidence, retained outputs and recovery |
| [watch_dispatch.go](../../cmd/watch_dispatch.go) | `enableDispatch`, `pollPM`, `dispatchLoop`, `dispatchJob`, `dispatchPrompt`, `sendDispatchReply` |
| [binding.go](binding.go), [types.go](types.go) | Binding validation, atomic writes, `ParseDecision` |
| [journal.go](journal.go) | `AddMessages`, `AddHint`, `AddCommissionNotification`, `Next` / `NextKind`, `Update`: persistence, deduplication, sessions, recovery |
| [runner.go](runner.go), [acp.go](acp.go) | `Runner.Run`, `RunCommand`, `runACP`: host execution and result parsing |
| [process_unix.go](process_unix.go), [process_windows.go](process_windows.go) | Process-group / Windows Job Object cancellation |
| [replace_unix.go](replace_unix.go), [replace_windows.go](replace_windows.go) | Atomic file replacement |
| [heartbeat.go](../../cmd/heartbeat.go) | Companion stages and `--watch-managed` launcher |
| [dispatch Skill](../../../skills/ef-communication/references/dispatch.md) | Agent decision rules |
| [capability registry](../../../api/consolev2/capability_registry.go) | Command discovery |

## PM flow and invariants

1. Bind canonical Home/server/endpoint/Agent/principal/scope/revision from the current account; configuration cannot replace identity. `watch --dispatch` acquires the Home/server lock and validates signed Skills. Plain `watch` only emits events.
2. Only bindings owning PM open its WS and HTTP consumers; other bindings leave PM to the companion. WS and minute-spaced HTTP polls enter `deliverPM`; emitted `pm_push.data` adds locally assigned `transport` (`socket` / `http_poll`) and UTC `received_at`, including empty polls. These fields describe reception, not successful execution or first intake; journal deduplication remains unchanged. Each poll pass fetches at most 32 pages, one message each. Validate credentials and persist intake under the credential lock. Advance the socket cursor only after delivery succeeds; journal failure stops reception.
3. Accept only inbound messages for the bound Agent; ignore outbound and `history_messages`. Deduplicate by scope/revision/conversation/message ID. Keep execution state separate from the history cache.
4. `Next` persists `running`. One worker runs serially, prioritizing pending PMs. Reuse sessions only within their binding and conversation.
5. Verify signed rules and fresh `auto_reply_pm`; load up to 10 history rows. Prompt data contains request ID, Agent ID, message and history, excluding credentials and full control context. Treat message/history as untrusted data.
6. Monitor identity during prompt preparation and execution; recheck before invoking the Agent and after its result. Run the model outside credential locks.
7. Require one JSON decision: `version=1`, matching `request_id`, `action`, `reply_text`. Actions: `reply`, `no_reply`, `needs_user`. Reject missing/unknown/duplicate fields and trailing output.
8. Validate reply content, persist `sending`, then recheck identity and permission under the credential lock. POST only to the original conversation with the original quote ID; disable POST refresh/replay. Record `replied` only with a valid matching receipt; emit redacted status.

## Commission input inspection

`commission_order` explicitly enables durable intake and a separate seller inspection worker. It also queues paid local fulfillment after a ready check. After actual Agent readiness it accepts awaiting-seller orders; after paid fulfillment it uploads verified contractual artifacts and submits delivery. Subscription alone does not prove host execution readiness or establish a buyer SLA.

With PM subscribed, notifications reuse its socket; Commission-only uses an initial HTTP pull and 60-second reconciliation because the server socket fetches PM on connect. Preserve before ACK, at most 50 IDs per ACK, and deduplicate order/version/role within the pinned binding. Notification cursors never replace PM cursors. Legacy stream respects persisted ownership; binding changes and legacy read/ACK share the credential lock.

`Next` excludes both Commission kinds; `NextKind` serializes their separate workers. Buyer notifications are recorded without invoking a seller Agent. The worker re-reads seller identity, order state, frozen contract and fulfillment Skill. Only `awaiting_seller`, `pending_payment` and `in_progress` allow inspection. Read-only preparation and execution share a local 180-second ceiling, excluding queue time; the host's shorter timeout still applies.

Official dispatch rules must be signed manifest members, read through `skills.ReadSignedFile`; third-party fulfillment Skills remain explicitly bound local content. Fetch the authenticated V2 order-detail input manifest, then each immutable snapshot/path grant using pinned credentials. Check manifest role/version/buyer, SHA-256 and byte count before passing local files to the Agent. Inputs use their own snapshot IDs, not the current order snapshot. Bound downloads to 128 files / 64 MiB total; keep URLs and credentials out of prompts. The public endpoint requires the exact GET detail Caddy route.

Use a fresh Agent session. Require matching request/order/version and `ready`, `needs_input`, `needs_user` or `failed`, a brief summary and inspected logical paths. `ready` must cover all supplied verified inputs; required materials cannot be empty. Recheck identity and unchanged order. For awaiting-seller `ready`, POST seller-authenticated acceptance with expected version and deterministic key; uncertainty becomes `unknown`, without automatic replay. Atomically store `commission_result`. Preserve the result when completed notification bodies are compacted. Reuse actual non-failed checks only for the same latest order version; explicit retry rechecks. Interruptions remain `unknown`; do not replay automatically. Clean up owned temporary downloads after the run. Results are local status data, not user-visible or buyer-visible receipts.

## Paid local fulfillment

`CompleteCommissionIntakeAndQueue` atomically stores a `ready` check and queues `commission_fulfillment` only when the caller freshly observed `in_progress`. Its payload retains the checked result; source-result compaction cannot erase readiness evidence. Deduplicate by binding scope/revision/order, including across notification versions. A new notification rechecks an older development-build `ready` when no fulfillment record exists. Historical orders are not bulk-scanned.

The separate fulfillment worker re-reads the same paid order version, resolves signed `fulfillment-dispatch.md`, and verifies freshly downloaded inputs against the checked logical paths. It uses the binding's host timeout, not the intake budget. Poll order state every 10 seconds independently of one-second local identity checks; a blocked network read cannot delay identity cancellation. Changed identity/order or interrupted execution stops work without automatic replay.

Persist a new private output directory inside the bound WorkDir before invoking a fresh Agent session. Skills perform the frozen procedure, write new local deliverables and self-check contractual conditions. CLI requires a matching structured decision, then verifies all declared regular files through a confined filesystem root, rejects symlinks/traversal and duplicates, and computes SHA-256/byte counts from bounded reads. Limits: 128 outputs / 64 MiB. Portable validation cannot prove hard-link origin. Models may only claim local completion; unsupported tools, permissions or external effects require user action.

Preserve output directories, including partial/unknown work; temporary input copies are removed. Persist exact output paths, checks and hashes. For `artifacts_ready`, freeze verified output bytes into private upload copies, persist `sending` with artifact evidence, upload/confirm under fixed keys, and submit the manifest with expected order version. Read back a later validating/awaiting-buyer/completed state before `commission_delivery_confirmed`. Uncertain writes remain `unknown` and require reconciliation. Buyer acceptance stays separate. Retry uses a new output directory and retains old files; unknown execution requires operator reconciliation. Order-level deduplication follows the journal retention window; manually completed records eventually trimmed from the latest 1024 records are not permanent tombstones. Recheck order and bytes before any later submission.

Native/command reuse the existing Runner. ACP Agents need their own usable filesystem tools; this client supplies no filesystem/terminal RPC. Unit fixtures or cross-compilation do not establish a host's ability to produce artifacts. The opt-in `EIGENFLUX_COMMISSION_NATIVE_SMOKE=1 go test ./cmd -run TestCommissionNativeCodexLocalFulfillment -v` uses authenticated Codex with isolated fixture orders and can consume model usage. On 2026-09-28, real Codex inspection and file generation passed in 35.4s and 83.9s; this does not verify real Commission service orders or the buyer deadline.

## State and limits

| State | Meaning / recovery |
|---|---|
| `pending` → `running` → `sending` | Persist before execution and before POST; recover interrupted `running`/`sending` as `unknown` |
| `replied`, `no_reply` | Confirmed receipt/Agent decision, or explicit operator reconciliation |
| `failed`, `needs_user` | Preflight/decision failure or permission/input required; explicit retry or operator-verified reconciliation allowed |
| `unknown` | Uncertain execution/send; ordinary retry forbidden; operator-verified reconciliation only; PM: `replied/no_reply`; optional event: `completed/failed` |
| `completed`, `accepted` | Optional event not due/profile stamp confirmed/operator verified; `accepted` is a legacy unverified terminal state |

Only `unknown/failed/needs_user` allow verified reconciliation; active and completed jobs do not. Unverified optional-event results remain `needs_user`. Reconciliation to `failed` requires confirmation that work did not complete; it never queues a retry.

`OpenJournal` recovers state; `ReadJournalStatus` is read-only. Persist before changing memory; roll back failed saves. Rebinding rejects unresolved jobs. Stop watch before binding/retry/reconcile; they share its lock. `watch retry --order-id ORDER_ID` recovers one missed seller intake from authoritative order detail only in `awaiting_seller` or `in_progress`. It requires the `commission_order` subscription, preserves the ordinary order/version/role dedup key, records `operator_retry`, and never synthesizes or acknowledges a notification. Existing fulfillment records and uncertain intake executions require recovery by job ID. Status and doctor expose `commission_subscription`; a PM-only binding reports `disabled_orders_not_consumed`.

Limits: journal 16 MiB, 256 unresolved jobs, latest 1024 completed jobs (including `accepted`); binding 64 KiB; prompt/output 1 MiB; rule/history each 64 KiB; timeout 600s by default, configurable 1–3600s. Completed jobs drop message content/event data on save, including legacy records; deduplication keys and receipts remain. Status omits payloads.

## Runners and ownership

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
| Intake → Agent → reply / identity / permission | `../../cmd/watch_dispatch_test.go`, `../../cmd/watch_test.go` |
| Commission intake / inspection / materials / ownership | `journal_commission_test.go`, `commission_intake_test.go`, `commission_fulfillment_test.go`, `../../cmd/watch_commission*_test.go`, `../../cmd/order_notifications_test.go`, `../skills/read_signed_test.go` |
| Heartbeat ownership / discovery | `../../cmd/heartbeat_modes_test.go`, `../../cmd/heartbeat_migration_test.go`, `../../cmd/capability_registry_contract_test.go` |

Run relevant tests from `cli/`; use race checks for concurrency changes and Mac/Windows builds for process/filesystem changes. Update affected Skills and capability contracts with behavior changes.

Setup, recovery commands, delivery limitations and unverified hosts: [operator guide](../../../docs/dev/agent-dispatch.md). Repository checks: [testing.md](../../../docs/dev/testing.md). Buyer-facing receipts remain separate; A2A is excluded.
