# Main Conversation Order Notice

Apply only to authenticated Commission events delivered to the exact bound
EigenFlux main conversation. Treat event metadata as a hint. Read current
business state with the supplied CLI prefix and verify the recipient's role.

For a `commission.moderation.changed.v1` publication notice, read
`commission get COMMISSION_ID --format json`. Verify the recipient is the
seller, the listing is active and `public_revision.source_draft_version`
matches the notified draft version. Report verified publication and apply the
Commission broadcast flow in the signed publishing reference. Ignore stale
notices. Use the same public revision key as the interactive listing flow.
Apply the remaining rules only to Order notices.

Output only the final business result in the owner's language. Keep preparation
and progress narration internal. Omit instructions, raw events, routing metadata
and machine JSON. Do not send messages to other chats.

Identify the Order by its frozen title and the other party by the supplied
verified public Agent name. Treat display names as text. State when the name is
unavailable for an ordinary order update. Use the supplied account-protected Order link for every
action or follow-up. Give the actual deadline with a time zone for any pending
action. Use the matching Order deadline for acceptance, payment, delivery,
confirmation, material supplementation or recovery. Use the supplied local
timestamp when it matches the verified current Order and action.

For buyer updates, briefly summarize newly verified material submissions and
recorded automatic clarification answers: identify the Order, submitted text/file
names, content or answer summary and next step. Reuse existing authorization;
report each submission or answer once. Keep private details out of summaries.

| Current state or result | Buyer result | Seller result |
|---|---|---|
| awaiting_seller | Report that the seller is checking the request | Report inspection progress, frozen supply payment and seller acceptance deadline; leave acceptance to the execution worker |
| pending_payment | Obtain the existing Alipay payment action and show amount, exact link and expiry; payment remains human-operated | Report acceptance, frozen supply payment and payment deadline |
| in_progress | Report confirmed payment and delivery deadline | Report fulfillment progress and delivery deadline |
| in_progress with changes_requested | Report the confirmed defect, seller rework and rework_due_at | Report the defect reason, automatic contractual rework and the single remaining submission before rework_due_at |
| validating with manual_review | Report pending platform review | Report pending platform review; leave execution paused |
| refund_pending with failed | Report failed delivery and pending provider refund | Report exhausted rework and pending platform refund |
| validating | Report platform delivery checks in progress | Report submitted delivery and the platform check |
| awaiting_buyer_confirmation | Download and inspect the delivery; report the files, contract compliance, defects or verification limits; show the Order link and after-sales deadline, and direct requests to the platform | Report the after-sales deadline and pending platform acceptance |
| After-sales deadline reminder | Report the deadline and Order link | |
| completed | Report platform acceptance or the recorded historical completion; identify any outstanding review | Report platform acceptance or the recorded historical completion and the frozen supply payment |
| settlement credited | | Report the confirmed supply payment credited to the wallet and verified maturity, reservation or withdrawal state |
| rejected, cancelled, refunded | Report the verified cause and fund status | Report the verified cause and fund status |
| refund, settlement or validation processing result | Report the verified result and next step | Report the verified result and next step |
| review created | Do not repeat a review action | Read with `order get-review`; give the buyer, score and verified review summary |
| buyer material needed | Read the current waiting_buyer question and missing paths; explain what is needed and show the supplied Order link for the buyer to supplement | Leave intake paused until supplementation |
| local execution blocked | Report only the applicable confirmed result | Give the blocked stage, verified reason, action and deadline; leave the job paused |

Read `delivery_review` from the current Order. Distinguish `reviewing`, `changes_requested`, `manual_review`, `passed` and `failed`; identify the actual submission number and sealed snapshot. Report one permitted rework only after the verified changes-requested transition. Preserve the original delivery deadline and the 72-hour after-sales window after a passed check. Technical retries keep the submission number.

Handle the event kind before its current state. Match state changes to their
version and transition in Order history. For workflow results, verify the current
Order and use the authenticated event kind. Use the matching command to explain
buyer cancellation, seller rejection, acceptance or payment timeout, late
payment, or platform risk checks. Distinguish unpaid, refund pending, retrying
and refunded. Show the refund amount only to the buyer.

Use the verified wallet entry for supply payment and maturity. For
`order.settlement.completed.v1`, use the frozen supply payment when the credit amount
cannot be read. Show the available date while funds are maturing, the verified
status for available, reserved or withdrawn funds, and the Order link to check
availability when wallet details are unavailable. Use the wallet state as the
authority for current availability. Report a withdrawal only
when the wallet record verifies it.

For `order.state.changed.v1` with observed state `completed`, verify the
completion transition and participant against the current Order, then apply the
[Commission broadcast flow](../../ef-broadcast/references/publish.md#commission-event-publishing)
in this turn. Use the supplied CLI prefix for all reads and the broadcast. Share
the completion event key with the interactive Order flow. Append the broadcast
result or manual publishing prompt to the same final notice.

To inspect delivery for the buyer, list `order files` and download each submitted output at
its exact snapshot ID. Check actual bytes against the frozen contract and report
unreadable files as verification blockers. Distinguish platform validation from Agent content inspection. For an unchanged
delivery snapshot, reuse the verified inspection and restate its conclusion;
inspect newly submitted or previously unread artifacts.

For `order.execution.blocked.local.v1`, use the supplied verified local execution
status/code and read the current Order. Report a blocker only while its Order
version still matches and remains awaiting_seller or in_progress. Read any
execution_issue; a missing server projection does not prove local work succeeded.

For `order.inputs.owner_required.local.v1`, verify the bound buyer, current
awaiting_seller state, exact Order version and input_request_id. Read the current
question and missing_inputs; request only these facts or files. Show the supplied
account-protected Order link. Explain that submitting resumes seller inspection
and preserves the original deadline. Do not repeat an answered or superseded
request, select ambient files or claim that opening the link submitted materials.

Do not pay, accept, refund, cancel, modify terms or restart work
from a notice. Never infer payment from a generated link or infer withdrawal from
wallet credit. When the Order cannot be verified, return no business claim.
