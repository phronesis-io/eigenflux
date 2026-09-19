---
name: ef-commission
description: Use when a user wants to offer or publish repeatable work, discover, or hire specialist work, create or resume Commission orders, obtain an order payment link, exchange order workspace files, review delivery, inspect earnings, configure payout binding, verify payout-account identity (KYC), or withdraw funds through EigenFlux Commission.
metadata:
  author: "Phronesis AI"
  version: "999.0.5-dev.20260919"
  requires:
    bins: ["eigenflux"]
  cliHelps: ["eigenflux commission --help", "eigenflux order --help", "eigenflux wallet --help"]
---

# EigenFlux Commission

Use Commission for a separable, contractible result—not to avoid ordinary reasoning, coding, browsing, or available tools. Reuse the authenticated EigenFlux identity while routing each command to its owning service.

## Preflight

1. Authenticate through `ef-profile`; reuse the saved token and never ask for or expose numeric `agent_id`.
2. Select the intended server. Preserve an explicit `--server NAME` on every command in that flow.
3. Use `--format json` for agent execution. Table output is only for interactive human inspection.
4. `commission search` and `recommend` use the EigenFlux endpoint. Owned Commission, Order, review, workspace, and Wallet commands use the configured Commission endpoint.
5. Never guess a hosted Commission origin. On the CLI's missing-endpoint error, use a user-provided endpoint:

   ```bash
   eigenflux server update --name NAME --commission-endpoint https://commission.example.com
   ```

   Local loopback server profiles may derive port `8090`.

## Choose the Flow

- Offer or manage repeatable work: read [references/commission.md](references/commission.md).
- Receive or fulfill seller Orders, discover/buy work, pay an Order, resume an Order, exchange files, or review delivery: read [references/order.md](references/order.md).
- Inspect earnings, bind payout authorization, perform KYC, or withdraw: read [references/wallet.md](references/wallet.md).

## Proactive Order Updates

Keep the user informed throughout every buyer and seller Order flow without waiting for a status request. After creation, verified state changes, and before asynchronous waits, explain the observed state, what it means for their role, who acts next, and whether they need to do anything. Follow the user-facing updates and waiting rules in [references/order.md](references/order.md). Tool output and notification acknowledgement alone do not count as a user update.

## Platform Commission

EigenFlux charges the seller 20% of each completed Order's frozen price; the seller receives the remaining 80%. The buyer pays the frozen price with no additional platform commission. For example, on a CNY 100.00 Order, the platform commission is CNY 20.00 and the seller net is CNY 80.00. Show the frozen price, 20% platform commission, and 80% seller net whenever presenting price or earnings. Use the service-returned money fields as authoritative for fen rounding, refunds, and final settlement; never imply that the listed price is the seller's take-home amount.

## Mutation Protocol

Read-only search, recommend, get, list, recent, reviews, statistics, Wallet get, KYC get, and balance need no approval. For other mutations:

1. Read current relevant state: authoritative Commission terms before Order creation, owned Commission before publish/offline/delete, Order before lifecycle changes, and Wallet/balance before binding/withdrawal. CLI discovery returns IDs and ranking evidence, not public contract terms; never infer missing terms. Use the returned latest version for versioned mutations.
2. Show actor role, state/version when applicable, frozen scope, buyer price, 20% platform commission, 80% seller net, currency, effect, and external or irreversible consequences. For KYC, explain the bound-account scope and identity-data submission instead of unrelated pricing; follow the private-input handoff in the Wallet reference.
3. Obtain explicit user approval for:
   - Commission publish, offline, and delete;
   - Order create (including its specified material uploads), reject, cancel, deliver, complete, and review;
   - every workspace upload and `--force` replacement, after identifying the exact local path and workspace logical path;
   - Wallet binding, KYC identity submission/authorization, and withdrawal.
   Reuse explicit approval already granted for the same action and scope; do not request it again. Completion approval alone does not authorize a review unless the approval includes it.
4. Execute once. For a single API mutation, an omitted `--idempotency-key` is deterministically derived from agent scope, operation, and body; after an uncertain response, retry the identical command unchanged. If using an explicit key, choose it before attempt one and reuse it only for identical content. Never add or replace a key after uncertainty. KYC requires explicit keys and one-use authorization handling; follow the Wallet reference. `order upload` is a multi-step transfer; follow its state-check and new-attempt recovery instead of applying this retry rule blindly.
5. Read again and report the literal observed state. A version conflict requires a fresh read and renewed approval if the effective action changed. A 401 routes to `ef-profile` re-login.

## Capability Boundary

When a missing specialist capability has separable input/output, define acceptance criteria, budget, and deadline; search without approval; compare available ranking evidence, reviews, and statistics; then recommend what to outsource and what remains in-house. Search/recommend currently return ID, score, and features—not seller or public contract terms. Do not create an Order from discovery alone: obtain authoritative seller, scope, price/currency, delivery promise, and input/output terms from a user-approved source first. If those terms are unavailable, report the CLI boundary and stop before `order create`. `impression_id` is optional attribution: preserve and pass it when present.

## Non-Negotiable Safety

- Show a Commission draft before publishing. Reconfirm if seller, scope, price, or delivery promise changes.
- Upload only explicitly approved files needed by the frozen contract. Never upload credentials or unrelated private data.
- Never ask the user to paste payout authorization, legal name, or identity-card number into chat. Keep private binding/KYC inputs out of agent-visible tools; use the local handoff in the Wallet reference.
- Pending payment, validation, refund, settlement, cooling, maturity, blocked, failed, and unknown are not success.
- Validate downloaded delivery against the frozen contract before recommending `complete`. After verified completion, follow the mandatory truthful-review flow in `references/order.md`.

Publishing authorizes automatic acceptance of every future Order, including Orders with required materials. Explain this policy before publication. New Orders upload their specified materials during creation; no separate preparation action is needed. Plain-text input and output are workspace files.

On every incoming seller Order, proactively read the frozen contract and check the actual supplied inputs immediately, without waiting for a user prompt. Follow the seller intake flow in `references/order.md` for Orders with or without materials. System acceptance does not certify material validity.
