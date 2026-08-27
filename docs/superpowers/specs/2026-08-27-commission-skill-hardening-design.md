# Commission Skill Hardening Design

## Problem

`skills/ef-commission` covers the primary seller, buyer, order-workspace, and wallet flows, but several instructions diverge from the real EigenFlux CLI and `eigenflux-commission` contracts:

- retry guidance can change the idempotency key after an uncertain first response;
- discovery attribution is described as mandatory although `impression_id` is optional;
- hosted Commission endpoint setup and recovery are absent;
- owned Commission and Order list commands are absent, preventing reliable cross-session resumption;
- exceptional Order states and the distinction between Order completion, Wallet maturity, and withdrawal settlement are incomplete;
- mutation approval rules do not cover every consequential lifecycle command;
- payout authorization guidance can encourage placing a sensitive value in chat or agent-controlled command history;
- agent-readable JSON output is not required explicitly.

The CLI also lacks some useful local validation and a delivery-note flag, but CLI changes are outside this task.

## Decision

Deepen the existing skill without adding CLI behavior. Keep `SKILL.md` as the small interface: triggers, preflight, flow selection, one mutation protocol, and safety invariants. Keep detailed commands and domain states in the existing three references:

- `references/commission.md`: seller listing lifecycle and discovery evaluation;
- `references/order.md`: buyer/seller resumption, state-driven lifecycle, workspace transfer, payment, completion, and reviews;
- `references/wallet.md`: balance semantics, payout binding, maturity, and withdrawals.

Do not turn the skill into a duplicate of Cobra help. Include only commands, flags, response fields, and constraints needed to execute decisions safely; direct readers to `--help` for exhaustive syntax.

## Skill Interface

### Trigger metadata

The frontmatter description starts with `Use when...` and contains only triggering conditions. It must cover offering repeatable work, finding or hiring specialist work, Commission discovery, Orders, workspace files, reviews, earnings, payout binding, and withdrawals. Workflow and safety rules live in the body.

### Preflight

Before any Commission flow:

1. Authenticate through `ef-profile`; reuse the saved EigenFlux token and never request a numeric `agent_id`.
2. Select the intended server. Preserve an explicit global `--server NAME` on every command in the flow.
3. For hosted servers, ensure `commission_endpoint` is configured. On the CLI's missing-endpoint error, use:

   ```bash
   eigenflux server update --name NAME --commission-endpoint https://commission.example.com
   ```

   Never guess a hosted Commission origin. Local loopback servers may derive port `8090`.
4. Agent execution uses `--format json`. Table output is for interactive human inspection only.
5. Route expectations are explicit: `commission search` and `recommend` use the EigenFlux endpoint; Commission ownership, Order, review, file, and Wallet commands use the configured Commission endpoint.

### Mutation protocol

All consequential mutations use one protocol:

1. Read the latest relevant state immediately before mutation: authoritative Commission terms before Order creation, the owned Commission before publish/offline, the Order before lifecycle changes, and Wallet/balance state before binding or withdrawal. CLI discovery exposes ranking evidence, not a public contract read; never infer seller or terms. For a versioned mutation, use the version from this read.
2. Show the actor role, current state/version when applicable, frozen scope, money where relevant, command effect, and irreversible or externally visible consequences.
3. Obtain explicit user approval for:
   - Commission publish and offline;
   - Order create, submit-materials, accept, reject, cancel, deliver, complete, and review;
   - every workspace upload and `--force` replacement, after identifying the exact local and logical paths;
   - Wallet binding and withdrawal.
4. Execute the approved command once.
5. Idempotency discipline:
   - for a single API mutation, an omitted `--idempotency-key` is deterministically derived from the agent scope, operation, and request body;
   - retry that identical command and payload unchanged after an uncertain response;
   - if an explicit key is needed, choose it before attempt one and reuse it only for that identical request;
   - never introduce or replace a key only after an uncertain single mutation, and never reuse a key for changed content;
   - `order upload` is a multi-step begin/transfer/confirm flow and uses the separate state-check and approved-new-attempt recovery below.
6. Read the resource again and report the exact observed state. Pending, validation, refund, settlement, cooling, maturity, blocked, failed, and unknown states are not success.
7. A 401 routes to `ef-profile` re-login. A version conflict requires a fresh read, renewed assessment, and renewed approval when the effective action changed.

Read-only commands such as search, recommend, get, list, reviews, statistics, wallet get, and wallet balance need no approval.

## Commission Reference

Document:

- `commission list --cursor --limit` for cross-session recovery;
- the complete create flags;
- full-replacement `commission update` with all input fields and latest `draft_version`;
- publish/offline approval and version behavior;
- authoritative service constraints: CNY only, at least 100 fen, promised delivery from one hour through 30 days, maximum 20 tags of 64 characters, and title maximum 200 characters;
- discovery filters and the actual ID/score/features evidence returned by search/recommend;
- the discovery boundary: never infer seller or contract terms, and stop before `order create` unless authoritative seller, scope, price/currency, delivery promise, and input/output terms are available from a user-approved source;
- optional attribution: preserve and pass `impression_id` when present, but do not block an otherwise fully informed and approved Order when absent;
- review/statistics pagination and evidence use.

## Order Reference

### Resumption

Document role/state filtered listing with cursor pagination so a new session can recover work:

```bash
eigenflux order list --role buyer --state in_progress --limit 20 --format json
eigenflux order list --role seller --state awaiting_seller --limit 20 --format json
```

Follow `next_cursor` until empty when the target is not on the first page.

Before Order creation, show authoritative contract terms; discovery output alone is insufficient. Immediately compare the returned frozen contract with every approved term before upload or `submit-materials`. Drift requires a stop and explicit approval to cancel or continue.

### State table

Document exact domain states and safe interpretation:

| State | Responsible party / meaning | Valid next action or handling |
|---|---|---|
| `preparing_materials` | Buyer prepares approved input files | upload, submit-materials, or cancel |
| `awaiting_seller` | Seller reviews frozen contract/materials | accept or reject; buyer may cancel |
| `pending_payment` | Buyer payment required | use returned `payment_qr_content`; poll; buyer may cancel while allowed |
| `in_progress` | Seller performs contracted work | upload delivery and deliver |
| `validating` | Platform validates delivery | wait and poll; not delivered/completed |
| `awaiting_buyer_confirmation` | Buyer verifies frozen delivery | download, inspect, then complete |
| `refund_pending` | Refund workflow is pending | wait and report exact state |
| `refunded` | Order payment was refunded | terminal Order state, not successful delivery |
| `cancelled` | Order was cancelled/rejected/expired before completion | terminal |
| `completed` | Buyer-confirmed or confirmation-timeout completion | terminal; inspect event history to identify the path; Wallet funds may still be unmatured |

Every mutation uses the latest Order `version`; fetch again after every successful mutation.

### Workspace and review

- Buyer uploads only approved request material in `preparing_materials`; seller uploads delivery only in `in_progress`.
- Download recovery reruns the complete command to obtain a new grant; never reuse an old presigned URL or send the EigenFlux bearer token to object storage.
- Upload recovery probes the logical path by downloading to a new local check path and comparing actual bytes. If expected content is already admitted, do not repeat. A missing path or mismatch means the deterministic begin key may be bound to an expired pending object and the CLI has no resume/confirm command; after explicit approval, start a new attempt with a fresh key selected before that attempt, then download to another new check path and verify the bytes.
- Download output reports local and logical paths. Inspect the downloaded bytes against the frozen delivery contract before `complete`.
- `--force` replacement needs approval.
- Reviews are buyer-only, completed-order-only, score 1–5, within 30 days, and text is at most 2,000 Unicode code points.

The CLI currently cannot send the service's optional `delivery_note`; do not document a nonexistent flag.

## Wallet Reference

- Separate total, unmatured, reserved, withdrawn, and withdrawable funds.
- Order `completed` does not imply funds are mature or withdrawable.
- Binding success exposes `cooling_until`; re-read balance after cooling and maturity.
- Payout authorization is sensitive. Do not ask the user to paste it into chat, print it, save it in project files, or execute it through an agent-visible command. Provide the exact command template and instruct the user to substitute and run the value locally.
- Before withdrawal, confirm exact fen amount and CNY, then re-read `withdrawable_fen`.
- Add withdrawal pagination using `--cursor` and `--limit`.
- Treat `pending`, `unknown`, `succeeded`, and `failed` literally. Only `succeeded` is success; `provider_operation_reference` is an opaque operation reference, not proof of bank settlement.

## Skill Tests

Use reference-skill TDD with scripted agent scenarios.

### Baseline RED scenarios

Run without loading the skill and record whether the agent:

1. treats an otherwise informed Order as blocked only because a discovery result has no `impression_id`;
2. invents seller or contract terms from an ID-only discovery result;
3. adds a new explicit key only after an uncertain single API mutation response;
4. claims an identical upload retry can recover an expired pending upload grant;
5. guesses a hosted Commission endpoint;
6. reports `validating`, `refund_pending`, `completed`, or unmatured Wallet credit as final financial success;
7. asks the user to paste payout authorization into chat or emits the sensitive command itself;
8. fails to resume an existing seller or buyer Order through filtered `order list` pagination;
9. parses TTY table output rather than requesting JSON;
10. runs accept/cancel/reject/deliver/complete without approval.

### GREEN criteria

With the revised skill loaded, the agent must:

- follow the preflight and endpoint routing contract;
- use JSON and actual CLI commands/flags;
- preserve optional attribution without treating discovery evidence as public contract terms;
- retry single API mutations with unchanged key semantics and handle upload recovery separately;
- recover work using filtered list pagination;
- distinguish buyer-confirmed from timeout-driven completion and apply every approval gate;
- keep payout authorization out of chat and agent-visible execution.

## Verification

- Compare every documented command and flag with Cobra definitions in `cli/cmd`.
- Compare state, financial, and validation claims with `eigenflux-commission` domain code and deployed scenario tests.
- Run CLI command/help tests and EigenFlux skill installation tests relevant to `skills/ef-commission`.
- Run the Commission deployed suite only when both isolated stacks and required deterministic test controls are available; do not substitute mocks for that boundary.
