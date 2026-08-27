# Wallet, Binding, and Withdrawals

Use Wallet commands for seller earnings and payouts. Follow the preflight and mutation protocol in `SKILL.md`. Financial mutations require explicit approval.

## Read Wallet State

```bash
eigenflux wallet get --format json
eigenflux wallet balance --format json
```

Explain fields separately:

- `total_fen`: all credited funds.
- `unmatured_fen`: credited funds not yet eligible for withdrawal.
- `reserved_fen`: funds allocated to a `pending` or `unknown` withdrawal.
- `withdrawn_fen`: funds consumed by successful withdrawals.
- `withdrawable_fen`: funds currently eligible for a new withdrawal.

Order `completed`, Wallet credit, maturity, withdrawability, withdrawal creation, and withdrawal success are distinct events. Never describe total or unmatured funds as available cash.

## Bind Payout Authorization

Payout authorization is sensitive. Do not ask the user to paste it into chat, print it, save it in project files, or execute a substituted value through an agent-visible tool. After approval, give this template; the user substitutes and runs it locally:

```bash
eigenflux wallet bind --authorization '<paste locally>' --format json
```

Binding returns `cooling_until`. Success does not make funds immediately withdrawable; wait for cooling and credit maturity, then re-read Wallet and balance state.

## Withdraw

Before withdrawal, read balance again, verify sufficient `withdrawable_fen`, and confirm the exact positive amount in fen and CNY:

```bash
eigenflux wallet balance --format json
eigenflux wallet withdraw --amount-fen AMOUNT --format json
eigenflux wallet withdrawals --limit 20 --format json
eigenflux wallet withdrawals --cursor NEXT_CURSOR --limit 20 --format json
eigenflux wallet withdrawal WITHDRAWAL_ID --format json
```

Continue withdrawal listing while `next_cursor` is nonzero. States are literal:

- `pending`: accepted but unfinished; check later.
- `unknown`: provider outcome is unknown; check later and do not retry as a new withdrawal.
- `succeeded`: withdrawal succeeded.
- `failed`: report `last_error_code` when present without inventing a remedy.

`provider_operation_reference` is an opaque external operation reference, not proof of bank settlement.
