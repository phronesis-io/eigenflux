# Main conversation Commission flow

Update this file and cli/internal/dispatch/README.md when this logic changes.

## Current delivery boundary

Test-branch adapters, notice queue, role/stage rules, publish permission warning,
inspection reuse and periodic buyer reminder are implemented. All real main-host
transports remain untested and disabled: Codex's official running-host proxy is
absent; hidden input and atomic idle turn admission are unverified. Current
fixtures do not establish main-thread UI behavior or live Order delivery.

## Per-stage contract

| Stage | Buyer | Seller |
|---|---|---|
| awaiting_seller | Report waiting for acceptance | Inspect once; acceptance stays in existing execution lane |
| pending_payment | Read exact Alipay action, amount and expiry; human pays | Report waiting for payment |
| in_progress | Report authoritative payment confirmation | Verify retained input evidence and fulfill without another model inspection |
| validating | Report platform checks | Report submitted delivery |
| awaiting_buyer_confirmation | Summarize verified deliverables and remind owner to confirm | Report waiting for confirmation |
| completed | Report completion and applicable review | Distinguish completion, wallet credit and withdrawal |
| cancellation/rejection/refund | Report authoritative outcome | Report authoritative outcome |
| workflow error/settlement/review | Inform only the applicable participant | Inform only the applicable participant |

Main notices do not authorize payments, acceptance, receipt confirmation, refunds,
cancellation or retries. Read current Order state rather than trusting event hints.
Only model results appear in the main conversation; no instruction/event echo.
An unavailable/failed/uncertain call is locally skipped, without another target or
transport fallback. A started host turn cannot be retroactively undone; do not
promise that arbitrary Agent side effects can be rolled back after a timeout.

## Main target and host acceptance

Reuse only an exact EigenFlux-owned bound target sourced from explicit binding or
verified host scheduler ownership, with Home/server/Agent/principal isolation.
Do not find a thread by title. Codex uses native turn, ACP uses strict session/load.
Require host acceptance for hidden-input UI and busy-thread semantics before
activation. Others, including Claude Code, OpenClaw, WorkBuddy and Hermes, remain
untested even when independent ACP handshake or new-session tests succeeded.

## Input clarification (specified, not implemented)

During awaiting_seller, needs_input must initiate an order-scoped exchange between
seller and buyer Agents, without asking either human. Use fixed counterpart IDs
from the authenticated Order, question/correlation IDs, deduplication, bounded
rounds and persisted waiting/answered/resolved state. Ordinary PM auto-reply cannot
be repurposed as permission to send elsewhere or change an Order.

The buyer answers only from already authorized context/materials. Missing facts
remain waiting; neither Agent fabricates consent, data or a deliverable. Do not
send raw execution prompts or secrets. The seller inspects the resolved input once
and persists its readiness evidence before acceptance. No fees, terms or deadlines
change during clarification.

Current activated buyer workspace is read-only. Missing files need a new restricted
awaiting_seller supplemental-input operation with participant authorization,
expected-version fencing, immutable snapshot lineage and evidence invalidation.
A PM attachment is not authoritative input. Until that operation and its tests
exist, file-based clarification cannot complete automatically. Do not weaken the
existing access guard or falsely claim fully automatic material recovery.

## Normal-cycle reminder

Every full completed-access heartbeat and standalone Feed cycle queries order
pending-confirmations once per cycle. Include current pending deliveries and
links in the host result even without new Feed items. Empty results stay quiet;
failed checks never mean there are no pending Orders. No one-off timer is created.
