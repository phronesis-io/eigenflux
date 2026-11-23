# API products: platform-managed products, gateway and stored-value wallet

Status: proposal for review. Baselines: `eigenflux` `main` at `cd77514a`,
`eigenflux-commission` `main` at `10bd232` (2026-11-12). Open questions are
listed at the end and are not decided.

## Problem

The Commission service sells Agent-fulfilled tasks. Every Order runs an
asynchronous, two-party workflow: seller acceptance, Alipay collection with a
24-hour payment window, workspace fulfillment by a bound local Agent, delivery
validation and a 72-hour buyer confirmation window.

We also want to sell API calls, similar to [treg.to](https://treg.to/): one
platform credential reaches many upstream providers, credentials stay
server-side, prices are quoted before execution and the caller pays per call.
An API call is synchronous and has no seller step, so most of the Order workflow
does not apply.

Both need a payment path that does not depend on a payment provider. Today each
Order creates its own Alipay collection.

## Decisions

| Topic | Decision |
|---|---|
| Product ownership | Only the platform registers and operates API products and their upstream integrations. No user-generated products in this phase |
| Platform commission | None. API revenue belongs to the platform; there is no seller share |
| Seller income | None. API calls never touch the income wallet |
| Callers | Same scope as treg: EigenFlux Agents and external applications |
| Caller authentication | Every call is signed with an Ed25519 private key that never leaves the caller. No bearer API secret exists |
| Response retention | The plaintext request and response are retained for 30 days |
| Compliance review of stored value | Out of scope for this proposal |
| Data model | A separate API Call model, not one Order per call (see [Order model compatibility](#order-model-compatibility)) |
| Pricing | One fixed price per call in P1. Per-unit pricing is deferred |
| Order integration | Stored value is a second payment provider behind the existing collection operations. The Order state machine, commands, intents and schema are unchanged |
| Payment source | API calls, and Orders accepted after the switch, are paid only from the stored-value wallet. Alipay funds top-ups only |
| Auto-pay limit | One per-account, per-payment limit applies to API calls and Orders. A payment at or below it needs no human confirmation; above it, an Order waits for Console confirmation and an API call is rejected |
| Cumulative auto-pay cap | None. Only the per-payment limit applies |
| Order refunds | A refund returns to the channel that funded the Order: an Alipay-funded Order refunds through Alipay as today, a wallet-funded Order refunds to the stored-value wallet |
| Rollout order | Wallet and API products ship first. Orders move to the wallet afterwards |
| Stored-value withdrawal | P2 provides no stored-value withdrawal. Refunds remain spendable in the wallet. |

## Goals

1. Platform operators register API products, declare the request and response
   mapping, manage upstream credentials, quotas and connection state.
2. A gateway authenticates signed calls, shapes traffic so a burst cannot
   exhaust an upstream or starve other callers, converts a validated request
   into the product's upstream request, and returns the response synchronously.
3. API calls and Commission Orders are paid from the same stored-value wallet
   and recorded in the same double-entry ledger. The Order workflow knows only
   its payment gateway; adding a funding channel adds a payment provider and
   changes neither Orders nor API calls.
4. Each account has two isolated wallets: a stored-value wallet for top-ups,
   payments and refunds, and the existing income wallet for Commission proceeds
   and withdrawal. Money never moves between them.

## Non-goals

- User-registered products, moderation and seller payouts for API products.
- Bring-your-own upstream keys.
- Promotional credit.
- A daily or cumulative auto-pay cap.
- Bearer API keys or HMAC shared secrets.
- Queueing calls while a product is at capacity (rejected with `Retry-After`).
- Streaming responses and long-running asynchronous API jobs.
- Discovery integration of API products.
- Several products sharing one upstream, or one product routed across backup
  providers. A product maps to exactly one upstream; either case would split
  the upstream mapping into its own entity.

## Order model compatibility

Creating one Order per API call breaks most invariants of the current Order
model in `eigenflux-commission/migrations/000001_platform.sql`:

- `seller_agent_id > 0`, buyer differs from seller, `commission_bps = 2000` and
  `frozen_promised_delivery_ms` between one hour and 30 days.
- Every ready version requires a matching workspace snapshot, enforced by the
  `orders_current_snapshot_matches_version` trigger.
- Order lists, counts, search, `commission_statistics`, reviews, seller
  reputation, notification outbox, Console trade pages and dashboard search
  would all need `kind != api` filters to avoid flooding with call records.
- About ten rows per call, including the hot `commission_statistics` row.

A separate API Call model keeps the Order lifecycle unchanged. The two models
share only the money layer:

| Layer | Relationship |
|---|---|
| Service and database | Same `eigenflux-commission` repository and PostgreSQL database |
| Stored-value wallet | Pays API calls and wallet-funded Orders; receives their refunds |
| Ledger | Reuses `journal_transactions` and `journal_postings` with new `stored_value_liability` and `api_revenue` accounts |
| Income wallet | Untouched |
| Order model | Unchanged: no new state, command, intent or column. Order reaches payment only through `PaymentGateway` (`rpc/order/domain/types.go`), and stored value sits behind it as a payment provider; see [Order payment](#order-payment) |
| Existing consumers | CLI `order` and Console trade lists, statistics and reviews are unaffected. Payment and refund surfaces change; see [EigenFlux repository changes](#6-eigenflux-repository-changes) |

## 1. API product management

A new `rpc/apiproduct` service in `eigenflux-commission`. Administrative routes
reuse the platform-administrator authorization pattern of
`rpc/commission/moderation_admin.go`.

### Data model

```
api_products               (product_id, lifecycle draft|active|suspended|retired,
                            current_revision, created_at, updated_at)
api_product_revisions      (product_id, revision,
                            title, description, input_schema, price_fen,
                            method, url_template, query_map, header_map, body_map,
                            response_map, success_status, timeout_ms,
                            max_request_bytes, max_response_bytes,
                            auth_scheme none|header|bearer|query, auth_param,
                            cost_fen, probe NULL)                       -- immutable
api_product_credentials    (credential_id, product_id, ciphertext, key_version,
                            fingerprint, status active|invalid|retired,
                            last_auth_failure_at, rotated_at)
api_product_limits         (product_id, qps, burst, max_concurrency,
                            max_account_share_bps, daily_call_cap,
                            monthly_budget_fen)
api_product_state_events   (event_id, product_id,
                            dimension lifecycle|circuit|quota|credential,
                            from_state, to_state, reason, actor, created_at)
                            -- append-only
api_product_health_samples (product_id, minute, ok, empty, caller_error,
                            upstream_error, timeout, rate_limited, p50_ms, p95_ms)
                            -- partitioned by month
```

A product is one sellable call bound to one upstream. Its revision carries both
the sold contract (title, input schema, price) and the upstream mapping, so a
price change and a mapping change each create a revision, and each call records
the revision it was charged under. A revision does not name a credential: the
gateway picks an `active` credential from the product's pool, so rotation never
creates a revision.

### Request format

- Caller input is constrained by the revision's `input_schema`, validated with
  the existing JSON Schema validation (`pkg/schemaval`).
- `query_map`, `header_map` and `body_map` are declarative JSON-pointer mappings
  from validated input into the upstream path, query, headers and body.
  `response_map` selects the delivered JSON from the upstream response. No
  scripting or template engine.
- `price_fen` is the fixed price of one call. `cost_fen` is the platform's
  upstream cost per call, used for the monthly budget and margin.

### Credentials

Upstream credentials form one platform-held pool per product,
envelope-encrypted with AES-GCM under a master key from KMS or the environment.
Reads return only the fingerprint. The repository has no encryption facility
today; this is new code. Rotation adds the new credential as `active`, then
retires the old one after in-flight calls finish.

### State maintenance

A product has four independent state dimensions. A call is admitted only when
every dimension allows it:

| Dimension | States | Owner | Storage |
|---|---|---|---|
| Lifecycle | `draft`, `active`, `suspended`, `retired` | Operator | PostgreSQL `api_products` |
| Circuit | `closed`, `open`, `half_open` | Health evaluator | Redis, with every transition in `api_product_state_events` |
| Quota | `available`, `exhausted` | Health evaluator | Redis counters, transitions in `api_product_state_events` |
| Credential | at least one `active` credential | Gateway marks `invalid`; operator rotates | PostgreSQL `api_product_credentials` |

Gateways report each call outcome to per-product 10-second Redis buckets.
Outcomes are classified so only upstream faults affect health:

| Outcome | Counts against health |
|---|---|
| `ok`, `empty` (success with no result) | No; `empty` is published separately |
| `caller_error` (input rejected by the upstream) | No |
| `rate_limited` (upstream 429) | No; reduces the effective rate instead |
| `upstream_error` (5xx, connection failure), `timeout` | Yes |

A single-replica `cmd/api-product-health` worker, built on `pkg/workerloop`,
evaluates every 5 seconds and is the only writer of circuit and quota state:

- `closed -> open`: upstream fault rate of at least 50% over at least 20 calls
  in 60 seconds, or 5 consecutive timeouts. Cooldown starts at 30 seconds and
  doubles on each reopen up to 5 minutes.
- `open -> half_open` after cooldown: admit up to 3 concurrent calls across
  all gateways. 5 successes close the circuit; any fault reopens it.
- Upstream 429: halve the product's effective QPS, honour `Retry-After` by
  pausing admission, then recover by 10% of the configured QPS every 10 seconds.
- Upstream 401 or 403: the gateway marks that credential `invalid` and uses
  another active credential. With no active credential left, the circuit opens
  with reason `credential` and the operator is alerted.
- `exhausted` when the daily call cap or monthly budget is reached; resets at
  the next Asia/Shanghai day or month boundary. Exhaustion is not a fault.
- Thresholds are global defaults; there are no per-product overrides.

Health is passive by default because most upstreams charge per call. A revision
with a `probe` (a fixed input, an interval and an allowed cost) is probed only
while `half_open` without traffic or with no samples for 15 minutes; probe cost
is recorded as platform expense.

The worker writes per-minute `api_product_health_samples` for the catalogue and
history. The catalogue health label is derived: `down` when lifecycle is not
`active` or the circuit is `open`; `degraded` when `half_open`, within 10
minutes after closing, or p95 latency exceeds half the revision timeout;
`unknown` with no samples in 15 minutes; otherwise `healthy`. If the worker
stops, state stays at its last value and its liveness alert fires.

Lifecycle changes take effect on the next call; in-flight calls finish on the
revision and credential they started with. Activating a revision or rolling
back moves `current_revision` atomically.

## 2. Gateway

A new `cmd/api-gateway` Hertz service, deployed separately from
`commission-api` for a fixed egress IP that providers can allowlist, network
isolation from internal services, and its own timeout profile. Redis is a hard
dependency: when it is unavailable the gateway returns 503, because nonces and
shared limits cannot be enforced.

### Routes

| Method | Path | Purpose |
|---|---|---|
| GET | `/v1/products` | Public catalogue of `active` products with price, health, success rate and latency; rate limited per IP |
| POST | `/v1/call/:product_id` | Execute a signed call; requires `Idempotency-Key` |
| GET | `/v1/calls/:call_id` | Read a retained call, including plaintext request and response; requires the `calls:read` scope |

### Caller keys

```
gateway_keys (key_id, agent_id, public_key, fingerprint UNIQUE, label,
              scopes call|calls:read, product_allowlist NULL, cidr_allowlist NULL,
              qps, burst, daily_spend_cap_fen NULL,
              status active|suspended|revoked, expires_at,
              created_delegation_jti, last_used_at, last_used_ip, revoked_at)
```

- EigenFlux Agents sign with their existing CLI installation key
  (`cli/internal/auth/identity.go`, `ed25519-v1`, file mode `0600`), registered
  in `agent_principals`. The gateway resolves the fingerprint through a new auth
  RPC returning `agent_id`, public key and principal status, and caches it for
  30 seconds (`shortcut: revoked device usable for up to 30s, add revocation
  push if that matters`). Agent principals get `call` scope only.
- External applications generate a key pair locally and register only the
  public key in Console. Registration is a Console-delegated operation and
  requires recent email authentication, as adding a device principal does
  (`api/consolev2/principal_handlers.go`). `expires_at` is required, at most
  365 days. `calls:read` is opt-in.
- Revocation and suspension through Console delete the gateway's cache entry
  immediately through Redis.
- The account's commission allowlist (`api/commissionaccess`) applies to both
  key kinds.

### Request signing

```
EF-Key:        <key fingerprint>
EF-Timestamp:  <unix milliseconds>
EF-Nonce:      <at least 128 random bits, base64url>
EF-Signature:  base64url(Ed25519(private_key, canonical))

canonical =
  "EF-GW-V1\n" + METHOD + "\n" + host + "\n" + path + "\n" +
  canonical_query + "\n" + EF-Timestamp + "\n" + EF-Nonce + "\n" +
  Idempotency-Key + "\n" + hex(sha256(body))
```

- `canonical_query` sorts parameters by name and value with RFC 3986
  percent-encoding. Including the host prevents replay across environments.
- The timestamp must be within 5 minutes of gateway time. The nonce is consumed
  with `SET NX` for 10 minutes per key; a reused nonce is rejected.
- The body is read up to a global 1 MiB cap before verification, then checked
  against the revision's `max_request_bytes`.
- A stolen request cannot be altered or replayed; a stolen private key is
  bounded by key expiry, scopes, product and CIDR allowlists, per-key QPS and
  optional daily spend cap, and the account's auto-pay limit per call.
- Signature failures return one generic `401 SIGNATURE_INVALID` and increment a
  per-key failure counter for monitoring.

### Traffic management

| Layer | Protects | Mechanism | Rejection |
|---|---|---|---|
| Instance | Gateway process | In-process in-flight cap, header and body caps, server read/write timeouts | `503 GATEWAY_BUSY` |
| Caller | Other callers and PostgreSQL | Redis token buckets per key and per account | `429 RATE_LIMITED` with `Retry-After` |
| Product | Upstream quota | One Redis Lua call that takes a concurrency lease and a product token, and caps one account at `max_account_share_bps` of the product's concurrency | `429 PRODUCT_BUSY` with `Retry-After` |
| Upstream feedback | Upstream | Effective QPS reduced on 429, see [State maintenance](#state-maintenance) | `429 PRODUCT_BUSY` |
| Budget | Platform cost | Daily call cap and monthly budget | `503 PRODUCT_EXHAUSTED` |

- Concurrency leases are members of a per-product and per-product-account
  sorted set scored by expiry (`timeout + grace`); each acquire first removes
  expired members, so a crashed gateway cannot leak capacity.
- Every rejection happens before the wallet debit: a burst never writes to
  PostgreSQL or charges the caller.
- Bursts are absorbed only up to each bucket's `burst`; the rest is rejected
  with a `Retry-After` computed from the refill time. Callers retry with the
  same `Idempotency-Key`.
- The gateway never retries after request bytes were sent. It retries once only
  when the connection could not be established.
- Each product has its own HTTP transport with `MaxConnsPerHost` set to its
  per-instance share of `max_concurrency`.

### Call lifecycle

```
1  Instance admission                                   -> 503 GATEWAY_BUSY
2  Timestamp window; resolve key (cached)               -> 401
3  Read body; verify signature; consume nonce           -> 401
4  Scope, product and CIDR allowlist, expiry, access    -> 403
5  Idempotency lookup: completed -> replay result;
   in progress -> 409 CALL_IN_PROGRESS;
   different request hash -> 409 IDEMPOTENCY_CONFLICT
6  Caller token buckets                                 -> 429 RATE_LIMITED
7  Product lifecycle, circuit, quota (1 s local cache)  -> 503 PRODUCT_UNAVAILABLE
8  Validate input; price_fen > auto_pay_limit           -> 403 AUTO_PAY_LIMIT_EXCEEDED
9  Product permit (lease, account share, token)         -> 429 PRODUCT_BUSY
10 One transaction: debit price_fen from the account,
   insert api_calls(in_progress, charged_fen = price_fen),
   insert api_call_idempotency                          -> 402 INSUFFICIENT_BALANCE
11 Upstream request with Idempotency-Key: ef-call-<call_id>
12 Insert api_call_payloads
13 One transaction, conditional on state = in_progress:
     billable outcome -> completed
     otherwise -> credit charged_fen back, set it to 0  -> failed
14 Release permit; report outcome; return the response
```

- Steps 1 to 9 do not write PostgreSQL. A call above the auto-pay limit is
  rejected because a synchronous call cannot wait for a human. The error
  carries the current limit and the Console URL where the account owner can
  raise it.
- `ok` is billable. `upstream_error`, `timeout` and `rate_limited` are not, even
  if the upstream may have executed the call; the platform absorbs that cost.
  Empty results and caller errors are free. Reusing the same idempotency key replays the original failure.
- A gateway crash after step 10 leaves the call `in_progress` until
  `expires_at` (`timeout + grace`); the wallet worker then credits the debit
  back and marks it `failed`. Step 13 is conditional, so a completion that
  arrives after expiry changes nothing and the call is free.

### Egress safety

- The host is fixed by the revision's `url_template`. Mapped path values are
  percent-encoded per segment; query values use URL encoding.
- `header_map` cannot set `Host`, `Authorization`, `Cookie`, `Proxy-*` or
  hop-by-hop headers. Credentials are injected after mapping and never logged.
- Redirects are not followed. The dialer rejects loopback, private, link-local
  and metadata addresses after DNS resolution.
- Responses are read up to `max_response_bytes`; `response_map` requires JSON.
- `api_call_payloads.request_body` stores the caller's validated input, never
  the upstream request, so `query`-scheme credentials are not retained.

## 3. Stored-value wallet

The existing `wallets` aggregate is the income wallet. Its schema, API and
`wallet get` semantics are unchanged. The stored-value wallet is a separate
aggregate in the wallet service.

```
stored_value_accounts      (account_id, agent_id UNIQUE, balance_fen,
                            auto_pay_limit_fen DEFAULT 0, version, status)
stored_value_entries       (entry_id, account_id,
                            kind topup|order_payment|order_refund|api_usage,
                            amount_fen, business_reference, created_at)
                            -- append-only, UNIQUE (kind, business_reference)
stored_value_topups        (topup_id, agent_id, amount_fen, order_id NULL, state,
                            payment_reference, created_at, succeeded_at)
stored_value_limit_changes (change_id, account_id, old_limit_fen, new_limit_fen,
                            delegation_jti, created_at)               -- append-only
```

- Debits use a conditional
  `UPDATE ... SET balance_fen = balance_fen - x WHERE balance_fen >= x` in the
  same transaction as their record, keyed idempotently by business reference.
- An entry and its journal transaction share `business_reference`, as an Order
  and its journals do today.
- Top-ups reuse Alipay `collect` with a `topup-<id>` business reference and a
  new `RecordTopup` journal. `payment_order_notifications` currently routes only
  to Orders and `payment_settlements` is keyed by `order_reference`; both need a
  top-up target. Statement matching by provider transaction ID is unchanged.

### Auto-pay limit

- `auto_pay_limit_fen` defaults to `0`: every wallet-funded Order needs
  confirmation and every priced API call is rejected until the account owner
  sets a limit.
- The limit and Order confirmations change only through Console. Both handlers
  require a Console delegation token (`pkg/consoledelegation`, new operations
  `wallet.auto_pay_limit.update` and `order.payment.confirm`, bound to the
  request body and idempotency key) and reject Agent access tokens and gateway
  keys. Otherwise an Agent could confirm its own spending or raise its own limit.
- Every change is recorded in `stored_value_limit_changes`, so the limit in
  force for any API call can be reconstructed without a per-call record.

### API usage billing

- `api_calls` records the debit; `charged_fen` is the per-call bill line.
- An hourly worker creates one `api_usage_batches` row, one journal transaction
  and one `api_usage` entry per account and hour. The batch for hour `P` runs
  after `P` ends plus the longest call lifetime and 5 minutes. It asserts that
  no call created in `P` is still `in_progress`, then sums `completed` calls by
  `created_at` in `P`. `UNIQUE (account_id, period_start)` makes reruns no-ops.
- Reconciliation asserts per account:
  `balance_fen = topups + order_refunds - order_payments - api_usage
  - charged_fen of calls not yet batched`, and that each batch equals the sum
  of its calls.

```
api_usage_batches (batch_id, account_id, period_start, period_end, call_count,
                   charged_fen, upstream_cost_fen, journal_transaction_id)
                   UNIQUE (account_id, period_start)
```

### Statement

The user's statement lists `stored_value_entries` newest first: top-ups, Order
payments and refunds with the Order ID and frozen title, and hourly API usage.
The current hour's usage is computed from `api_calls` and marked pending. An
`api_usage` line drills down to its calls by account and period. CLI and Console
show the same statement.

### Order payment

Stored value is a second payment provider. Payment already records a `provider`
on every `payment_operations` row, and Order already creates, observes, closes
and refunds a collection only through `PaymentGateway`, so the Order service
does not change.

- Payment holds two providers, `alipay` and `stored_value`. One configuration
  value selects the provider for new Order `collect` operations. `close`,
  `refund` and reconciliation use the provider recorded on the original
  `collect` operation, so an Order never changes channel.
- `create_collection` inserts the `collect` operation as `pending`, as today. A
  `stored_value` operation has no QR content.
- Payment resolves a pending `stored_value` operation by calling
  `Wallet.PayOrder(order-<id>, gross, deadline)`. The existing reconcile loop
  runs it. A new operation is due for reconciliation 30 seconds after creation
  today; a `stored_value` operation is due immediately. Console confirmation
  and top-up success also request an immediate attempt.

| Condition | `PayOrder` result |
|---|---|
| Gross at most the limit and balance sufficient | Authorize `auto`, debit, `succeeded` |
| Gross above the limit, no authorization | `pending`; `order payment` returns a Console confirmation action |
| Authorized, balance insufficient | `pending`; `order payment` returns a top-up action for at least the shortfall |
| Row closed or deadline passed | `failed`; funds already topped up stay in the wallet |

The wallet keeps one row per wallet-funded Order. `PayOrder`, confirmation and
close lock it, so a debit and a close cannot both succeed:

```
stored_value_order_payments (order_reference PK, buyer_agent_id, amount_fen,
                             deadline_at, state open|paid|closed,
                             authorization ''|auto|console|topup,
                             limit_fen_at_decision, delegation_jti NULL,
                             topup_id NULL, paid_at NULL, created_at, updated_at)
```

- A Console confirmation records `authorization = console` for that exact
  Order and amount. A top-up started from an Order records `topup` for it: the
  person paying in Alipay already approved that Order.
- A successful debit writes the `order_payment` entry in the same transaction
  and makes the operation `succeeded`, with the entry ID as its provider
  transaction ID. From there the existing path runs unchanged: the payment
  worker delivers `ObservePayment`, Order records the journal through
  `RecordCollection` and applies `CommandPaymentConfirmed`.
- The wallet debits only while the row is `open` and before `deadline_at`, so
  `late_payment_detected` never fires for a wallet-funded Order.
- `close_collection` closes the row. Closing a row that is already `paid`
  fails, as closing a paid Alipay trade does, and the existing
  `payment_recovered` or late-refund path takes over.
- The wait reason and shortfall are computed when `order payment` is read, from
  the row, the balance and the limit. They are not stored.
- No notification kind is added. The existing `pending_payment` notification
  prompts the buyer's Agent to read `order payment`.
- Payment now calls Wallet, and Wallet already calls Payment for payouts and
  settlement facts. The two services depend on each other.

### Order refund

`request_refund` creates a `refund` operation as today. For a `stored_value`
operation Payment calls `Wallet.RefundOrder(order-<id>, gross)`, which credits
the balance and writes the `order_refund` entry in one transaction and returns
`succeeded`. `ProcessRefundIntent` already handles a refund that succeeds
synchronously, so `refund_pending` resolves in the same intent run. An
Alipay-funded Order refunds through Alipay as today.

### Settlement

`SettleCompletion` (`rpc/payment/reconciliation/service.go`) requires a
succeeded `collect` operation, its funding journal and its
`payment_settlements` row. A `stored_value` operation satisfies all three with
two differences, both selected by the operation's provider:

- It verifies the `order_payment` journal instead of the `collection` journal.
- Its settlement row is `cleared` when the debit succeeds, because no provider
  statement line will arrive. The settlement reconciliation check exempts these
  rows from the statement requirement.

The 15-day `wallet_credits` maturity, the withdrawal settlement check and the
completion reconciliation check are unchanged.

### Ledger postings

New accounts: `stored_value_liability` (owner type `stored_value`) and
`api_revenue` (owner type `platform`).

| Event | Debit | Credit |
|---|---|---|
| Top-up succeeded | `platform_alipay_cash` | `stored_value_liability` |
| API usage (hourly batch per account) | `stored_value_liability` | `api_revenue` |
| Order paid from wallet | `stored_value_liability` | `buyer_payment_clearing(order)` |
| Wallet-funded Order refunded | `buyer_payment_clearing(order)` | `stored_value_liability` |
| Alipay-funded Order paid or refunded (unchanged) | As today | As today |
| Order completion (unchanged) | `buyer_payment_clearing` | `commission_revenue` + `wallet_liability` |
| Stored-value withdrawal | Unavailable | Unavailable |

Upstream cost is recorded on calls and batches for budgets and margin; posting
provider invoices to the ledger is out of scope.

### Isolation

1. Separate tables and aggregates; no transfer API exists.
2. No accounting function posts to both `wallet_liability` and
   `stored_value_liability`; a test asserts no journal transaction touches both.
3. `Withdraw` reads only the income wallet; API debits, `PayOrder` and
   `RefundOrder` read only the stored-value wallet.

## 4. Data model changes

### API calls

```
api_calls            (call_id, account_id, key_fingerprint, product_id,
                      product_revision, credential_id, idempotency_key,
                      request_hash, state in_progress|completed|failed,
                      charged_fen, upstream_cost_fen, expires_at,
                      outcome ok|empty|caller_error|upstream_error|timeout|rate_limited,
                      upstream_status, latency_ms, error_code,
                      created_at, finished_at)        -- partitioned by day on created_at
api_call_idempotency (account_id, idempotency_key, call_id, request_hash,
                      created_at)                     -- PK (account_id, idempotency_key), not partitioned
api_call_payloads    (call_id, created_at, request_body, response_body)
                                                      -- partitioned by day
```

- `call_id` is a Snowflake ID, so `GET /v1/calls/:call_id` derives the
  partition from the ID's timestamp.
- A partitioned table cannot carry a global unique constraint without the
  partition key, so idempotency lives in `api_call_idempotency`, purged after 7
  days.
- `api_calls` is a billing record and follows financial retention.
  `api_call_payloads` follows the 30-day plaintext retention period
  and is removed by dropping partitions.

### Orders, Payment and Wallet

| Service | Change |
|---|---|
| Order | None. `orders`, `order_events`, `order_workflow_intents` and `order_notification_outbox` keep their schema and kinds |
| Payment | A `stored_value` provider beside `alipay`, chosen per operation; a configured provider for new Order collections; the two settlement differences above; `RecordCollection` and `RecordRefund` post the stored-value pairs for a `stored_value` operation; `PaymentInstruction` returns the confirmation or top-up action for one |
| Wallet | `PayOrder`, `RefundOrder`, closing and Console confirmation on `stored_value_order_payments` |
| Trade API | The Order DTO gains `funding_source` (`alipay` or `stored_value`), read from the `collect` operation's provider |

## 5. Throughput

- A successful call performs one conditional update on its
  `stored_value_accounts` row; a failed call adds a credit. The single-account
  row is the hot spot, estimated at a few hundred QPS per account; this is not
  load tested. Mark it in code as
  `shortcut: single-account hot row, upgrade to Redis credit blocks above ~300 QPS`.
- Later, for hot accounts the gateway reserves a block of balance (for example
  CNY 10) in PostgreSQL, decrements it atomically in Redis with Lua per call,
  and writes call records asynchronously in batches.

## 6. EigenFlux repository changes

- Auth RPC: resolve an `ed25519-v1` principal fingerprint to `agent_id`,
  public key and status for the gateway.
- CLI:
  - New `api call` (signs with the installation key), `api products`,
    `wallet topup` and `wallet statement`.
  - `wallet balance` reports `income` and `stored_value` separately, including
    `auto_pay_limit_fen`. The CLI cannot change the limit or register gateway
    keys.
  - `order payment` keeps the `payment_action{provider,type,url,expires_at}`
    shape. An Alipay-funded Order returns the Alipay redirect as today. For a
    wallet-funded Order `provider` is `eigenflux` and `type` is `confirm`
    (Console URL) or `topup` (Alipay top-up URL, with the shortfall); a paid
    Order returns no action. Help text states that Orders are paid from
    balance.
  - `watch_desktop_copy.go` `pending_payment` copy distinguishes awaiting
    confirmation from insufficient balance; refund copy names the destination
    from `funding_source`.
- Console V2 trade BFF: top-up, balance and statement, auto-pay limit setting,
  Order payment confirmation, gateway key registration and usage pages. Limit,
  confirmation and key routes issue Console delegation tokens.
- `ef-commission` Skill: `order.md` Buyer Payment presents the confirmation or
  top-up action and never confirms on the buyer's behalf; `wallet.md` explains
  the balance, statement and limit; API product reference added. The funding
  disclosure changes from "Orders are paid through Alipay" to "balance top-ups
  are Alipay-only", kept consistent with `api/consolev2/capability_registry.go`
  `fundingDisclosures` and
  [Commission disclosure points](../../dev/commission.md#disclosure-points).
- Documentation: `docs/dev/commission.md` and the Commission repository
  `CONTEXT.md` gain Stored-Value Wallet, Auto-Pay Limit, API Product, API Call
  and Gateway Key. `CONTEXT.md` Payment ("collected into the platform Alipay
  account") and Wallet ("not a deposit") are revised; Refund ("to its original
  source") stays true.

## Cutover

Orders move to the wallet by one configuration switch. No collection is closed
or migrated:

1. Before the switch, every Order `collect` operation uses `alipay`.
2. After the switch, a new Order `collect` operation uses `stored_value`.
3. An Order whose `alipay` collection already exists stays on Alipay until it
   is paid, expires, completes or refunds, on the existing code.
4. The Alipay Order-collection path, including `payment_qr_content`, is deleted
   once no Alipay-funded Order is non-terminal. `close_collection`,
   `payment_recovered` and the late-refund path stay: the wallet channel uses
   them.

## Phasing

| Phase | Scope |
|---|---|
| P0 | Stored-value wallet: top-up, balance, statement, auto-pay limit |
| P1 | API product management and state maintenance, gateway with signing and traffic management, API Call model and usage billing, gateway keys, fixed pricing |
| P2 | Orders paid from the wallet: `stored_value` payment provider, Console confirmation, settlement differences, cutover |
| P3 | Per-unit pricing (adds a hold with `max_charge_fen` and `units`, captured after the call), Redis credit blocks for hot accounts, per-product threshold overrides, key anomaly detection, discovery `api` kind, streaming and asynchronous jobs |

## Open questions

1. External developer identity. The draft assumes external developers register
   an EigenFlux account through Console email login and own keys and wallets by
   `agent_id`. A developer account independent of Agents needs a separate
   identity design.
2. Promotional credit and bring-your-own upstream keys are deferred.
