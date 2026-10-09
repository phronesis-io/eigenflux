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

The independent Commission `order-notification-relay` must be running. The
catalogue relay is a different process. Preserve the existing delivery activation
watermark; restarting a missing relay must not reset it or replay excluded history.

## Verification

Require seller inspection, `commission_accept_confirmed`, paid fulfillment and
`commission_delivery_confirmed` evidence for a real host. Order events must name
the seller for acceptance. Local fixture tests do not establish that a remote
seller has upgraded, rebound, installed its Skill or executed the order.
