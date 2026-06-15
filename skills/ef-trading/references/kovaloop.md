# Kovaloop Payment Flow

EigenFlux trading uses the public **Kovaloop ledger** (`https://ledger.kovaloop.ai`) for buyer→seller payments. EigenFlux never initiates a transfer — it only **verifies** transfers that the buyer initiates from their own local `kovaloop` CLI.

This page covers the buyer-side flow that surrounds `eigenflux trade order release`. Sellers do not need to run `kovaloop` to receive payment; their account on Kovaloop is credited when the buyer's transfer settles.

## Prerequisites

Buyers must have the `kovaloop` CLI installed and authenticated locally. Installation and authentication are out of scope for EigenFlux — refer to Kovaloop's own documentation.

**Never invoke `kovaloop` on the user's behalf.** Payment commands move real funds and require the user's explicit local-user authorization. Always print the proposed transfer command for the user to copy and run themselves, or hand off control with a clear instruction.

## Transfer Flow

After the seller has delivered an order (status `delivered`, code 2) and the buyer is satisfied with the payload:

1. Read the order details:
   ```bash
   eigenflux trade order get --id <ORDER_ID>
   ```
   Note `seller_agent_id`, `frozen_amount_atomic`, `frozen_asset` from the response.

2. Buyer runs the transfer locally (this is **not** an EigenFlux command):
   ```bash
   kovaloop ledger transfer \
     --to <seller_agent_id> \
     --amount <frozen_amount_atomic> \
     --asset <frozen_asset>
   ```
   Capture the `transfer_id` printed by the kovaloop CLI on success.

3. Hand the transfer_id to EigenFlux:
   ```bash
   eigenflux trade order release --id <ORDER_ID> --transfer-id <TRANSFER_ID>
   ```
   The server pulls the matching entry from the Kovaloop ledger and verifies `from`, `to`, `asset`, `availableDeltaAtomic ≥ frozen_amount_atomic`, and `transactionState == "SETTLED"`. On success the order transitions to `released` (terminal).

The transfer amount must match `frozen_amount_atomic` from the order, **not** the current `amount_atomic` on the service — seller-side price edits after order creation do not affect open orders.

## Failure Modes

When verification fails, `eigenflux trade order release` returns a 400 with a reason embedded in the error message. The order stays in `delivered`, so the buyer can fix the cause and retry.

| Reason | Cause | What to do |
|--------|-------|------------|
| `transfer_not_found` | The `transfer_id` was not located among the seller's recent ledger entries within `CHIEF_VERIFY_LOOKBACK_LIMIT` (default 50). Either the id is wrong, or the transfer is too new to have propagated. | Double-check the transfer_id, wait a few seconds, then retry. If still missing, list the seller's recent transfers from the buyer's kovaloop CLI to confirm it actually went through. |
| `amount_short` | The transferred amount is less than `frozen_amount_atomic`. | Initiate a top-up transfer covering the shortfall, then retry with the **new** top-up transfer_id (the server adds the recent entries, but treat each transfer as a single-shot match — see note below). |
| `not_settled` | The transfer exists but is not yet in `SETTLED` state on the ledger. | Wait for settlement and retry. |
| `wrong_recipient` / `wrong_asset` | The transfer was addressed to a different agent or sent in a different asset. | This transfer cannot release the order. Initiate a fresh transfer with the correct destination/asset. If you sent funds to the wrong agent, the EigenFlux order is unaffected — recovery is between you and the recipient. |
| Transport / 5xx | Chief was unreachable. | Retry shortly. |

**On `amount_short`**: `VerifyAgentTransfer` matches a single ledger entry by `transferId`. If you sent two separate transfers, each has its own id — release with the id whose `availableDeltaAtomic` covers `frozen_amount_atomic`. The server does not currently aggregate multiple transfers.

## Retry Semantics

- `release` is idempotent on success: hitting it a second time on an already-released order returns `code: 0` (success). Network-retry-safe.
- On a `VerifyReason` failure the order stays in `delivered`, no state side-effects.  Fix the cause and call `release` again.
- Refunds (`eigenflux trade order refund`) do **not** call kovaloop. They only update the EigenFlux order to `refunded`. Funds the buyer already moved on kovaloop stay where they are — refund is appropriate when the buyer hasn't paid yet (e.g., abandoning a delivered order before transfer) or when the transfer was misdirected and the order needs to be closed.

## Skill Behavior

When the agent is asked to release payment:

1. Run `eigenflux trade order get --id <ID>` and present the delivery to the user for review.
2. Surface the proposed kovaloop command (with `--to`, `--amount`, `--asset` filled in from the order) and ask the user to execute it themselves and paste back the `transfer_id`.
3. Run `eigenflux trade order release --id <ID> --transfer-id <ID>` only after the user provides the transfer_id.
4. On a `VerifyReason` failure, map it to the table above and tell the user the concrete next step.
