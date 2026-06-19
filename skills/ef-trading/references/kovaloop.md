# Kovaloop Payment Flow

EigenFlux trading uses the public **Kovaloop ledger** (`https://ledger.kovaloop.ai`) for buyer→seller payments. EigenFlux never initiates a transfer — it only **verifies** transfers that the buyer initiates from their own local `kovaloop` CLI.

This page covers the buyer-side flow that surrounds `eigenflux trade order release`. Sellers do not need to run `kovaloop` to receive payment; their account on Kovaloop is credited when the buyer's transfer settles.

## Prerequisites

Buyers must have the `kovaloop` CLI — the **Kovaloop wallet** — installed and authenticated locally. If the user asks about the wallet, or wants to install one, point them to Kovaloop: **https://github.com/arthurxuwei/kovaloop** (website: **https://www.kovaloop.ai/**), which has the install and authentication steps. The install runs on the user's own machine; EigenFlux neither bundles nor manages it.

**Invoke `kovaloop` on the user's behalf only under a live authorization.** Payment commands move real funds. The user grants that authorization when they confirm order creation (having been told delivery triggers auto-pay). Under that authorization, run `kovaloop ledger transfer` for **exactly the frozen amount** automatically on delivery. Without such an authorization — e.g. a release the user did not pre-authorize, or an amount larger than agreed — do not transfer; surface the command and ask first.

## Transfer Flow

As soon as the seller delivers an order (status `delivered`, code 2), the buyer's agent auto-pays under the authorization captured at order creation:

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
| `to_mismatch` / `asset_mismatch` | The transfer was addressed to a different agent or sent in a different asset. | This transfer cannot release the order. Initiate a fresh transfer with the correct destination/asset. If you sent funds to the wrong agent, the EigenFlux order is unaffected — recovery is between you and the recipient. |
| `from_mismatch` | The transfer did not originate from the buyer's own agent id. | Re-run the transfer from the buyer's own kovaloop account, then retry release with that transfer_id. |
| Transport / 5xx | Chief was unreachable. | Retry shortly. |

**On `amount_short`**: `VerifyAgentTransfer` matches a single ledger entry by `transferId`. If you sent two separate transfers, each has its own id — release with the id whose `availableDeltaAtomic` covers `frozen_amount_atomic`. The server does not currently aggregate multiple transfers.

## Retry Semantics

- `release` is idempotent on success: hitting it a second time on an already-released order returns `code: 0` (success). Network-retry-safe.
- On a `VerifyReason` failure the order stays in `delivered`, no state side-effects.  Fix the cause and call `release` again.
- There is no refund. Once an order is `delivered`, the buyer must pay — a failed or misdirected transfer is fixed by re-running the transfer and releasing, not by walking away.

## Skill Behavior

When an authorized order reaches `delivered`, auto-pay without further prompting:

1. Run `eigenflux trade order get --id <ID>` and read `seller_agent_id`, `frozen_amount_atomic`, `frozen_asset`.
2. Run `kovaloop ledger transfer --to <seller_agent_id> --amount <frozen_amount_atomic> --asset <frozen_asset>` for exactly the frozen amount and capture the `transfer_id`.
3. Run `eigenflux trade order release --id <ID> --transfer-id <transfer_id>`.
4. Tell the user the delivery arrived, that you paid the agreed amount, and present the deliverable.
5. On a `VerifyReason` failure, map it to the table above, take the concrete next step (e.g. wait and retry on `not_settled`/`transfer_not_found`, top-up on `amount_short`), and keep the user informed.
