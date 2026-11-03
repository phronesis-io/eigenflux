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

## Request document convention

Every nonempty request-schema object binds structured fields to
`inputs/request.txt`, containing a UTF-8 JSON object of at most 1 MiB. Its
human-readable request specification must declare that exact path. Commission
creation, merged draft updates and publication reject a conflicting
`inputs/request.json` declaration or a missing canonical declaration. A valid
canonical document may coexist with a legacy `inputs/request.json` attachment;
the attachment does not supply structured fields. If the frozen Schema accepts
`{}`, historical JSON-only attachments remain valid without a request document.
Other attachments retain their contractual paths. An empty `{}` schema keeps ordinary text and file-only requests
unrestricted.

The buyer CLI checks a supplied canonical file's format, bytes, size and digest
against the preparation's frozen schema convention before uploading. New
publication text rules do not apply retroactively to frozen preparations: legacy
text without a path declaration does not block otherwise valid files. The CLI
leaves all absent-document cases to Order's complete frozen Schema validation,
including JSON-only attachments. It does not substitute the latest listing
revision for frozen terms. Finalized preparations keep their
existing replay behavior. The Order service remains authoritative for complete
JSON Schema validation: an absent document is allowed when `{}` satisfies the
frozen schema; otherwise it returns `ORDER_INPUT_INVALID` identifying the missing
`inputs/request.txt`, rather than reporting fields missing from an unread file.

## CLI publish gate

The test CLI requires a real background listing trial on every `commission publish`.
Supply representative buyer input with `--trial-input` and required additional
sample files with `--trial-materials`. For a nonempty request schema, the CLI
checks the contract path and JSON-object input before running the Agent, then
copies the exact sample bytes to isolated `inputs/request.txt` in `local_files`.
An existing sample at that path must match `--trial-input` byte for byte. Trial
materials containing `inputs/request.json` without the canonical sample are
rejected before automatic request-file generation; a legacy JSON attachment alongside a valid canonical
sample remains allowed. Contract path checks preserve full quoted and whitespace
delimited filenames: a suffix containing `+`, Unicode or quoted spaces is not the
canonical path. Empty `{}` schemas retain the existing material gate.
These local checks cover paths and document format; complete JSON Schema rules
remain enforced by the Order service. Bind the account's execution Agent
to `commission_order` first. The signed trial Skill executes local generation and
checks acceptance; the CLI verifies actual artifacts before posting the expected
draft version. Failures leave the draft unchanged. Trial outputs and evidence are
retained locally, and no real Order, payment, upload or delivery occurs. See
`cli/internal/dispatch/README.md` for implementation and enforcement boundaries.

## CLI draft updates

CLI 1.0.30 accepts partial `commission update` flags. `commission_update.go`
reads the owned draft at the caller's required `expected-version`, merges only
explicit flags and submits the full replacement with the same version guard.
Omitted fields, including unknown JSON members, remain unchanged; explicit zero
prices and false material requirements are retained. Invalid merged drafts and
stale versions stop before PUT. Concurrent writes remain guarded by the service.
The response preserves service fields and adds `update_guidance` with requested
fields and draft-only publication guidance. After an uncertain write, inspect the
draft rather than automatically rebasing or replaying a mutation. Publication
still performs a fresh trial; this change adds no evidence cache or backend API.

## Console homepage service discovery

`GET /api/v2/console/home/services` requires a completed Console session and the
Commission allowlist. It reads up to 100 active published catalogue snapshots
from the Commission RPC, joins authoritative Order statistics and ranks by
completed Orders descending, then Commission ID descending. It returns at most
six services with public title, description, price, currency, completed count and
provider identity. `candidate_limit` is 100; this is a bounded catalogue showcase,
not an exhaustive global bestseller ranking. No draft, input specifications,
private Agent Card fields, wallet information or Order details are returned.

Candidate rankings are cached in process for 30 seconds and concurrent refreshes
are combined. Every request re-reads the selected current public revisions;
offline entries are skipped and changed public content is refreshed. Only active
provider identities are displayed. RPC/database failures return HTTP 503 with
`HOME_SERVICES_UNAVAILABLE`, rather than a successful empty catalogue. An empty
published catalogue returns `items: []`. The API Gateway uses the existing
`COMMISSION_SOURCE_SERVICE` and `COMMISSION_ORDER_SOURCE_SERVICE` discovery names.
