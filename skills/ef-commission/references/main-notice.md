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

For buyer updates, briefly summarize newly verified material submissions and
recorded automatic clarification answers: identify the Order, submitted text/file
names, content or answer summary and next step. Reuse existing authorization;
report each submission or answer once. Keep private details out of summaries.

| Current state or result | Buyer result | Seller result |
|---|---|---|
| awaiting_seller | Report that the seller is checking the request | Report inspection progress; leave acceptance to the execution worker |
| pending_payment | Obtain the existing Alipay payment action and show amount, exact link and expiry; payment remains human-operated | Report acceptance and waiting for payment |
| in_progress | Report confirmed payment and waiting for delivery | Report fulfillment progress without repeating input inspection |
| validating | Report platform delivery checks in progress | Report submitted delivery, without claiming buyer acceptance |
| awaiting_buyer_confirmation | Download and inspect the delivery; report the files, contract compliance, defects or verification limits, and acceptance recommendation; show the Order link and request receipt-confirmation authorization | Report waiting for buyer confirmation |
| completed | Report actual completion and any required review | Report completion; distinguish wallet credit and withdrawal |
| rejected, cancelled, refunded | Report the authoritative result | Report the authoritative result |
| refund, settlement or validation processing result | Report the relevant confirmed result or blocker | Report the relevant confirmed result or blocker |
| review created | Do not repeat a review action | Summarize the actual received review |
| buyer material needed | Read the current waiting_buyer question and missing paths; explain what is needed and show the supplied Order link for the buyer to supplement | Leave intake paused until supplementation |
| local execution blocked | Report only the applicable confirmed result | Explain the verified local blocker and current Order state; do not restart work |

For `order.state.changed.v1` with observed state `completed`, verify the
completion transition and participant against the current Order, then apply the
[Commission broadcast flow](../../ef-broadcast/references/publish.md#commission-event-publishing)
in this turn. Use the supplied CLI prefix for all reads and the broadcast. Share
the completion event key with the interactive Order flow. Append the broadcast
result or manual publishing prompt to the same final notice.

For buyer acceptance, list `order files` and download each submitted output at
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

Do not pay, accept, confirm receipt, refund, cancel, modify terms or restart work
from a notice. Never infer payment from a generated link or infer withdrawal from
wallet credit. When the Order cannot be verified, return no business claim.
