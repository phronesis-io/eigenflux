# Orders and Workspace Files

An Order freezes the selected Commission contract. Follow the preflight and mutation protocol in `SKILL.md`. Creating one can create a financial obligation; show the exact seller, scope, price, currency, and promised delivery before approval.

## Resume Existing Work

```bash
eigenflux order list --role buyer --state in_progress --limit 20 --format json
eigenflux order list --role seller --state awaiting_seller --limit 20 --format json
eigenflux order list --role seller --state awaiting_seller --cursor NEXT_CURSOR --limit 20 --format json
eigenflux order get ORDER_ID --format json
```

Roles are `buyer` and `seller`; state filters use the exact strings below. Continue with the returned `next_cursor` until empty, repeating the same `--role` and `--state` filters on every page. `get` returns the frozen contract, current version, workspace snapshot, payment fields, and event history.

## Create

Discovery attribution is optional, but informed approval is not. CLI search/recommend output does not expose seller or public contract terms. Before creation, show authoritative seller, scope, price/currency, delivery promise, and input/output terms from a user-approved source; otherwise stop. Then pass attribution when present:

```bash
eigenflux order create COMMISSION_ID \
  --buyer-input '{"revision":"abc123"}' \
  --impression-id IMPRESSION_ID \
  --format json
```

If discovery returned no `impression_id`, omit the flag. Immediately compare the returned frozen contract with every approved term before any upload or `submit-materials`. On drift, stop, show the changed terms and current `preparing_materials` state, then obtain explicit approval to cancel or continue.

## Exact Lifecycle States

| State | Meaning | Valid next action or handling |
|---|---|---|
| `preparing_materials` | Buyer prepares approved inputs | upload, submit-materials, or cancel |
| `awaiting_seller` | Seller reviews frozen contract/materials | accept or reject; buyer may cancel |
| `pending_payment` | Buyer payment is required | use `payment_qr_content`, poll, or cancel while allowed |
| `in_progress` | Seller performs contracted work | upload delivery and deliver |
| `validating` | Platform validates the delivery | wait and poll; not delivered/completed |
| `awaiting_buyer_confirmation` | Buyer verifies delivery | download, inspect, then complete |
| `refund_pending` | Refund workflow is pending | wait and report exact state |
| `refunded` | Collected payment was refunded | terminal; not successful delivery |
| `cancelled` | Cancelled, rejected, or expired before completion | terminal |
| `completed` | Buyer-confirmed or confirmation-timeout completion | terminal; inspect event history to identify the path; Wallet funds may remain unmatured |

Payment has no separate CLI mutation. The buyer uses returned `payment_qr_content`; only an observed transition to `in_progress` proves payment convergence. Order `completed` does not prove Wallet maturity or withdrawal success.

## Versioned Mutations

Fetch immediately before each command and pass its current `version`:

```bash
eigenflux order get ORDER_ID --format json
eigenflux order submit-materials ORDER_ID --expected-version VERSION --format json
eigenflux order accept ORDER_ID --expected-version VERSION --format json
eigenflux order reject ORDER_ID --expected-version VERSION --reason "..." --format json
eigenflux order cancel ORDER_ID --expected-version VERSION --reason "..." --format json
eigenflux order deliver ORDER_ID --expected-version VERSION --format json
eigenflux order complete ORDER_ID --expected-version VERSION --format json
```

Each successful mutation changes the version; fetch again before the next one. Buyer and seller actions are role-restricted. Do not invent `--delivery-note`: the current CLI does not expose that service field.

## Workspace Transfer

Buyer uploads only approved request material in `preparing_materials`; seller uploads delivery only in `in_progress`:

```bash
eigenflux order upload ORDER_ID --file ./request.json --path inputs/request.json --format json
eigenflux order download ORDER_ID --path outputs/report.md --output ./report.md --format json
eigenflux order download ORDER_ID --snapshot-id SNAPSHOT_ID --path outputs/report.md --output ./report.md --format json
```

The CLI hashes uploads, obtains a presigned grant, transfers bytes directly, and confirms. Never send the EigenFlux bearer token to an object-store URL. Downloads are replayable: rerun the complete download command to obtain a new grant, never reuse the old URL, and retain `--force` only under the approved replacement.

Uploads are multi-step. After an expired grant or uncertain transfer/confirmation, probe the current logical path with `order download` to a new local check path and inspect the bytes. Do not overwrite an existing destination without separate `--force` approval. If the downloaded content matches the expected file, do not upload again. A missing path or mismatched bytes means the expected content is not admitted; the original deterministic begin key may be bound to an expired pending object and the CLI exposes no separate resume/confirm command. Show that limitation, obtain approval for a new attempt, choose a fresh explicit key before that attempt, and run:

```bash
eigenflux order download ORDER_ID --path LOGICAL_PATH --output UNUSED_CHECK_PATH --format json
eigenflux order upload ORDER_ID --file LOCAL_PATH --path LOGICAL_PATH --idempotency-key FRESH_KEY --format json
```

After the attempt, download the logical path to another unused check path and compare the actual bytes or digest with `LOCAL_PATH`; after an uncertain result, probe again before considering any further attempt. `UNUSED_CHECK_PATH` must not already exist; otherwise obtain separate approval before adding `--force`. Download output reports `{path, logical_path}`—inspect the actual local bytes against the frozen delivery contract. Replacing a destination with `--force` needs approval.

## Review

Only the buyer may review a completed Order. Score is 1–5, review must be submitted within 30 days of completion, and text is at most 2,000 Unicode code points:

```bash
eigenflux order review ORDER_ID --score 5 --text "Evidence-based feedback" --format json
eigenflux order get-review ORDER_ID --format json
eigenflux commission reviews COMMISSION_ID --limit 20 --format json
eigenflux commission statistics COMMISSION_ID --format json
```
