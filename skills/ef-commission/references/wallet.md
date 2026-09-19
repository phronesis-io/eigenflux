# Wallet, Binding, KYC, and Withdrawals

Use Wallet commands for seller earnings and payouts. Follow the preflight and mutation protocol in `SKILL.md`. Financial mutations require explicit approval. EigenFlux deducts its 20% platform commission from the completed Order price before crediting the seller's 80% net; Wallet amounts are seller-side amounts after that deduction.

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

Binding returns `cooling_until`, not proof of KYC or payout eligibility. Check KYC separately. Cooling and credit maturity apply unless the service grants an exemption; a retained cooling timestamp alone does not prove a blocker. A waiting-period whitelist does not bypass KYC or explicit platform bans.

## Verify the Bound Alipay Account

Require CLI 0.0.107 or later and a Commission deployment with browser KYC configured. Read the current Wallet binding and its authoritative KYC status:

```bash
eigenflux wallet get --format json
eigenflux wallet kyc get --format json
```

Use the current Agent and bound Alipay account; do not ask for another Alipay login or pass an account override. Binding through UID/OpenID is not KYC. Do not use the binding's immutable `kyc_status` snapshot to overrule `wallet kyc get`.

Obtain explicit consent to submit the owner's legal name and mainland identity-card number for comparison with the bound account. Never collect these values or authorization codes in chat, command arguments, shell history, project files, or agent-visible tool input/output. Have the owner supply private JSON through a trusted local process pipe and run these commands locally; do not invent a pipe helper or insert real data into a shell template:

```text
eigenflux wallet kyc start --stdin --idempotency-key START_KEY --format json
```

1. `start` accepts only JSON fields `user_name` and `cert_no` (18 characters, final checksum letter uppercase `X`). It returns `verification.verification_id`, `binding_id`, `state`, `expires_at`, and `authorization_url` with `authorization_expires_at` while pending. Keep decimal IDs as strings; timestamps are Unix milliseconds.
2. Give the owner the returned `authorization_url` to open privately. Do not fetch or preview it yourself: it is a one-use link, valid for at most ten minutes. Keep the entire flow in that browser; do not forward the link or callback URL. The Commission API redirects to Alipay `id_verify` and binds the callback to the original actor, verification and browser cookie.
3. The owner authorizes the bound Alipay account. The server validates the callback, uses the fresh authorization code and completes the comparison automatically. Do not ask the owner to copy the code, reuse binding authorization, or call `alipay.user.info.share`. A generic browser completion page is not proof of a passed comparison.
4. Read `wallet kyc get` again. Only `verified` satisfies KYC for that binding; no attempt returns `not_assessed` with verification ID `"0"`. Rebinding requires new KYC. Do not start another attempt unnecessarily: a new attempt supersedes the previous one, even if it was verified.

For an existing pending attempt whose link is missing, consumed, or expired, run `eigenflux wallet kyc authorize VERIFICATION_ID --format json` within the approved KYC scope. This returns a fresh link without resubmitting identity data or starting a new attempt. Do not keep renewing links automatically. If browser configuration is unavailable, report the server's required `ALIPAY_KYC_CALLBACK_URL`/application setup and stop; do not invent a callback URL.

Choose a non-sensitive 1–64 byte start key before submitting. Retry start only with that same key and exact private input. After an uncertain callback, read status first; if still pending and unexpired, obtain a fresh link and user authorization. Use the returned deadline; pending attempts expire after two hours. Manual `wallet kyc complete VERIFICATION_ID --stdin --idempotency-key COMPLETE_KEY` remains for an explicitly selected external integration: stdin accepts only `authorization` from a fresh, validated `id_verify` callback, never from chat.

- `preparing` or `pending`: not verified; inspect state before deciding whether to resume.
- `rejected`: comparison failed; obtain corrected input locally and approval for a new attempt.
- `failed` or `expired`: start a new approved attempt with a new start key; do not loop on the old key.
- HTTP 409: inspect binding, attempt state and retry identity before proceeding. HTTP 422: report the business code, not an inferred mismatch or cooling failure. HTTP 429: honor retry-after. If the required recovery or trusted authorization flow is unavailable, stop and explain the blocker.

`WALLET_KYC_REQUIRED` means KYC is missing; `WALLET_BLOCKED` means an explicit platform wallet/account ban. Historical provider `risk_status=not_assessed` alone is not an Alipay payout gate, and no platform ban means unblocked—not proof of a provider risk assessment. Never rewrite risk to `clear`, bypass KYC, or clear a ban as a workaround.

## Withdraw

Before withdrawal, read current Wallet, KYC and balance again. Require `verified` for the current Alipay binding, no explicit platform ban, sufficient `withdrawable_fen`, and any non-exempt waiting periods. KYC success is not withdrawal success. Confirm the exact positive amount in fen and CNY:

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
