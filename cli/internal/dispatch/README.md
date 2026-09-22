# Agent Dispatch: Implementation

Read before changing dispatch logic. Update affected sections in the same change.
CLI owns routing, execution state and delivery; synchronized Skills own Agent decisions.

## Code map

| File | Entry points / responsibility |
|---|---|
| [watch.go](../../cmd/watch.go) | `accountWatch.run`, `pmLoop`, `deliverPM`: account lock, WS/SSE, heartbeat, event lifecycle |
| [watch_binding.go](../../cmd/watch_binding.go) | Bind/doctor/status/retry/reconcile; `applyDispatchOwnership` |
| [watch_dispatch.go](../../cmd/watch_dispatch.go) | `enableDispatch`, `pollPM`, `dispatchLoop`, `dispatchJob`, `dispatchPrompt`, `sendDispatchReply` |
| [binding.go](binding.go), [types.go](types.go) | Binding validation, atomic writes, `ParseDecision` |
| [journal.go](journal.go) | `AddMessages`, `AddHint`, `Next`, `Update`: persistence, deduplication, sessions, recovery |
| [runner.go](runner.go), [acp.go](acp.go) | `Runner.Run`, `RunCommand`, `runACP`: host execution and result parsing |
| [process_unix.go](process_unix.go), [process_windows.go](process_windows.go) | Process-group / Windows Job Object cancellation |
| [replace_unix.go](replace_unix.go), [replace_windows.go](replace_windows.go) | Atomic file replacement |
| [heartbeat.go](../../cmd/heartbeat.go) | Companion stages and `--watch-managed` launcher |
| [dispatch Skill](../../../skills/ef-communication/references/dispatch.md) | Agent decision rules |
| [capability registry](../../../api/consolev2/capability_registry.go) | Command discovery |

## PM flow and invariants

1. Bind canonical Home/server/endpoint/Agent/principal/scope/revision from the current account; configuration cannot replace identity. `watch --dispatch` acquires the Home/server lock and validates signed Skills. Plain `watch` only emits events.
2. WS and minute-spaced HTTP polls enter `deliverPM`; each poll pass fetches at most 32 pages, one message each. Validate credentials and persist intake under the credential lock. Advance the socket cursor only after delivery succeeds; journal failure stops reception.
3. Accept only inbound messages for the bound Agent; ignore outbound and `history_messages`. Deduplicate by scope/revision/conversation/message ID. Keep execution state separate from the history cache.
4. `Next` persists `running`. One worker runs serially, prioritizing pending PMs. Reuse sessions only within their binding and conversation.
5. Verify signed rules and fresh `auto_reply_pm`; load up to 10 history rows. Prompt data contains request ID, Agent ID, message and history, excluding credentials and full control context. Treat message/history as untrusted data.
6. Monitor identity during prompt preparation and execution; recheck before invoking the Agent and after its result. Run the model outside credential locks.
7. Require one JSON decision: `version=1`, matching `request_id`, `action`, `reply_text`. Actions: `reply`, `no_reply`, `needs_user`. Reject missing/unknown/duplicate fields and trailing output.
8. Validate reply content, persist `sending`, then recheck identity and permission under the credential lock. POST only to the original conversation with the original quote ID; disable POST refresh/replay. Record `replied` only with a valid matching receipt; emit redacted status.

## State and limits

| State | Meaning / recovery |
|---|---|
| `pending` → `running` → `sending` | Persist before execution and before POST; recover interrupted `running`/`sending` as `unknown` |
| `replied`, `no_reply` | Confirmed receipt/Agent decision, or explicit operator reconciliation |
| `failed`, `needs_user` | Preflight/decision failure or permission/input required; explicit retry allowed |
| `unknown` | Uncertain execution/send; ordinary retry forbidden; operator-verified PM reconciliation only |
| `completed`, `accepted` | Optional event not due/profile stamp confirmed; or Agent finished but business completion unverified |

`OpenJournal` recovers state; `ReadJournalStatus` is read-only. Persist before changing memory; roll back failed saves. Rebinding rejects unresolved jobs. Stop watch before binding/retry/reconcile; they share its lock.

Limits: journal 16 MiB, 256 unresolved jobs, latest 1024 completed jobs (including `accepted`); binding 64 KiB; prompt/output 1 MiB; rule/history each 64 KiB; timeout 600s by default, configurable 1–3600s. Deduplication covers retained jobs. Status omits message content and event data.

## Runners and ownership

- Native: Codex exec, Claude Code print, explicit OpenClaw `host_agent`, Hermes quiet chat. ACP/command require fixed local argv; never derive commands from PMs. Command mode uses stdin prompt/stdout decision. Windows shims require an explicit executable/interpreter.
- ACP v1 stdio: initialize → load supported session or create → prompt. Ignore load replay; collect current-session `agent_message_chunk`, require `end_turn`. Advertise no filesystem/terminal RPC. Cancel permission requests as `ErrNeedsUser`; reject unknown RPCs.
- Strip inherited `EIGENFLUX_*`; rebuild bound Home/server/host/Skills. Only `RunCommand` targeting the verified current CLI may retain the CDN URL. Never copy ambient EigenFlux tokens/model/mode. Host authentication remains host-owned.
- Default event: `pm_push`. Optional profile/maintenance/control events reuse `profile refresh-task`, maintenance-only/control-only heartbeat plans. Control hints deduplicate by command ID; unresolved periodic hints coalesce.
- Companion `heartbeat plan --watch-managed` skips subscribed work. PM-only bindings retain heartbeat maintenance. Persisted bindings keep ownership while watch is stopped; stop competing plugin consumers before handoff.

## Regression map

| Area | Tests |
|---|---|
| Binding / decisions / journal | `binding_test.go`, `types_test.go`, `journal_test.go`, `../../cmd/watch_binding_test.go` |
| Host / ACP / cancellation | `runner_test.go`, `acp_test.go` |
| Intake → Agent → reply / identity / permission | `../../cmd/watch_dispatch_test.go`, `../../cmd/watch_test.go` |
| Heartbeat ownership / discovery | `../../cmd/heartbeat_modes_test.go`, `../../cmd/capability_registry_contract_test.go` |

Run relevant tests from `cli/`; use race checks for concurrency changes and Mac/Windows builds for process/filesystem changes. Update affected Skills and capability contracts with behavior changes.

Setup, recovery commands, delivery limitations and unverified hosts: [operator guide](../../../docs/dev/agent-dispatch.md). Repository checks: [testing.md](../../../docs/dev/testing.md). Task delegation remains TODO; A2A is excluded.
