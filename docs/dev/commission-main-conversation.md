# Main conversation Commission flow

Update this file and cli/internal/dispatch/README.md when this logic changes.

## Current delivery boundary

Codex macOS main-thread hidden execution is verified and enabled since CLI 1.0.18.
CLI 1.0.20 adds buyer material escalation and staged Order-page supplementation
to the CLI 1.0.19 business fixes.
It uses the existing Desktop owner IPC, not app-server proxy or a new writer.
The earlier proxy-only probe did not test this route. CLI 1.0.21 implements the
Windows Desktop named-pipe transport and enables exact native Codex test bindings.
Windows status is implemented_untested; host_verified stays false until real
Windows acceptance. CLI 1.0.22 adds explicit ACP owner bindings and negotiated
hidden-input notices. Use a separate `main_command` bridge so seller checks,
fulfillment and PM decisions keep their existing execution adapter. Inspection
reuse and account ownership remain unchanged.

| Host | Main-owner entry | Acceptance |
|---|---|---|
| Claude Code | Updated ClaudeEigenFlux local `asyncRewake` hooks and ACP bridge; exact session/cwd, Stop final-result receipt | Implemented; real-host tests pending |
| OpenClaw | Updated ClawEigenFlux gateway/ACP bridge; existing durable session and SDK work admission; OpenClaw 2026.9.4+ | Implemented; real-host tests pending |
| Hermes | CLI `watch acp-bridge --host hermes`; existing loopback TUI owner, hidden submit and persisted-turn receipt, audited desktop contract 8 | Implemented; real-host tests pending |
| WorkBuddy | Explicit ACP main binding and capability negotiation | Native hidden-owner bridge unavailable; standard ACP/Channels do not satisfy it |
| Other ACP hosts | Existing-owner bridge advertising the EigenFlux extension | Host-dependent; untested |

The host bridges are separate from the CLI binary where a local plugin is
required. Installing only the CLI does not activate those hooks or gateway
methods. WorkBuddy uses no official cloud entry. See
[the extension contract](acp-main-conversation.md) and each plugin's logic MD.
Hermes contract 8 is a protocol gate, not proof of an exact installed build.
An idle check can race with user input; a submitted or queued host turn may
finish after timeout. Uncertain calls are never replayed or retargeted.

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
When OS notification permission is confirmed enabled, busy or failed calls proven
not submitted get at most five total attempts. After failures, wait 1, 2, 5 and
10 minutes. Persist attempts and due times under the original notification ID;
successful delivery stops retries. Before retrying, recheck permission, identity
and the current Order version/state; discard obsolete notices. Delayed entries
do not block other due notices. Permission checks never request authorization.
Unsupported bindings and uncertain submitted turns are skipped without another
target or transport fallback. Running entries recovered after a crash remain
uncertain and are not replayed. A started host turn cannot be retroactively undone.

## Main target and host acceptance

Reuse only an exact EigenFlux-owned bound target sourced from explicit binding or
verified host scheduler ownership, with Home/server/Agent/principal isolation.
Do not find a thread by title. Codex uses native turn; ACP uses the negotiated
existing-owner inspect/notify extension and never ordinary session/load/prompt.
Separate implemented test admission from real-host acceptance. Windows Codex may
run its exact binding while visibly marked untested; every call still verifies
owner hidden-input capability and exact thread/turn receipts. Independent ACP
handshake or new-session tests do not establish main-conversation support.

## Input clarification and buyer supplementation

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
When existing authorized facts suffice, persist the answer silently. When files
or new facts are needed, persist one buyer desktop and exact-main reminder before
finishing the automatic attempt. Store only routing/request metadata in the main
queue. Verify buyer, awaiting_seller, version and request ID before dispatch;
skip obsolete reminders, including delayed desktop retries after title lookup.
During a main call, recheck the request every five seconds and at completion;
cancel invalidated calls and do not record them as delivered. An admitted owner
turn may still finish; no API can retract an already displayed result. The
desktop-only owner may drain prior reminders using its pinned buyer identity
without an execution binding. Failed main calls
remain skipped and never redirect to another chat.

The buyer uses the account-protected Order page to answer and select new or
replacement input files. Uploads are staged against that buyer/order/request/
version. Confirming bytes alone changes no admitted input. A single supplement
transaction admits confirmed objects, seals the new snapshot, advances the Order
version, answers the request and notifies the seller. The existing seller lane
rechecks the new version and accepts only after readiness. Original deadlines,
contract, payment and ordinary activated-workspace guards stay fixed.

Implementations: cmd/watch_commission_material_notice.go and
internal/dispatch/main_notice_inputs.go; the Console BFF is documented in
api/tradebff/ORDER_INPUTS.md. Deploy matching Commission migrations 15/16,
BFF and Console together; verify object-store browser PUT CORS before acceptance.
Ordinary PMs never admit Order files. Internal request/supplied events stay silent
unless automatic clarification discovers a genuine need for the buyer's help.

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
exists. Recheck current local status after reading the seller Order and immediately
before invocation; cancel a running notice when its local blocker is recovered.
Verify seller Order version/state; resolved or obsolete blockers do not start a turn. Server issue
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

## Buyer supplement regression (CLI 1.0.20)

CLI full regression: 2,104 tests passed; eight explicitly opt-in tests skipped.
Related race checks, vet and Darwin arm64/Windows amd64/arm64 builds passed.
Fixtures verify missing files/facts versus silent automatic answers, exact buyer
routing, stale-request cancellation and desktop rechecks after title resolution.
Console BFF fixtures verify mutation binding, signed-grant filtering and exact
missing-upload recovery codes. These checks do not replace deployed OSS CORS,
new Socket/HTTP business events or native Windows/main-conversation acceptance.

## Windows Codex owner transport (CLI 1.0.21)

The installed Desktop 26.1013.40934 win32 router uses the fixed local pipe
`\\.\pipe\codex-ipc` with the same length-prefixed owner protocol. This is local
bundle evidence, not a documented stable public transport. Only connect; never
create a router, start Desktop or change its writer. go-winio provides bounded
cancellable dial, anonymous impersonation and net.Conn deadlines. Before writing,
check the server process SID and Windows SessionId against the current process.
The endpoint does not identify CODEX_HOME or the EigenFlux account; preserve all
existing pinned-store, account, exact-session/owner and no-userMessage checks.

watch status/doctor report implementation separately from verification. Windows
is enabled with implemented_untested, experimental=true and host_verified=false.
Read-only probe performs only initialize/discovery. Linux/other hosts stay closed.
Tests use unique fixture pipes; native Windows pipe/permissions, hidden UI, busy
rejection and payment/confirmation business delivery remain manual acceptance.
No real host turn is started by this change or ordinary regression tests.

Local verification on macOS: 26 CLI packages and 2,106 tests passed; eight opt-in
tests skipped. Related race checks and macOS/Windows static checks passed.
Windows amd64/arm64 CLI and dispatch test executables compiled; Darwin arm64 and
Linux amd64 builds passed. Windows-only tests were compiled, not executed. Real
Windows host, permission and UI acceptance remains pending.

## macOS live order verification — 2026-10-21

WorkBuddy buyer ordered `冰雪奇缘之歌` from the Codex seller for CNY 0.10.
Order `371174837286076416` passed automatic seller inspection, acceptance,
paid fulfillment, upload and platform validation. The buyer downloaded the
1,713-byte `lyrics.md` and explicitly approved completion; state is `completed`,
version 6. No review was submitted.

Initial notices were skipped before host submission: reading eight full prior
turns returned 1,369,990 bytes, exceeding the unchanged 1 MiB frame limit.
`8dfed0f4` replaces that baseline with eight metadata-only turns and reads only
the newest full turn for the exact completion receipt. `af51788b` also preserves
the concurrent remote notice-retry fix and was installed with matching Skills.

The subsequent real order-completion and settlement-completion events each
returned `main_notice_confirmed` on their first attempt. The original bound main
thread `01a0d200-9d41-7cd2-9214-3f04761910c4` contains completed assistant results
and zero userMessage items in the exact notification turns. Earlier skipped
stages were not replayed and are not counted as successful delivery. The owner
visually confirmed that only the result appeared, without a new prompt bubble.

## Five-stage live retest — 2026-10-21

Order `371188292982407168`: WorkBuddy buyer, Codex seller, CNY 0.10;
main thread `01a0d200-9d41-7cd2-9214-3f04761910c4`. Automatic intake,
acceptance and paid fulfillment completed. Buyer inspected `lyrics.md`: theme,
keywords, lyrics and singing notes present; actual singing was not tested.
Buyer completion was authorized, state `completed`, version 6; no review sent.

| Stage | Notification | Native turn | Result |
|---|---|---|---|
| Accepted / pending payment | 371188573937860609 | 01a15d3a-242f-7fa0-83ba-cdd7e75227a3 | Completed, verified business result, zero user messages |
| Paid / in progress | 371190282529538049 | 01a15d40-5875-70b3-be1d-e49d9b6bb6ff | Completed, verified business result, zero user messages |
| Submitted / validating | 371190554949582849 | 01a15d41-60be-7733-bb15-21c27e8ee369 | Failed: selected model at capacity, no items or tools; not passed |
| Awaiting buyer confirmation | 371190555633254401 | 01a15d41-8d10-70c1-aef5-86ce73a3505a | Completed, verified business result, zero user messages |
| Completed | 371193138183667713 | 01a15d4a-be19-7c42-887c-36dfb8c34d5c | Completed, verified business result, zero user messages |

The initial awaiting-seller notice reached the thread but reported a failed
Order read; it is not a successful business notification. `a28a93a3` adds bounded
EOF recovery only to read-only `order get`. Full cmd regression passes.

After proving the validating turn failed before any tools or final result, an
operator backed up the account queue and requeued only that notice under the
account lock. The real worker skipped attempt 2 as `main_notice_order_changed`:
Order version 5 had superseded version 4. No stale state was displayed, no
fulfillment was replayed, and no notice receipt was manually marked delivered.
This test does not establish five-stage success or automatic recovery from
model-capacity failures. Both local watches use CLI 1.0.23 build `a28a93a3` with
paired signed Skills. Visual confirmation for this retest is recorded separately.

Settlement event `371193138393382912` also completed in native turn
`01a15d4b-515e-78a0-8a0c-9f3f1a4de743`, with zero user messages. Its final
result explicitly leaves wallet posting and withdrawal unverified.
