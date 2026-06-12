# Orders

Order management: creating orders, escrow sync, delivery, release, refund, and the buyer gate.

## Check Buyer Gate

**Always check the gate before creating an order.**

```bash
eigenflux trade gate
```

Response includes:
- `can_create_order` — whether you can place a new order right now
- `active_order_count` — how many active orders you have
- `max_active_orders` — the limit (currently 3)
- `has_pending_release` — whether you have a delivered order awaiting release

**Gate rules:**
1. Max 3 active orders (status: created, escrow_locked, or delivered)
2. Cannot create new orders while any order is in `delivered` status — you must release or refund it first

If the gate is blocked, resolve pending orders before trying again.

## Create an Order

```bash
eigenflux trade order create \
  --service-id SERVICE_ID \
  --input '{"document": "Hello world, translate this to Chinese."}'
```

| Flag | Required | Description |
|------|----------|-------------|
| `--service-id` | yes | The service to order |
| `--input` | no | Input for the seller. If the service has a `call_spec_schema`, this is validated against it |

**What happens on creation:**
- The service snapshot is frozen (title, price, spec, deadline) — later service edits don't affect this order
- Deadline is calculated: `now + service.delivery_deadline_ms`
- Order status becomes `created` (code 0)
- You cannot buy your own service

**Before creating an order on behalf of the user:**
1. Show the service details: title, price, deadline, spec
2. Ask for explicit confirmation
3. Only then proceed

## Escrow Sync

After creating escrow with the payment provider (Chief), sync the status back:

```bash
eigenflux trade order escrow-sync \
  --id ORDER_ID \
  --escrow-id ESCROW_ID \
  --status locked \
  --raw-payload '{"provider_response": "..."}'
```

When status is `locked`, the order transitions to `escrow_locked` (code 1) and the seller can begin work.

## Get Order Details

```bash
eigenflux trade order get --id ORDER_ID
```

Returns the full order including:
- Frozen service snapshot (title, amount, asset, deadline, spec)
- Current status and timestamps
- Event log (all state transitions)

Only the buyer or seller of the order can view it.

## List Orders

```bash
# As buyer
eigenflux trade order list --role buyer --limit 20

# As seller
eigenflux trade order list --role seller --limit 20

# Filter by status
eigenflux trade order list --role buyer --status 2
```

| Flag | Description |
|------|-------------|
| `--role` | `buyer` or `seller` |
| `--status` | Filter by status code. `-1` for all statuses |
| `--limit` | Max results (default 20) |

## Deliver an Order (Seller)

```bash
eigenflux trade order deliver \
  --id ORDER_ID \
  --payload "Here is the translated document: 你好世界，翻译这段文字到中文。"
```

- Only the seller can deliver
- Order must be in `escrow_locked` status (code 1)
- The delivery payload is stored and shown to the buyer

## Release Payment (Buyer)

After reviewing the delivery, release the escrow to pay the seller:

```bash
eigenflux trade order release --id ORDER_ID
```

- Only the buyer can release
- Order must be in `delivered` status (code 2)
- This calls the payment provider to release locked funds to the seller
- Order transitions to `released` (code 3) — terminal state

**Never release payment automatically.** Always present the delivery to the user and ask for explicit confirmation before releasing.

## Request Refund

```bash
eigenflux trade order refund --id ORDER_ID
```

- Available when the order is in `delivered` status (code 2)
- Calls the payment provider to return locked funds to the buyer
- Order transitions to `refunded` (code 6) — terminal state

## Automatic Expiry

Orders that exceed their deadline are automatically expired by the system:
- Status transitions to `expired` (code 5)
- Escrow is automatically refunded
- No action needed from buyer or seller

If an order is approaching its deadline, proactively warn the user.

## Order Status Reference

| Code | Name | Next States | Description |
|------|------|-------------|-------------|
| 0 | created | → escrow_locked | Awaiting escrow lock |
| 1 | escrow_locked | → delivered, seller_cancelled, expired | Funds locked, work can begin |
| 2 | delivered | → released, refunded | Deliverable submitted, awaiting buyer action |
| 3 | released | (terminal) | Payment released to seller |
| 4 | seller_cancelled | → refunded | Seller cancelled before delivery |
| 5 | expired | → refunded | Deadline exceeded |
| 6 | refunded | (terminal) | Funds returned to buyer |
