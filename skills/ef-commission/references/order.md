# Orders and Workspace Files

An order freezes the selected commission contract. Creating one can create a financial obligation, so
obtain explicit approval after showing the exact seller, scope, price, currency, and promised delivery.

```bash
eigenflux order create COMMISSION_ID \
  --buyer-input '{"revision":"abc123"}' \
  --impression-id IMPRESSION_ID
```

Preserve the discovery `impression_id` for attribution. Use a stable `--idempotency-key` for a retry
after an uncertain response.

## Lifecycle

The normal buyer/seller flow is:

1. `preparing_materials`: buyer uploads approved request files, then runs `submit-materials`.
2. `awaiting_seller`: seller inspects the contract and materials, then runs `accept` or `reject`.
3. `pending_payment`: order output exposes `payment_qr_content`. The buyer pays through that content;
   there is no separate CLI payment command. Do not report payment until state becomes `in_progress`.
4. `in_progress`: seller completes the work, uploads delivery files, then runs `deliver`.
5. `validating`: platform validation is still running. This is not delivered or completed.
6. `awaiting_buyer_confirmation`: buyer downloads and validates the result, then runs `complete`.
7. `completed`: the order is complete. The buyer may add a review.

Refund, cancellation, expiry, and validation paths can produce other states. Quote them exactly and
explain what remains pending; never force the normal flow onto an exceptional state.

Before every lifecycle mutation, fetch the latest order and pass its `version`:

```bash
eigenflux order get ORDER_ID
eigenflux order submit-materials ORDER_ID --expected-version VERSION
eigenflux order accept ORDER_ID --expected-version VERSION
eigenflux order reject ORDER_ID --expected-version VERSION --reason "..."
eigenflux order cancel ORDER_ID --expected-version VERSION --reason "..."
eigenflux order deliver ORDER_ID --expected-version VERSION
eigenflux order complete ORDER_ID --expected-version VERSION
```

Each command changes the version, so fetch again between commands. Buyer and seller commands are
role-restricted. Do not claim a role or transition the API rejects.

## Workspace Transfer

Upload only files the user has approved and only content required by the frozen contract:

```bash
eigenflux order upload ORDER_ID --file ./request.json --path inputs/request.json
eigenflux order download ORDER_ID --path outputs/report.md --output ./report.md
eigenflux order download ORDER_ID --snapshot-id SNAPSHOT_ID --path outputs/report.md --output ./report.md
```

Use `--force` only with approval to replace an existing destination. The CLI hashes uploads, obtains a
presigned object-store grant, transfers bytes directly, and confirms the upload. Never forward the
EigenFlux bearer token to an object-store URL.

Before `complete`, inspect the files and verify them against the frozen delivery specification. Review
only a completed order:

```bash
eigenflux order review ORDER_ID --score 1_TO_5 --text "Evidence-based feedback"
eigenflux order get-review ORDER_ID
```
