# Commission

The Commission service (`eigenflux-commission`, default `http://localhost:8090/api/v1`)
owns the catalogue, orders, workspace transfers, reviews, wallets and
withdrawals. This repository owns discovery projection and ranking, the gateway
facade, the Console V2 trade BFF, the CLI, and the `ef-commission` Skill.

## Funding channel

Payments and payouts currently support Alipay in CNY only:

- A buyer pays an Order through Alipay. No other payment channel exists, and an
  Order left unpaid past its deadline is cancelled.
- A seller is paid to a bound Alipay account. Binding accepts no other account
  type.

Withdrawal additionally compares the owner's legal name and mainland
identity-card number against the bound account. Commission refuses the
withdrawal with `WALLET_KYC_REQUIRED` until that passes, and the KYC start route
requires an 18-character `cert_no` (`api/tradebff/kyc.go`). A normal mainland
Alipay account already satisfies the comparison, so it is a step inside the
payout flow, not a qualification a seller must obtain first: disclose it there,
not before listing. A non-mainland Alipay account cannot pass the 18-character
check.

The limit is a product fact, not a configuration switch: no environment variable
enables another channel. Treat it as current rather than permanent, and never
publish a timeline for additional channels.

## Disclosure points

The platform cannot detect whether a user has Alipay, so every surface discloses
the limit before the work it would waste. None of them blocks an operation; this
is disclosure, not enforcement. Keep these consistent when the wording changes:

| Surface | Where | Discloses |
|---|---|---|
| Skill | `skills/ef-commission/SKILL.md` `## Funding Channel` | Both gates, plus the rule against offering another channel |
| Skill | `references/commission.md` `## Confirm Payout Eligibility First` | Seller gate before the interview, listing fields, and fulfillment skill |
| Skill | `references/order.md` intro and `## Create` | Buyer gate before material preparation and `order create` |
| Skill | `references/wallet.md` intro and binding section | Alipay-only payouts and the verification requirement |
| CLI help | `commission`, `order`, `wallet` roots and their payment-relevant subcommands | The limit in `Long` text, which `cliHelps` injects into the Skill |
| CLI runtime | `commission create`, `commission publish`, `order create` | `fundingChannelNotice` on stderr, once per server profile; stdout output contracts are unchanged |
| Capability registry | `api/consolev2/capability_registry.go` `fundingDisclosures` | Bilingual operation descriptions for hosts and Console |

The seller gate runs before the seller interview; the buyer gate runs before
material preparation. Both sit ahead of any irreversible or billable step.

Each surface establishes the limit once instead of repeating it, so a seller who
already funds through Alipay is not reminded on every listing or Order:

- The Skill's seller gate reads `wallet get`. An existing Alipay binding answers
  the question from state, so the Agent proceeds without raising it.
- The Skill's buyer gate states the limit once per conversation and skips a
  buyer who has already paid an Order.
- The CLI stamps `_commission_funding_notice_at` per server profile and prints
  the stderr notice only the first time. The stamp is CLI-private and is never
  exported or silenced by settings sync. Command help keeps the full statement
  available afterwards.

## Surfaces owned elsewhere

These disclose the same limit but are not changed from this repository:

- Commission service responses and its public sharing projection
  (`/api/v1/public/commissions/*`), owned by `eigenflux-commission`.
- The product website and its Console trade, earnings and payout pages. The
  Console V2 trade BFF currently returns no payment-method field; the website
  states the limit itself.
