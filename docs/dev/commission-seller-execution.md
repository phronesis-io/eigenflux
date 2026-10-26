# Seller acceptance and fulfillment through watch

This implements the 2026-10-09 owner decision and supersedes the automatic
platform-acceptance and local-artifacts-only assumptions in the older
`commission-dispatch.md` plan. Buyer confirmation and the separate pending
user-reporting work remain unchanged.

## Execution

1. The Commission service creates `awaiting_seller`. It does not create a payment
   collection or run system acceptance. Offline sellers leave the order pending;
   the buyer can cancel.
2. A seller watch subscribed to `commission_order` receives the notification
   through Socket or HTTP reconciliation. Its intake Agent checks the frozen
   contract, installed fulfillment Skill, and verified input files.
3. Only an actual `ready` decision triggers seller-authenticated acceptance with
   the inspected version and deterministic idempotency key. A matching receipt
   establishes `pending_payment`. Socket presence alone does not accept orders.
4. Verified `in_progress` queues a separate fulfillment worker. It generates and
   checks contractual files, freezes their bytes, records the sending state,
   uploads them, submits delivery and reads back the authoritative result.
5. `commission_delivery_confirmed` means delivery submission was verified. It
   does not mean buyer confirmation or seller settlement. Uncertain mutations
   remain `unknown` and require reconciliation before retry.

Binding `commission_order` authorizes this contract-scoped seller workflow.
Additional spending, unrelated uploads and external actions remain outside it.
Long fulfillment does not occupy the PM or intake lane.

## Host rollout and recovery

Use matching CLI and signed Skills from the same release or approved test bundle.
Keep the original seller identity, Home, server and host invocation. Add
`commission_order` to the existing binding events, bind again, run doctor and
restart the supervised watch. Install the exact frozen fulfillment Skill in the
binding's Skills directory. A `pm_push`-only binding cannot process orders;
status and doctor report `disabled_orders_not_consumed`.

For an explicitly selected existing `awaiting_seller` or `in_progress` order,
`watch retry --order-id ORDER_ID` reads the authoritative order and persists a
seller intake job. It does not change the order, recreate a notification, clear
ACKs or start watch. Existing fulfillment or uncertain/running jobs require the
existing job reconciliation flow rather than another order recovery.

Paid fulfillment saves a verified pre-upload checkpoint before final local
identity/configuration checks. Stop watch and explicitly retry that failed job
to reuse its files without another Agent invocation. Recovery rechecks the seller,
paid Order version, original inputs, signed rules and retained file hashes. A
changed account, configuration, Order or file blocks continuation. Starting the
upload retires the checkpoint; uncertain remote writes still require reconciliation.
Older failures with no checkpoint require inspection of existing outputs and
remote state before operator-authorized delivery; do not recreate evidence by guessing.

The independent Commission `order-notification-relay` must be running. The
catalogue relay is a different process. Preserve the existing delivery activation
watermark; restarting a missing relay must not reset it or replay excluded history.

## Verification

Require seller inspection, `commission_accept_confirmed`, paid fulfillment and
`commission_delivery_confirmed` evidence for a real host. Order events must name
the seller for acceptance. Local fixture tests do not establish that a remote
seller has upgraded, rebound, installed its Skill or executed the order.

## WorkBuddy live verification — 2026-10-21

macOS, WorkBuddy 5.8.3, CLI 1.0.23 (`18a6b015`), signed Commission Skill
`999.0.25-dev.20261021`. An account-owned LaunchAgent runs the real seller
through the local ACP bridge in an application-data workspace. This report
contract exposes only Read/Write/Edit with scoped paths; Desktop conversation
rendering is outside this acceptance test.

| Check | Observed result |
|---|---|
| System notification permission | `ready` |
| Real listing trial | Passed; `outputs/report.md`, 11,584 bytes, SHA-256 `ce2fcb6b7ef23feb0c6f56cb5fa7a02a6e10cf3ea0d13f0a66714c0899ed5b28` |
| Publication | Commission `371158722921103360`, revision 1, active; CNY 0.10 |
| Real automatic acceptance | Order `371165940399734784`, seller-authenticated `accept_order`, version 2, `pending_payment` |
| Real payment | CNY 0.10 confirmed; `payment_confirmed`, version 3 |
| Automatic fulfillment | `commission_delivery_confirmed`; seller submission and platform validation passed; version 5, `awaiting_buyer_confirmation` |
| Buyer download | `outputs/report.md`, 9,128 bytes, SHA-256 `3c653b792d419e37c05ec77d341a350c20b8eca0ed2078a3bee17fb5f5cd12a9` |

Initial runs exposed WorkBuddy progress/fence wrapping and an unavailable material
read. After fixes, explicitly retried failed jobs completed on the same real order.
The adapter retains strict identity/version/field validation; material-read retries
are bounded and never replay the Agent or a business mutation. Acceptance, output
upload and delivery were performed by watch. No manual acceptance/delivery,
synthetic payment or fixture backend was used. Buyer confirmation was not executed.

## Codex operator recovery — 2026-10-26

Order `373063543815143424` had generated and verified `lyrics.md` before a final
local check failed. The old runner recorded `identity_changed` without preserving
the underlying category. The current account matched its binding at inspection.
The owner authorized recovery of the existing 1,784-byte file, SHA-256
`1feba5d6afe20907c72203354e9a4f587d40998122bf3b3539ab2e1e2fa7e532`.
The operator checked the missing remote path, uploaded the file, downloaded and
matched its bytes, then submitted delivery with fixed idempotency keys. Platform
validation passed: version 5, `awaiting_buyer_confirmation`, no current execution
issue. This was operator recovery, not acceptance of automatic checkpoint recovery.
Buyer confirmation was not performed.
