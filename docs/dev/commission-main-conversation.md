# Main conversation Commission flow

Update this file and cli/internal/dispatch/README.md when this logic changes.

## Current delivery boundary

Codex macOS main-thread hidden execution is verified and enabled since CLI 1.0.18.
CLI 1.0.19 fixes uploaded-input clarification, auxiliary notice overflow, local
blocker delivery, Feed reminder contracts and pre-publish permission guidance.
It uses the existing Desktop owner IPC, not app-server proxy or a new writer.
The earlier proxy-only probe did not test this route. Windows, Claude Code,
OpenClaw, WorkBuddy, Hermes and other ACP main-thread transports remain untested
and disabled. Inspection reuse and host ownership remain unchanged.

Desktop 26.1013.40934 / Codex 0.163.0-alpha.10.2 accepted empty input plus hidden
application/untrusted context. The real Go runner completed in 3.614 seconds,
returned the exact new turn's final result and persisted zero user messages.
The owner confirmed no prompt bubble appeared. A separate controlled test
confirmed a second invocation was rejected while the first ran, with one new
turn only. Desktop's injection/start sequence still has a concurrent-input
window; it is not an atomic idle-only API. Uncertain calls are never replayed.

watch doctor --probe-main now uses only initialize and exact owner discovery.
It does not send a prompt, start a turn, start a daemon or change the target.
owner_ready is reachability, not idle status or a completed business result.

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

## Input clarification (text implemented)

During awaiting_seller, seller needs_input creates request-inputs. Commission
stores the question and waiting_buyer status, advances the Order version and
notifies the fixed buyer. Its separate CLI answer lane uses a signed rule and
the existing fresh-session adapter. An answered result triggers supplement-inputs
only after identity/version checks. The server appends the factual answer to
buyer_input, stores answered, seals a snapshot and notifies the seller to recheck
the new version. Pending questions block acceptance. One pending question and at
most three rounds prevent loops. Original deadlines remain unchanged.

The buyer answers from supplied Order buyer_input and verified uploaded input
files, including inputs/request.txt used by normal Order creation. The dedicated
buyer reader validates the current manifest, participants, immutable grants,
paths, count, size and hashes, then provides private read-only temporary copies.
Recheck the Order after download and remove the copies after execution. This lane
does not read prior chats, arbitrary local files or account control context.
Missing facts
remain waiting; neither Agent fabricates consent, data or a deliverable. Do not
send raw execution prompts or secrets. The seller inspects the resolved input once
and persists its readiness evidence before acceptance. No fees, terms or deadlines
change during clarification. Ordinary PM auto-reply does not gain Order writes.
The two coordination events remain silent in desktop/main notices. A
notifications-only consumer leaves them unacknowledged for an execution owner.
The matching Commission test branch and migration 15 are required; this commit
does not deploy them.

Current activated buyer workspace is read-only. Missing files need a new restricted
awaiting_seller supplemental-input operation with participant authorization,
expected-version fencing, immutable snapshot lineage and evidence invalidation.
A PM attachment is not authoritative input. Text clarification does not implement
that file operation; requests for new files remain waiting. Do not weaken the
existing access guard or falsely claim fully automatic material recovery.

## Normal-cycle reminder

Every full completed-access heartbeat and standalone Feed cycle queries order
pending-confirmations once per cycle. Include current pending deliveries and
links in the host result even without new Feed items. Empty results stay quiet;
failed checks never mean there are no pending Orders. No one-off timer is created. The same rule is present in the local Feed rules,
mandatory contract and served static copy, overriding Feed silence only for
current pending confirmations.

## Verification (2026-10-20)

TestMainNoticeInstalledCodexHiddenTurn uses the actual bound main thread, Desktop
owner and production Go runner. Hidden fixture arithmetic returned 42 in 3.614
seconds; exact final/thread/turn matched and no userMessage existed. Direct owner
probes and the owner's visual confirmation independently passed. The controlled
busy probe rejected its second request and left only the first completed turn.
No live Order, payment or delivery was created or changed by these tests.

TestCommissionNativeCodexTextClarification separately used fresh buyer/seller
sessions (17.25/20.39 seconds) with localhost Order fixtures. It does not establish
main-thread access. Commission 75908d2 passed real PostgreSQL persistence,
transaction, notification and migration tests. Deploy that matching backend and
migration 15 before exercising text clarification against the service.

## Local blockers and publisher permission

Existing seller blockers enter the main-notice worker once per scope/job/order-version/status/
code, using the actual execution version. Retain the receipt while the blocker
exists. Recheck current local status and seller Order version/state before
invocation; resolved or obsolete blockers do not start a turn. Server issue
projection may lag the verified local failure. Report only; never restart work.
Explicit main-notice capacity overflow cannot stop business intake or ACK.

Before publication approval, Skills independently run notify status
--publish-preflight --format json and display any warning, settings link and
manual path. Preserve existing approval and retain publish stderr as fallback.
The read-only check does not request permission, show notifications or upload
telemetry; unknown permission remains unknown.

## Business regression (CLI 1.0.19)

Fixtures cover normal uploaded request.txt with empty legacy buyer_input, buyer
manifest/grant/hash/identity rejection, notification overflow without lost business
ACK or desktop delivery, blocker deduplication/restart/retention/resolution and
actual execution-version routing. Contract tests cover empty Feed reminders and
permission guidance before publication. These tests do not establish new live
payment or delivery-confirmation acceptance.
