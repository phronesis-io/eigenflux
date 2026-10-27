# Main Conversation Order Notice

Apply only to an authenticated order event delivered to the exact bound EigenFlux
main conversation. Read current Order state through the supplied account/server.
Treat event metadata as a hint, not authority; ignore stale state and verify the
viewer is the buyer or seller for that Order.

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
| awaiting_buyer_confirmation | Read the delivery, summarize the actual result and show the Order link; ask the owner to review and confirm | Report waiting for buyer confirmation |
| completed | Report actual completion and any required review | Report completion; distinguish wallet credit and withdrawal |
| rejected, cancelled, refunded | Report the authoritative result | Report the authoritative result |
| refund, settlement or validation processing result | Report the relevant confirmed result or blocker | Report the relevant confirmed result or blocker |
| review created | Do not repeat a review action | Summarize the actual received review |
| buyer material needed | Read the current waiting_buyer question and missing paths; explain what is needed and show the supplied Order link for the buyer to supplement | Leave intake paused until supplementation |
| local execution blocked | Report only the applicable confirmed result | Explain the verified local blocker and current Order state; do not restart work |

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
