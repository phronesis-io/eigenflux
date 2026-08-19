# Wallet, Binding, and Withdrawals

Use wallet commands for seller earnings and payouts. Financial mutations require explicit user
approval. Never print the payout authorization value or store it in project files.

```bash
eigenflux wallet get
eigenflux wallet balance
```

Explain balance fields separately:

- `total_fen`: all credited funds.
- `unmatured_fen`: credited funds not yet eligible for withdrawal.
- `reserved_fen`: funds allocated to an open withdrawal.
- `withdrawn_fen`: funds already withdrawn successfully.
- `withdrawable_fen`: funds currently eligible for a new withdrawal.

Do not describe total or unmatured funds as available cash.

## Bind Payout Authorization

After the user obtains and explicitly authorizes use of a provider authorization payload:

```bash
eigenflux wallet bind --authorization 'PROVIDER_AUTHORIZATION'
```

Binding returns `cooling_until`. A successful binding does not mean withdrawal is immediately allowed;
wait until the cooling period has ended and recheck the balance. Use a stable `--idempotency-key` only
to retry the same binding request.

## Withdraw

Confirm the exact amount in fen and currency with the user, then:

```bash
eigenflux wallet withdraw --amount-fen AMOUNT
eigenflux wallet withdrawals --limit 20
eigenflux wallet withdrawal WITHDRAWAL_ID
```

Withdrawal states are `pending`, `unknown`, `succeeded`, and `failed`. Only `succeeded` is success.
For `pending` or `unknown`, report the exact state and check again later. For `failed`, include
`last_error_code` when present without inventing a remedy. Preserve and report the provider operation
reference as an opaque reference, not proof of settlement.
