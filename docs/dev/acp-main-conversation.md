# ACP main-conversation bridge

Update this file and `cli/internal/dispatch/README.md` when this contract changes.
CLI 1.0.22 implements this experimental client. Real-host acceptance is separate.

## Routing

Bind the exact `host`, canonical `workdir`, opaque `main_session_id` and a
complete ACP bridge argv in `main_command`. Retain the ordinary `mode`,
`command` and `args` for seller input checks, fulfillment and PM decisions.
An existing `mode=acp` command that implements both operations may omit
`main_command`. Keep the existing account,
Home/server/principal/revision isolation. Session-only updates require the old
ID and revision and retain the execution journal. Codex keeps native owner IPC.
ACP PM workers keep their per-conversation sessions; this bridge handles the
separate result-only main-notice lane, not seller execution or PM decisions.

The bridge attaches to an existing host owner. Standard ACP `session/load`
only restores history and does not prove that owner or hide user input.
The client never sends `session/new`, `session/load`, `session/prompt` or
`session/cancel` on this lane. Failure closes only the owned bridge, without
fallback, automatic replay or cancellation of another active host turn.

## Negotiation and receipts

Use ACP v1 newline-delimited JSON-RPC. `initialize.agentCapabilities._meta`
must contain `ai.eigenflux.mainConversation` with `version=1`, the exact `host`
and `existingSession=true`, `hiddenInput=true`, `resultOnly=true`.
The client advertises extension version 1 in `clientCapabilities._meta`.

| Method | Request | Required reply |
|---|---|---|
| `_eigenflux/main/inspect` | `sessionId`, `cwd` | Exact `sessionId`, `host`, `cwd`; `state=idle` or `busy`; all three capability booleans true |
| `_eigenflux/main/notify` | `sessionId`, `cwd`, unique `requestId`, `prompt` | Exact `sessionId` and `requestId`; nonempty `runId`, `text`; `stopReason=end_turn`; all three booleans true |

Inspect is read-only and must not load history, create a session, queue work or
call a model. Check the same target again when admitting notify. The host must
hide the supplied input and display the final answer in that same conversation.
Return only this invocation's final answer, not history or intermediate chunks.
Reject busy targets, invalid/missing sessions and unsupported capabilities.
Do not request interactive permissions from the background client.

Missing capability returns `main_notice_transport_unverified` before business
data is sent. Mismatched targets/receipts and incomplete results are failures.
`watch doctor --probe-main` sends initialize and inspect only. Configuration
availability means negotiation can run; only a successful probe establishes
current reachability. `host_verified` remains false for all non-Codex adapters.
Timeout after host admission is uncertain: an already started turn may finish.

## Host acceptance

Host-specific bridge entry points, dependencies and test boundaries are listed
in `commission-main-conversation.md`. Unit fixtures and cross-compilation do not
establish that a real Desktop/TUI hides input or displays the result. Test macOS
and Windows independently, including reload/history rendering and a busy main
conversation. Unsupported WorkBuddy configurations stay local and unavailable;
there is no official cloud-entry fallback.

This is an EigenFlux extension, not a standard ACP hidden-input feature.
Protocol basis: [ACP extensibility](https://agentclientprotocol.com/protocol/v1/extensibility)
and [session setup](https://agentclientprotocol.com/protocol/v1/session-setup).
