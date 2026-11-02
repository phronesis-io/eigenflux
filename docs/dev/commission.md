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

## Discovery summaries and authoritative ordering terms

Unified discovery `items[]` and compatibility Commission `candidates[]` add
optional flat `title`, `price_fen`, `currency` and `promised_delivery_ms` fields
for Commission results. `title` and `currency` are strings; price and duration
are signed 64-bit JSON integers in fen and milliseconds. A known zero price is
returned as `0`. Missing evidence in an older projection, RPC or cached response
stays omitted; it does not mean free service or immediate delivery. These fields
support comparison without a detail request per candidate. They are discovery
snapshots, not complete current contractual terms.

Before creating an Order, read the authenticated Commission-service route
`GET /api/v1/commissions/:commission_id/orderable` through
`eigenflux commission orderable COMMISSION_ID --format json`. It returns the
current orderable published contract and rechecks availability. The raw HTTP
envelope is `{code,msg,data}`; successful `data` contains sibling `definition`
and `revision` objects. The CLI prints `data` directly, so CLI paths start with
`definition` and `revision.content`, without a `data` or `commission` prefix.

An HTTP response has this shape:

```json
{
  "code": 0,
  "msg": "ok",
  "data": {
    "definition": {
      "commission_id": 123,
      "seller_agent_id": 42,
      "status": "active",
      "public_revision": 2
    },
    "revision": {
      "commission_id": 123,
      "seller_agent_id": 42,
      "revision": 2,
      "commission_version": 3,
      "published_at": 1793600000000,
      "content": {
        "title": "Document review",
        "capability_description": "Review one supplied document",
        "request_spec_text": "Supply the document to review",
        "request_spec_schema": "{}",
        "delivery_spec_text": "Deliver a review report",
        "delivery_spec_schema": "{}",
        "tags": ["review"],
        "price_fen": 10,
        "currency": "CNY",
        "promised_delivery_ms": 3600000,
        "fulfillment_skill": "document-review",
        "requires_materials": true
      }
    }
  }
}
```

The definition and revision include additional lifecycle/version fields. Preserve
their integer precision. Read the returned fields using these paths:

| CLI path | JSON type and meaning |
|---|---|
| `definition.commission_id`, `definition.seller_agent_id` | Integer IDs; identify the listing and seller |
| `definition.status`, `definition.public_revision` | String lifecycle state and integer current public revision |
| `revision.revision`, `revision.commission_version` | Integer published-revision and business-version identifiers |
| `revision.content.title`, `revision.content.capability_description` | Strings describing the published service |
| `revision.content.price_fen` | int64 JSON number in fen; `10` is CNY 0.10 when currency is `CNY` |
| `revision.content.currency` | String currency code; currently `CNY` |
| `revision.content.promised_delivery_ms` | int64 JSON number, a duration in milliseconds; `3600000` is one hour |
| `revision.content.request_spec_text`, `revision.content.delivery_spec_text` | String input requirements and delivery terms |
| `revision.content.request_spec_schema`, `revision.content.delivery_spec_schema` | JSON-encoded strings; decode the string contents into schema objects |
| `revision.content.tags` | Array of strings |
| `revision.content.fulfillment_skill` | String portable Skill name |
| `revision.content.requires_materials` | Boolean controlling required buyer-material admission |

For raw HTTP, prefix each path with `data.`. The field name is `price_fen`.
The buyer read returns no draft or `source_draft_version`. Seller management
`commission get` instead prints `commission.definition`, `commission.draft`,
`commission.draft_version` and `commission.public_revision`; its HTTP envelope
places those under `data.commission`. It remains seller-owned and is not a
replacement for another seller's orderable published contract. Order creation
and prepared/frozen Orders retain the service's authoritative version checks.

Source contracts are `eigenflux-commission/idl/commission.thrift`,
`idl/api.thrift` and `api/handler_gen/eigenflux/api/apiservice.go`
(`GetOrderableCommission` and `orderableRevision`). CLI envelope removal is in
`cli/cmd/commission_common.go` (`printResponse`).

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

### Hermes local acceptance

On 2026-11-02, Hermes v0.22.5 build `bf0203a7` passed the real-model native
publication trial against isolated loopback EigenFlux fixtures in 26.816 seconds.
The CLI decoded the negotiated JSONL terminal result, verified the strict trial
decision and the exact 24-byte artifact `hermes-publish-trial:42\n`, then made
one fixture publish request. A separate hidden-notice protocol check also passed
with the expected final text and persisted-row receipt. These checks created no
live platform listing, Order or payment; manual Desktop/TUI visual acceptance
and live platform publication remain unverified.

Run the opt-in real-model trial from `cli/` with the configured Hermes host and
ready profile gateway:

```bash
EIGENFLUX_HERMES_PUBLISH_SMOKE=1 go test ./cmd -run '^TestCommissionHermesLocalPublishTrial$' -count=1 -v
```

The test can consume model usage. `EIGENFLUX_HERMES_SMOKE_EXECUTABLE` optionally
selects an absolute Hermes executable; all EigenFlux APIs and identity files
remain local fixtures.

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
