# Agent Dispatch Implementation Map

Read this file before changing CLI outer-loop or local Agent dispatch logic.
Update its affected sections with every logic change in the same change,
including changes that preserve the public API. Keep this document about the
current implementation; use Git history for historical behavior.

## Responsibilities and source map

The CLI owns account routing, intake, durable execution state, host invocation,
and reply delivery. Synchronized Skills own Agent decisions. This package is
part of the independent CLI Go module and does not import server packages.

| File | Main symbols and responsibility |
|---|---|
| [../../cmd/watch.go](../../cmd/watch.go) | `watchCmd`, `accountWatch.run`, `pmLoop`, `deliverPM`, `controlLoop`, `runtimeLoop`, `checkLoop`: account lock, socket/SSE intake, heartbeat, event emission and lifecycle |
| [../../cmd/watch_binding.go](../../cmd/watch_binding.go) | `watchBindingIdentity`, `sameBindingIdentity`, `applyDispatchOwnership`, bind/doctor/status/retry/reconcile commands |
| [../../cmd/watch_dispatch.go](../../cmd/watch_dispatch.go) | `enableDispatch`, `pollPM`, `dispatchLoop`, `dispatchJob`, `dispatchPrompt`, `sendDispatchReply`: intake-to-Agent-to-reply orchestration |
| [binding.go](binding.go) | `Binding.Validate`, `ReadBinding`, `WriteJSON`: strict configuration, identity scope, executable resolution and atomic persistence |
| [types.go](types.go) | Binding/message/job/request/result types; `ParseDecision` validates the Agent's private-message decision |
| [journal.go](journal.go) | `OpenJournal`, `AddMessages`, `AddHint`, `Next`, `Update`, `Retry`, `Reconcile`: durable jobs, deduplication, sessions and recovery |
| [runner.go](runner.go) | `Runner.Run`, `arguments`, `parseNative`, `RunCommand`: native and command entry points, host output, environment and timeout boundaries |
| [acp.go](acp.go) | `runACP`, `acpClient.call`: ACP v1 stdio initialization, sessions, current-turn output and permission handling |
| [process_unix.go](process_unix.go), [process_windows.go](process_windows.go) | Child-process lifecycle: Unix process group or Windows Job Object |
| [replace_unix.go](replace_unix.go), [replace_windows.go](replace_windows.go) | Atomic file replacement for bindings and journal state |
| [../../cmd/heartbeat.go](../../cmd/heartbeat.go) | Companion heartbeat ownership, selected stages and preservation of `--watch-managed` in its launcher |
| [../../../skills/ef-communication/references/dispatch.md](../../../skills/ef-communication/references/dispatch.md) | Dynamically synchronized private-message decision and authorization rules |

The Agent decision contract is
[skills/ef-communication/references/dispatch.md](../../../skills/ef-communication/references/dispatch.md).
Command discovery is registered in
[api/consolev2/capability_registry.go](../../../api/consolev2/capability_registry.go).
Use [the operator guide](../../../docs/dev/agent-dispatch.md) for configuration,
packaging and manual acceptance, and [testing.md](../../../docs/dev/testing.md)
for repository test conventions.

## Private-message execution flow

1. `watch bind` pins the current canonical Home, server, endpoint, Agent ID,
   principal ID, scope and a fresh revision. Configuration cannot select another
   EigenFlux identity. Bindings are stored under Home's `watch/` directory.
2. `watch --dispatch` holds the Home/server account lock, validates the binding
   and signed local Skills, opens the journal, and starts one dispatch worker.
   Plain `watch` keeps event delivery without running an Agent.
3. WebSocket `pm_push` and `pmFallbackLoop` converge on `deliverPM`. The fallback
   waits one minute between poll passes; each pass pulls at most 32 pages with
   `limit=1`. Credential validation and durable message registration happen
   under the credential lock. The socket cursor advances after delivery succeeds.
4. `AddMessages` accepts only messages addressed to the bound Agent from another
   sender. It ignores `history_messages` and deduplicates by scope, binding
   revision, conversation ID and message ID. Cache storage remains separate.
5. `Next` persists `running` before execution. One binding executes serially;
   pending PM jobs take priority over pending optional events. Existing host
   plugin scheduling is outside this worker.
6. `dispatchPrompt` verifies current signed Skills, reads the dispatch rule,
   checks fresh `auto_reply_pm`, and loads up to 10 conversation-history rows.
   The prompt carries request ID, Agent ID, message and history. It does not
   carry the full control context or credentials. Message/history content is
   untrusted data, not a source of command arguments or authorization.
7. `dispatchJob` monitors identity while preparing and running the task, checks
   again before invoking the Agent and after its result, and reuses only the
   journal session for that conversation. The model runs outside credential locks.
8. `ParseDecision` requires one UTF-8 JSON object containing `version=1`, the
   matching `request_id`, `action` and `reply_text`. Allowed actions are `reply`,
   `no_reply` and `needs_user`. Unknown/duplicate fields and trailing output fail.
9. For `reply`, validate message content and persist `sending`. Under the
   credential lock, recheck identity and fresh automatic-reply permission, then
   POST to the original `conv_id` with the original `quote_msg_id`. Disable
   credential-refresh replay for this POST. Record `replied` only after a valid
   message ID and matching conversation receipt. Emit redacted dispatch status.

## State, persistence and recovery

| State | Meaning and next action |
|---|---|
| `pending` | Persisted intake; eligible for the serial worker |
| `running` | Claimed before prompt/model work; recovery changes it to `unknown` |
| `sending` | Reply intent persisted before POST; recovery changes it to `unknown` |
| `replied` | Reply receipt confirmed, or operator explicitly reconciled its ID |
| `no_reply` | Agent decided no response is needed, or operator reconciled that outcome |
| `needs_user` | Automatic replies disabled, permission denied, or Agent requested owner input; explicit retry allowed |
| `failed` | Recorded preflight/decision failure; explicit retry allowed |
| `unknown` | Execution/send outcome uncertain; never automatically replay or allow ordinary retry |
| `completed` | Optional event was not due, or a profile completion stamp confirmed its outcome |
| `accepted` | Optional-event Agent turn finished without independently verified business completion |

`OpenJournal` performs crash recovery; `ReadJournalStatus` never rewrites state.
`watch reconcile` requires `--verified` and handles unknown PM jobs only, with
`replied` plus a reply ID or `no_reply`. It records the operator's conclusion;
it does not independently prove the remote outcome. Stop watch before mutation
commands acquire its account lock. Rebinding rejects unresolved old jobs.

The journal is limited to 16 MiB, 256 unresolved jobs and the latest 1024
completed jobs. Completed outcomes include `accepted`; deduplication retains all
unresolved jobs and that completed window. Mutations atomically persist before
updating in-memory state; save failures roll back. Status hides message content
and event data. Dispatch intake persistence failure terminates reception rather
than advancing the socket cursor and continuing silently.

## Hosts, protocol and environment

Native adapters invoke Codex `exec`, Claude Code print mode, OpenClaw's explicit
`host_agent`, or Hermes quiet chat. ACP and command modes require explicit local
argv; the runner never derives an executable or recipient from a PM. Command
mode uses stdin for the prompt and stdout for the decision. Windows shell shims
must be replaced by an explicitly configured executable/interpreter and script.

ACP v1 uses newline-delimited JSON-RPC: initialize, load a supported prior
session or create a session, then prompt. Ignore history replay during load;
collect only current-session `agent_message_chunk` text during prompt and require
`end_turn`. Do not advertise filesystem/terminal RPC capabilities. Cancel
permission requests and return `ErrNeedsUser`; reject unknown RPC requests.

Binding JSON is limited to 64 KiB. Prompt/output limits are 1 MiB; the dispatch
rule and history response each have a 64 KiB limit. The default bound timeout is
600 seconds, configurable from 1 to 3600. Cancellation terminates the managed
process group/Job Object. Windows process behavior still needs native acceptance.

Remove inherited `EIGENFLUX_*` and rebuild only bound Home/server/host/Skills
values. Binding environment cannot override EigenFlux variables. `RunCommand`
may additionally retain the CDN URL only after verifying the executable is the
current CLI. Do not copy ambient tokens, model or mode into the child environment.
The host's own authentication/permission configuration remains its responsibility.

## Optional events and ownership

Only PM is subscribed by default. Explicit subscriptions reuse existing CLI
plans: `profile_review_due` calls `profile refresh-task`; `maintenance_due` calls
`heartbeat plan --maintenance-only`; `control_pending` calls the control-only
plan. Control hints use command IDs; unresolved periodic hints coalesce.

The companion `heartbeat plan --watch-managed` skips only subscribed dispatch
work. A PM-only binding leaves maintenance on heartbeat; subscribing maintenance
moves that work to dispatch. A persisted binding retains ownership when its
foreground process is down. Stop competing plugin consumers before handoff.
Agent-turn completion alone must not be reported as business completion.

## Regression map and remaining boundaries

| Change area | Relevant tests |
|---|---|
| Binding, strict JSON and decision identity | `binding_test.go`, `types_test.go`, `../../cmd/watch_binding_test.go` |
| Persistence, crash recovery, deduplication, limits and sessions | `journal_test.go` |
| Host invocation, environment and descendant cancellation | `runner_test.go` |
| ACP sessions, permissions, framing, limits and cancellation | `acp_test.go` |
| Socket + poll + Agent + reply, identity switch and permission revocation | `../../cmd/watch_dispatch_test.go`, `../../cmd/watch_test.go` |
| Heartbeat ownership and compatibility | `../../cmd/heartbeat_modes_test.go`, `../../cmd/capability_registry_contract_test.go` |

Run relevant tests from `cli/`; include race checks for lifecycle/concurrency
changes and Mac/Windows builds for process or filesystem changes. Keep operator
instructions, Skills contracts and capability registration synchronized when
their behavior changes. Link moved symbols and new regressions here in the same
change; do not treat documentation updates as a later task.

The server marks fetched messages read before local journal persistence. The
crash/write-failure window is not closed by this implementation; it is not a
transactional inbox or an exactly-once guarantee. WorkBuddy local identity is
unverified and its cloud API is excluded. Task delegation remains TODO. Fixture
tests and cross-compilation do not establish live-model, Windows-machine or
Computer Use acceptance. See the operator guide for the manual verification scope.
