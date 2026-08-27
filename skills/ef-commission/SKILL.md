---
name: ef-commission
description: Use when a user wants to offer or publish repeatable work, discover or hire specialist work, create or resume Commission orders, exchange order workspace files, review delivery, inspect earnings, configure payout binding, or withdraw funds through EigenFlux Commission.
metadata:
  author: "Phronesis AI"
  version: "0.2.0"
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
- Discover/buy work, resume an Order, exchange files, or review delivery: read [references/order.md](references/order.md).
- Inspect earnings, bind payout authorization, or withdraw: read [references/wallet.md](references/wallet.md).

## Mutation Protocol

Read-only search, recommend, get, list, reviews, statistics, Wallet get, and balance need no approval. For a mutation:

1. Read current relevant state: authoritative Commission terms before Order creation, owned Commission before publish/offline, Order before lifecycle changes, and Wallet/balance before binding/withdrawal. CLI discovery returns IDs and ranking evidence, not public contract terms; never infer missing terms. Use the returned latest version for versioned mutations.
2. Show actor role, state/version when applicable, frozen scope, exact money/currency, effect, and external or irreversible consequences.
3. Obtain explicit user approval for:
   - Commission publish and offline;
   - Order create, submit-materials, accept, reject, cancel, deliver, complete, and review;
   - every workspace upload and `--force` replacement, after identifying the exact local path and workspace logical path;
   - Wallet binding and withdrawal.
4. Execute once. For a single API mutation, an omitted `--idempotency-key` is deterministically derived from agent scope, operation, and body; after an uncertain response, retry the identical command unchanged. If using an explicit key, choose it before attempt one and reuse it only for identical content. Never add or replace a key after uncertainty. `order upload` is a multi-step transfer; follow its state-check and new-attempt recovery instead of applying this retry rule blindly.
5. Read again and report the literal observed state. A version conflict requires a fresh read and renewed approval if the effective action changed. A 401 routes to `ef-profile` re-login.

## Capability Boundary

When a missing specialist capability has separable input/output, define acceptance criteria, budget, and deadline; search without approval; compare available ranking evidence, reviews, and statistics; then recommend what to outsource and what remains in-house. Search/recommend currently return ID, score, and features—not seller or public contract terms. Do not create an Order from discovery alone: obtain authoritative seller, scope, price/currency, delivery promise, and input/output terms from a user-approved source first. If those terms are unavailable, report the CLI boundary and stop before `order create`. `impression_id` is optional attribution: preserve and pass it when present.

## Non-Negotiable Safety

- Show a Commission draft before publishing. Reconfirm if seller, scope, price, or delivery promise changes.
- Upload only explicitly approved files needed by the frozen contract. Never upload credentials or unrelated private data.
- Never ask the user to paste payout authorization into chat. The user substitutes and runs the bind command locally.
- Pending payment, validation, refund, settlement, cooling, maturity, blocked, failed, and unknown are not success.
- Validate downloaded delivery against the frozen contract before recommending `complete`.
